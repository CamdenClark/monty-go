package monty_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestOSPolicyClocksAndSleep(t *testing.T) {
	pool := integrationPool(t)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	for _, policy := range []monty.OSPolicy{
		{FixedDateTime: &fixed, TimeZone: "America/Phoenix", Sleep: monty.SleepZero},
		{FixedDateTime: &fixed, FixedTimeZone: &monty.TimeZone{OffsetSeconds: -7 * 3600}, Sleep: monty.SleepZero},
	} {
		session := integrationSession(t, pool, monty.CheckoutOptions{OSPolicy: policy})
		got := run(t, session, "from datetime import date, datetime\nimport time, asyncio\ntime.sleep(60)\nawait asyncio.sleep(60)\n(datetime.now().isoformat(), date.today().isoformat(), time.time(), time.process_time())")
		want := monty.Tuple{"2026-01-01T20:04:05.000006", "2026-01-01", float64(fixed.Unix()) + 0.000006, float64(0)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frozen clock: %#v, want %#v", got, want)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
	session := integrationSession(t, pool, monty.CheckoutOptions{OSPolicy: monty.OSPolicy{ProcessTime: monty.ProcessTimeElapsed}})
	if got := run(t, session, "import time\nsum(range(10000))\ntime.process_time() > 0"); got != true {
		t.Fatalf("elapsed process clock: %#v", got)
	}
}

func TestOSPolicyHostCallbacks(t *testing.T) {
	session := integrationSession(t, integrationPool(t), monty.CheckoutOptions{OSPolicy: monty.OSPolicy{
		DateTime: monty.DateTimeCallHost, Sleep: monty.SleepCallHost, Random: monty.RandomCallHost,
	}})
	calls := map[string]int{}
	date := monty.Date{Year: 2026, Month: 1, Day: 2}
	dateTime := monty.DateTime{Year: 2026, Month: 1, Day: 2, Hour: 3}
	got := run(t, session, "import time, random, asyncio\nfrom datetime import date, datetime\ntime.sleep(60)\nawait asyncio.sleep(60)\nrandom.random()\nrandom.random()\n(time.time(), date.today(), datetime.now())", monty.FeedOptions{
		OS: func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
			calls[name]++
			switch name {
			case "time.time":
				return 1234.5, nil
			case "date.today":
				return date, nil
			case "datetime.now":
				return dateTime, nil
			case "time.sleep", "asyncio.sleep":
				return nil, nil
			case "os.urandom":
				return make([]byte, args[0].(uint64)), nil
			default:
				return monty.NotHandled, nil
			}
		},
	})
	if !reflect.DeepEqual(got, monty.Tuple{1234.5, date, dateTime}) || !reflect.DeepEqual(calls, map[string]int{"time.time": 1, "date.today": 1, "datetime.now": 1, "time.sleep": 1, "asyncio.sleep": 1, "os.urandom": 1}) {
		t.Fatalf("host policy: %#v, calls %#v", got, calls)
	}
}

func TestOSPolicyRandomSeedMatchesPython(t *testing.T) {
	pool := integrationPool(t)
	for _, seed := range []any{int64(-257), int64(-128), int64(-1), int64(0), int64(127), int64(128), uint64(^uint64(0)), new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 130)), 1.25, "", "seed", []byte{}, []byte{0, 255}} {
		t.Run(fmt.Sprintf("%T/%v", seed, seed), func(t *testing.T) {
			seeded := integrationSession(t, pool, monty.CheckoutOptions{OSPolicy: monty.OSPolicy{RandomSeed: seed}})
			control := integrationSession(t, pool)
			got := run(t, seeded, "import random\n(random.random(), random.getrandbits(64))")
			want := run(t, control, "import random\nrandom.seed(seed)\n(random.random(), random.getrandbits(64))", monty.FeedOptions{Inputs: map[string]any{"seed": seed}})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %v: %#v, want %#v", seed, got, want)
			}
		})
	}
}

