package monty_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestSuspensionSourcePosition(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	prefix := "label = 'é'\n"
	name := "host_value"
	p, err := session.FeedStart(context.Background(), prefix+name)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := p.(*monty.NameLookupSnapshot)
	if snapshot.Position.Filename == "" || snapshot.Position.Start != uint32(len(prefix)) || snapshot.Position.End != uint32(len(prefix+name)) {
		t.Fatalf("source byte offsets: %#v", snapshot.Position)
	}
	if _, err := snapshot.Resume(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestSystemSleepCompletes(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, code := range []string{"import time\ntime.sleep(0.001)\n42", "import asyncio\nawait asyncio.sleep(0.001)\n42"} {
		if got := run(t, session, code); got != int64(42) {
			t.Fatal(got)
		}
	}
}

func TestSuspensionLimit(t *testing.T) {
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

func TestInputContainersAreCopied(t *testing.T) {
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

func TestOutputContainersShareReferences(t *testing.T) {
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

func TestArenaMetadata(t *testing.T) {
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

func TestComplexBoundaryValues(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, tc := range []struct {
		name  string
		input any
		want  complex128
	}{
		{"complex128", complex128(1.5 - 2.25i), 1.5 - 2.25i},
		{"complex64", complex64(1.5 - 2.25i), 1.5 - 2.25i},
		{"zero", complex128(0), 0},
		{"negative zeros", complex(math.Copysign(0, -1), math.Copysign(0, -1)), complex(math.Copysign(0, -1), math.Copysign(0, -1))},
		{"infinities", complex(math.Inf(1), math.Inf(-1)), complex(math.Inf(1), math.Inf(-1))},
		{"nan", complex(math.NaN(), 1), complex(math.NaN(), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, session, "value", monty.FeedOptions{Inputs: map[string]any{"value": tc.input}}).(complex128)
			for i, pair := range [][2]float64{{real(got), real(tc.want)}, {imag(got), imag(tc.want)}} {
				if math.IsNaN(pair[1]) {
					if !math.IsNaN(pair[0]) {
						t.Fatalf("part %d lost NaN: %v", i, got)
					}
				} else if math.Float64bits(pair[0]) != math.Float64bits(pair[1]) {
					t.Fatalf("part %d changed: %v, want %v", i, got, tc.want)
				}
			}
		})
	}
	got := run(t, session, "[1+2j, {3-4j: 5+6j}, complex]")
	want := monty.List{complex128(1 + 2i), monty.Dict{{Key: complex128(3 - 4i), Value: complex128(5 + 6i)}}, monty.Type("complex")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested complex values: %#v", got)
	}
	got = run(t, session, "transform(1+2j)", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"transform": func(z complex64) complex64 { return z * (2 - 1i) },
	}})
	if got != complex128(4+3i) {
		t.Fatalf("complex callback: %#v", got)
	}
	got = run(t, session, "await transform_async(1+2j)", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"transform_async": monty.Async(func(z complex128) complex128 { return z * (2 - 1i) }),
	}})
	if got != complex128(4+3i) {
		t.Fatalf("async complex callback: %#v", got)
	}
}

func TestComplexSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	p, err := session.FeedStart(ctx, "z = 1+2j\nz + host_value()")
	if err != nil {
		t.Fatal(err)
	}
	call := p.(*monty.FunctionSnapshot)
	blob, err := call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	p, err = restored.LoadSnapshot(ctx, blob)
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.(*monty.FunctionSnapshot).Resume(ctx, complex128(3-4i))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*monty.Complete).Output; got != complex128(4-2i) {
		t.Fatalf("restored complex result: %#v", got)
	}
}
