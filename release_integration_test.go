package monty_test

import (
	"context"
	"errors"
	monty "github.com/camdenclark/monty-go"
	"reflect"
	"testing"
	"time"
)

func TestMontyV1ValuesAndSuspensionPosition(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	want := monty.Time{Hour: 12, Minute: 30}
	if got := run(t, session, "from datetime import time\ntime(12, 30)"); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := run(t, session, "items[0]", monty.FeedOptions{Inputs: map[string]any{"items": []any{want}}}); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	got := run(t, session, "class Point:\n    def __init__(self, x):\n        self.x = x\nPoint(42)")
	instance, ok := got.(monty.ClassInstance)
	if !ok || instance.Type.Name != "Point" || len(instance.ID) != 16 {
		t.Fatalf("class instance: %#v", got)
	}
	p, err := session.FeedStart(context.Background(), "host_value")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := p.(*monty.NameLookupSnapshot)
	if snapshot.Position.End <= snapshot.Position.Start || snapshot.Position.Filename == "" {
		t.Fatal(snapshot.Position)
	}
	if _, err := snapshot.Resume(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestMontyV1PerFeedDurationAndSystemSleep(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxFeedDuration: time.Millisecond, MaxTurnDuration: time.Millisecond}})
	_, err := session.FeedRun(context.Background(), "while True: pass")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("duration error: %v", err)
	}
	if got := run(t, session, "21 * 2"); got != int64(42) {
		t.Fatal(got)
	}
	normal := integrationSession(t, pool)
	if got := run(t, normal, "import time\ntime.sleep(0.001)\n42"); got != int64(42) {
		t.Fatal(got)
	}
	if got := run(t, normal, "import asyncio\nawait asyncio.sleep(0.001)\n42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestMontyV1SuspensionLimit(t *testing.T) {
	session := integrationSession(t, integrationPool(t), monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxSuspensions: 2}})
	_, err := session.FeedRun(context.Background(), "for i in range(10):\n    host_tick()", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_tick": func() int { return 1 }}})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("suspension limit error: %v", err)
	}
	if got := run(t, session, "42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestMontyV1InputContainersAreCopied(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	shared := []any{int64(1)}
	sharedMap := map[string]any{"x": int64(1)}
	got := run(t, session, "a[0] = 9\nc['x'] = 9\n(a is b, b[0], c is d, d['x'])", monty.FeedOptions{Inputs: map[string]any{"a": shared, "b": shared, "c": sharedMap, "d": sharedMap}})
	want := monty.Tuple{false, int64(1), false, int64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input copy semantics: %#v want %#v", got, want)
	}
	if shared[0] != int64(1) || sharedMap["x"] != int64(1) {
		t.Fatal("Python mutated host inputs")
	}
}

func TestMontyV1OutputContainersShareReferences(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	got := run(t, session, "shared = [1]\n[shared, shared]").(monty.List)
	got[0].(monty.List)[0] = int64(9)
	if got[1].(monty.List)[0] != int64(9) {
		t.Fatal("worker shared list became copies")
	}
	dicts := run(t, session, "d = {'x': 1}\n[d, d]").(monty.List)
	dicts[0].(monty.Dict)[0].Value = int64(9)
	if dicts[1].(monty.Dict)[0].Value != int64(9) {
		t.Fatal("worker shared dictionary became copies")
	}
}

func TestMontyV1ArenaMetadata(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	offset := -7 * 3600
	zone := "PDT"
	want := monty.Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, OffsetSeconds: &offset, TimezoneName: &zone, Fold: 1}
	if got := run(t, session, "from datetime import time, timezone, timedelta\ntime(1, 2, 3, 4, timezone(timedelta(hours=-7), 'PDT'), fold=1)"); !reflect.DeepEqual(got, want) {
		t.Fatalf("time metadata: %#v", got)
	}
	got := run(t, session, "from dataclasses import dataclass\n@dataclass\nclass Point:\n    x: int\nPoint(42)")
	instance, ok := got.(monty.ClassInstance)
	if !ok || !instance.Type.IsDataclass || !reflect.DeepEqual(instance.Attrs, monty.Dict{{Key: "x", Value: int64(42)}}) {
		t.Fatalf("dataclass metadata: %#v", got)
	}
}