func TestVirtualCwdAndPrintBatching(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	session := integrationSession(t, pool, monty.CheckoutOptions{PrintFlushInterval: time.Second})
	if got := run(t, session, "import os\nos.getcwd()", monty.FeedOptions{Cwd: "/workspace"}); got != "/workspace" {
		t.Fatal(got)
	}
	if got := run(t, session, "os.getcwd()", monty.FeedOptions{Cwd: "/next"}); got != "/next" {
		t.Fatal(got)
	}
	if got := run(t, session, "os.getcwd()"); got != "/next" {
		t.Fatal(got)
	}
	if _, err := session.FeedRun(ctx, "42", monty.FeedOptions{Cwd: "relative"}); err == nil {
		t.Fatal("relative Cwd accepted")
	}
	var chunks []string
	got := run(t, session, "print('one')\nprint('two')\nbarrier()", monty.FeedOptions{
		PrintCallback: func(_ monty.PrintStream, text string) error { chunks = append(chunks, text); return nil },
		ExternalLookup: monty.ExternalLookup{"barrier": func() int {
			if !reflect.DeepEqual(chunks, []string{"one\ntwo\n"}) {
				t.Errorf("output was not batched and flushed before host call: %#v", chunks)
			}
			return 42
		}},
	})
	if got != int64(42) {
		t.Fatal(got)
	}
	blob, err := session.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	if err := restored.LoadSession(ctx, blob); err != nil {
		t.Fatal(err)
	}
	if got := run(t, restored, "os.getcwd()"); got != "/next" {
		t.Fatalf("restored cwd: %v", got)
	}
}

func assertSleepBudgetError(t *testing.T, err error) {
	t.Helper()
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "TimeoutError" {
		t.Fatalf("sleep budget error: %v", err)
	}
}

func TestTotalSleepBudgetAcrossFeedsAndSnapshots(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	session := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTotalSleep: 3 * time.Millisecond}})
	run(t, session, "import time\ntime.sleep(0.001)")
	p, err := session.FeedStart(ctx, "time.sleep(0.001)")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := p.(monty.Snapshot).Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	snapshot, err := restored.LoadSnapshot(ctx, blob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ResumeAuto(ctx); err != nil {
		t.Fatal(err)
	}
	run(t, restored, "time.sleep(0.001)")
	blob, err = restored.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idle := integrationSession(t, pool)
	if err := idle.LoadSession(ctx, blob); err != nil {
		t.Fatal(err)
	}
	_, err = idle.FeedRun(ctx, "try:\n    time.sleep(0.001)\nexcept TimeoutError:\n    pass")
	assertSleepBudgetError(t, err)
	if got := run(t, idle, "42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestSystemSleepCapsAndAsyncBudget(t *testing.T) {
	pool := integrationPool(t)
	zero := time.Duration(0)
	noWait := integrationSession(t, pool, monty.CheckoutOptions{OSPolicy: monty.OSPolicy{SleepSystemMax: &zero}, Limits: monty.ResourceLimits{MaxTotalSleep: time.Millisecond}})
	run(t, noWait, "import time, asyncio\ntime.sleep(100)\nawait asyncio.sleep(100)")
	limited := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTotalSleep: 3 * time.Millisecond}})
	_, err := limited.FeedRun(context.Background(), "import asyncio\nawait asyncio.gather(asyncio.sleep(0.002), asyncio.sleep(0.002))")
	assertSleepBudgetError(t, err)
	// An explicitly positive budget below the wire's microsecond resolution
	// becomes zero, rather than accidentally becoming unlimited.
	tiny := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTotalSleep: time.Nanosecond}})
	_, err = tiny.FeedRun(context.Background(), "import time\ntime.sleep(0.001)")
	assertSleepBudgetError(t, err)
}

func TestRestoredSleepBudgetKeepsDestinationCeiling(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	original := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTotalSleep: 5 * time.Millisecond}})
	p, err := original.FeedStart(ctx, "import time\ntime.sleep(0.002)")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := p.(monty.Snapshot).Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTotalSleep: time.Millisecond}})
	_, err = restored.LoadSnapshot(ctx, blob)
	assertSleepBudgetError(t, err)
	if got := run(t, restored, "42"); got != int64(42) {
		t.Fatal(got)
	}
}
