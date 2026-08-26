package monty_test

import (
	"context"
	"errors"
	"math/big"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func integrationPool(t *testing.T, options ...monty.Options) *monty.Monty {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	pool, err := monty.New(ctx, options...)
	if err != nil {
		if os.Getenv("MONTY_BIN") == "" {
			t.Skipf("Monty integration binary unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func integrationSession(t *testing.T, pool *monty.Monty, options ...monty.CheckoutOptions) *monty.Session {
	t.Helper()
	session, err := pool.Checkout(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func run(t *testing.T, session *monty.Session, code string, options ...monty.FeedOptions) monty.Value {
	t.Helper()
	value, err := session.FeedRun(context.Background(), code, options...)
	if err != nil {
		t.Fatalf("run %q: %v", code, err)
	}
	return value
}

func TestIntegrationBasicExecutionAndPersistentState(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	if got := run(t, session, "1 + 2"); got != int64(3) {
		t.Fatalf("got %#v", got)
	}
	_ = run(t, session, "x = 21")
	if got := run(t, session, "x * 2"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
	if got := run(t, session, "x + y", monty.FeedOptions{Inputs: map[string]any{"x": 10, "y": 5}}); got != int64(15) {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationMontyLanguageAndStdlibInterfaces(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	cases := []struct {
		name, code string
		want       any
	}{
		{"function recursion", "def fib(n):\n    return n if n < 2 else fib(n-1) + fib(n-2)\nfib(10)", int64(55)},
		{"comprehensions", "{str(i): i*i for i in range(4) if i % 2 == 0}", monty.Dict{{Key: "0", Value: int64(0)}, {Key: "2", Value: int64(4)}}},
		{"json", "import json\njson.loads('{\"x\": [1, true, null]}')", monty.Dict{{Key: "x", Value: monty.List{int64(1), true, nil}}}},
		{"regex", "import re\nre.findall(r'\\d+', 'a12b34')", monty.List{"12", "34"}},
		{"datetime", "from datetime import date, timedelta\ndate(2026, 8, 24) + timedelta(days=2)", monty.Date{Year: 2026, Month: 8, Day: 26}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, session, tc.code); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
	cycle := run(t, session, "x=[]\nx.append(x)\nx").(monty.List)
	if len(cycle) != 1 {
		t.Fatalf("cycle got %#v", cycle)
	}
	if _, ok := cycle[0].(monty.Cycle); !ok {
		t.Fatalf("cycle marker got %T", cycle[0])
	}
}

func TestIntegrationInputAndOutputValues(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	large := new(big.Int).Lsh(big.NewInt(1), 100)
	name := "UTC+01"
	offset := 3600
	cases := []struct {
		name        string
		input, want any
	}{
		{"none", nil, nil}, {"bool", true, true}, {"negative int", int64(-42), int64(-42)},
		{"large uint", uint64(^uint64(0)), new(big.Int).SetUint64(^uint64(0))}, {"bigint", large, large},
		{"float", 1.25, 1.25}, {"string", "héllo", "héllo"}, {"bytes", []byte{0, 1, 255}, []byte{0, 1, 255}},
		{"list", monty.List{int64(1), "x"}, monty.List{int64(1), "x"}},
		{"tuple", monty.Tuple{int64(1), "x"}, monty.Tuple{int64(1), "x"}},
		{"dict", monty.Dict{{Key: "x", Value: int64(1)}, {Key: int64(2), Value: "y"}}, monty.Dict{{Key: "x", Value: int64(1)}, {Key: int64(2), Value: "y"}}},
		{"set", monty.Set{int64(1), int64(2)}, monty.Set{int64(1), int64(2)}},
		{"frozenset", monty.FrozenSet{"a", "b"}, monty.FrozenSet{"a", "b"}},
		{"date", monty.Date{Year: 2026, Month: 8, Day: 24}, monty.Date{Year: 2026, Month: 8, Day: 24}},
		{"datetime", monty.DateTime{Year: 2026, Month: 8, Day: 24, Hour: 12, Minute: 30, Microsecond: 12, OffsetSeconds: &offset, TimezoneName: &name}, monty.DateTime{Year: 2026, Month: 8, Day: 24, Hour: 12, Minute: 30, Microsecond: 12, OffsetSeconds: &offset, TimezoneName: &name}},
		{"timedelta", monty.TimeDelta{Days: -2, Seconds: 30, Microseconds: 7}, monty.TimeDelta{Days: -2, Seconds: 30, Microseconds: 7}},
		{"timezone", monty.TimeZone{OffsetSeconds: offset, Name: &name}, monty.TimeZone{OffsetSeconds: offset, Name: &name}},
		{"path", monty.Path("/virtual/file"), monty.Path("/virtual/file")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, session, "value", monty.FeedOptions{Inputs: map[string]any{"value": tc.input}})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v (%T), want %#v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestIntegrationExternalFunctionsValuesKwargsAndErrors(t *testing.T) {
	type person struct {
		Name string `monty:"name"`
	}
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	lookup := monty.ExternalLookup{
		"greeting": "hello ",
		"add": func(ctx context.Context, a, b int) (int, error) {
			if ctx == nil {
				t.Fatal("nil context")
			}
			return a + b, nil
		},
		"describe": func(p person, kw monty.Kwargs) map[string]any {
			return map[string]any{"text": p.Name, "times": kw["times"]}
		},
		"fail": func() error { return &monty.HostError{Type: "ValueError", Message: "bad host input"} },
	}
	if got := run(t, session, "greeting + name", monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}, ExternalLookup: lookup}); got != "hello Ada" {
		t.Fatalf("got %#v", got)
	}
	if got := run(t, session, "add(20, 22)", monty.FeedOptions{ExternalLookup: lookup}); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
	got := run(t, session, "describe({'name': 'Ada'}, times=3)", monty.FeedOptions{ExternalLookup: lookup})
	want := monty.Dict{{Key: "text", Value: "Ada"}, {Key: "times", Value: int64(3)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if got := run(t, session, "\ntry:\n    fail()\nexcept ValueError as e:\n    result = str(e)\nresult", monty.FeedOptions{ExternalLookup: lookup}); got != "bad host input" {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationAsyncExternalFunctionAndSingleUseSnapshots(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	p, err := session.FeedStart(context.Background(), "await slow(21)", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"slow": monty.Async(func(v int) int { return v * 2 }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("got %T", p)
	}
	p, err = call.ResumeAuto(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future, ok := p.(*monty.FutureSnapshot)
	if !ok {
		t.Fatalf("got %T", p)
	}
	if _, err := call.Resume(context.Background(), int64(0)); err == nil || !strings.Contains(err.Error(), "already been resumed") {
		t.Fatalf("stale snapshot returned %v", err)
	}
	p, err = future.ResumeAuto(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done, ok := p.(*monty.Complete)
	if !ok || done.Output != int64(42) {
		t.Fatalf("got %T %#v", p, p)
	}
}

func TestIntegrationPrintAndErrorsLeaveSessionUsable(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	var mu sync.Mutex
	var lines []string
	_, err := session.FeedRun(context.Background(), "print('out')", monty.FeedOptions{PrintCallback: func(stream monty.PrintStream, text string) error {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, stream.String()+":"+text)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"stdout:out\n"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %#v", lines)
	}
	_, err = session.FeedRun(context.Background(), "1 / 0")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "ZeroDivisionError" {
		t.Fatalf("got %T %v", err, err)
	}
	if !strings.Contains(runtimeErr.Exception.Traceback(), "ZeroDivisionError") {
		t.Fatalf("traceback: %s", runtimeErr.Exception.Traceback())
	}
	if got := run(t, session, "6 * 7"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
	_, err = session.FeedRun(context.Background(), "def broken(")
	var syntax *monty.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("got %T %v", err, err)
	}
	collector, err := monty.NewCollectString()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.FeedRun(context.Background(), "print('collected')", monty.FeedOptions{PrintCallback: collector.Callback()}); err != nil {
		t.Fatal(err)
	}
	if collector.Output() != "collected\n" {
		t.Fatalf("got %q", collector.Output())
	}
	tiny, _ := monty.NewCollectString(2)
	if _, err = session.FeedRun(context.Background(), "print('too long')", monty.FeedOptions{PrintCallback: tiny.Callback()}); err == nil {
		t.Fatal("expected collector limit")
	}
	if got := run(t, session, "7 * 6"); got != int64(42) {
		t.Fatalf("session lost after callback failure: %#v", got)
	}
	var reentrant error
	_, err = session.FeedRun(context.Background(), "print('callback')", monty.FeedOptions{PrintCallback: func(monty.PrintStream, string) error {
		_, reentrant = session.FeedRun(context.Background(), "1 + 1")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reentrant == nil || !strings.Contains(reentrant.Error(), "active protocol turn") {
		t.Fatalf("reentrant call got %v", reentrant)
	}
}

func TestIntegrationOSCallbacks(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	handler := func(_ context.Context, name string, args []any, _ monty.Kwargs) (any, error) {
		switch name {
		case "os.getenv":
			if args[0] == "HOME" {
				return "/home/monty", nil
			}
			return monty.NotHandled, nil
		case "open":
			h, err := monty.NewFileHandle(args[0].(string), args[1].(string), 0)
			return h, err
		case "Path.read_text":
			return "host file contents", nil
		default:
			return monty.NotHandled, nil
		}
	}
	if got := run(t, session, "import os\nos.getenv('HOME')", monty.FeedOptions{OS: handler}); got != "/home/monty" {
		t.Fatalf("got %#v", got)
	}
	if got := run(t, session, "open('/data/a.txt').read()", monty.FeedOptions{OS: handler}); got != "host file contents" {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationFilesystemDeniedWithoutOSHandler(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, code := range []string{
		"open('/etc/passwd').read()",
		"from pathlib import Path\nPath('/etc/passwd').read_text()",
	} {
		_, err := session.FeedRun(context.Background(), code)
		var runtimeErr *monty.RuntimeError
		if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "PermissionError" {
			t.Fatalf("default filesystem access got %T %v", err, err)
		}
	}
}

func TestIntegrationPoolCapacityTimeoutAndRecovery(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1, CheckoutTimeout: 30 * time.Millisecond})
	first := integrationSession(t, pool)
	if first.WorkerPID() == 0 {
		t.Fatal("missing worker PID")
	}
	_, err := pool.Checkout(context.Background())
	if err == nil {
		t.Fatal("expected checkout timeout")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := integrationSession(t, pool)
	if got := run(t, second, "40 + 2"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationRequestTimeoutKillsOnlyWorkerAndPoolRecovers(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1, RequestTimeout: 40 * time.Millisecond})
	session := integrationSession(t, pool)
	_, err := session.FeedRun(context.Background(), "while True:\n    pass")
	var crashed *monty.CrashedError
	if !errors.As(err, &crashed) || !crashed.TimedOut {
		t.Fatalf("got %T %v", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout cause was not preserved: %T %v", err, err)
	}
	_ = session.Close()
	replacement := integrationSession(t, pool)
	if got := run(t, replacement, "21 * 2"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationCallerCancellationIsPreserved(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
	session := integrationSession(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	_, err := session.FeedRun(ctx, "while True:\n    pass")
	var crashed *monty.CrashedError
	if !errors.As(err, &crashed) {
		t.Fatalf("got %T %v", err, err)
	}
	if crashed.TimedOut {
		t.Fatalf("explicit cancellation reported as timeout: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause was not preserved: %T %v", err, err)
	}
	_ = session.Close()
	replacement := integrationSession(t, pool)
	if got := run(t, replacement, "6 * 7"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationSnapshotsAndDumps(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	p, err := session.FeedStart(context.Background(), "greet(name) + '!'", monty.FeedOptions{Inputs: map[string]any{"name": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok || call.FunctionName != "greet" || !reflect.DeepEqual(call.Args, []any{"Ada"}) {
		t.Fatalf("got %T %#v", p, p)
	}
	dump, err := call.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p, err = call.Resume(context.Background(), "hello Ada"); err != nil {
		t.Fatal(err)
	}
	if done, ok := p.(*monty.Complete); !ok || done.Output != "hello Ada!" {
		t.Fatalf("got %T %#v", p, p)
	}

	restored := integrationSession(t, pool)
	snap, err := restored.LoadSnapshot(context.Background(), dump)
	if err != nil {
		t.Fatal(err)
	}
	restoredCall, ok := snap.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("got %T", snap)
	}
	p, err = restoredCall.Resume(context.Background(), "restored")
	if err != nil {
		t.Fatal(err)
	}
	if done := p.(*monty.Complete); done.Output != "restored!" {
		t.Fatalf("got %#v", done.Output)
	}

	_ = run(t, session, "saved = 41")
	idleDump, err := session.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idle := integrationSession(t, pool)
	if err := idle.LoadSession(context.Background(), idleDump); err != nil {
		t.Fatal(err)
	}
	if got := run(t, idle, "saved + 1"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestIntegrationManualSnapshotVariants(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	p, err := session.FeedStart(context.Background(), "missing_value")
	if err != nil {
		t.Fatal(err)
	}
	lookup, ok := p.(*monty.NameLookupSnapshot)
	if !ok || lookup.Name != "missing_value" {
		t.Fatalf("got %T %#v", p, p)
	}
	if _, err = lookup.Dump(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = lookup.ResumeUndefined(context.Background())
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "NameError" {
		t.Fatalf("got %T %v", err, err)
	}

	p, err = session.FeedStart(context.Background(), "missing_func()")
	if err != nil {
		t.Fatal(err)
	}
	call := p.(*monty.FunctionSnapshot)
	_, err = call.ResumeNotFound(context.Background())
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "NameError" {
		t.Fatalf("got %T %v", err, err)
	}

	p, err = session.FeedStart(context.Background(), "await later()")
	if err != nil {
		t.Fatal(err)
	}
	call = p.(*monty.FunctionSnapshot)
	p, err = call.ResumeFuture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future := p.(*monty.FutureSnapshot)
	if _, err = future.Dump(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err = future.Resume(context.Background(), map[uint32]any{call.CallID: "future value"})
	if err != nil {
		t.Fatal(err)
	}
	if done := p.(*monty.Complete); done.Output != "future value" {
		t.Fatalf("got %#v", done.Output)
	}

	p, err = session.FeedStart(context.Background(), "import os\nos.getenv('SECRET')")
	if err != nil {
		t.Fatal(err)
	}
	osCall := p.(*monty.FunctionSnapshot)
	if !osCall.IsOSFunction {
		t.Fatalf("not an OS snapshot: %#v", osCall)
	}
	_, err = osCall.ResumeNotHandled(context.Background())
	if err == nil {
		t.Fatal("expected default OS denial")
	}
}

func TestIntegrationRestoreConcurrentFuturesAcrossPools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary, err := monty.Install(ctx)
	if err != nil {
		t.Skipf("pinned Monty integration binary unavailable: %v", err)
	}

	pool, err := monty.New(ctx, monty.Options{BinaryPath: binary})
	if err != nil {
		t.Fatal(err)
	}
	session, err := pool.Checkout(ctx)
	if err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	defer func(sourceSession *monty.Session, sourcePool *monty.Monty) {
		_ = sourceSession.Close()
		_ = sourcePool.Close()
	}(session, pool)

	code := `
import asyncio

try:
    result = await asyncio.gather(succeed(), fail())
except ValueError as e:
    result = 'caught: ' + str(e)
result
`
	p, err := session.FeedStart(ctx, code)
	if err != nil {
		_ = session.Close()
		_ = pool.Close()
		t.Fatal(err)
	}
	first, ok := p.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("first suspension = %T, want *monty.FunctionSnapshot", p)
	}
	p, err = first.ResumeFuture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := p.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("second suspension = %T, want *monty.FunctionSnapshot", p)
	}
	p, err = second.ResumeFuture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	futures, ok := p.(*monty.FutureSnapshot)
	if !ok {
		t.Fatalf("future suspension = %T, want *monty.FutureSnapshot", p)
	}
	dump, err := futures.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pool.Close(); err != nil {
		t.Fatal(err)
	}

	pool, err = monty.New(ctx, monty.Options{BinaryPath: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	session, err = pool.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	restored, err := session.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	restoredFutures, ok := restored.(*monty.FutureSnapshot)
	if !ok {
		t.Fatalf("restored suspension = %T, want *monty.FutureSnapshot", restored)
	}

	callIDs := map[string]uint32{
		first.FunctionName:  first.CallID,
		second.FunctionName: second.CallID,
	}
	if _, ok = callIDs["succeed"]; !ok {
		t.Fatalf("calls = %q and %q, missing succeed", first.FunctionName, second.FunctionName)
	}
	if _, ok = callIDs["fail"]; !ok {
		t.Fatalf("calls = %q and %q, missing fail", first.FunctionName, second.FunctionName)
	}
	results := map[uint32]any{
		callIDs["succeed"]: "success",
		callIDs["fail"]:    &monty.HostError{Type: "ValueError", Message: "restored failure"},
	}
	p, err = restoredFutures.Resume(ctx, results)
	if err != nil {
		t.Fatal(err)
	}
	done, ok := p.(*monty.Complete)
	want := "caught: restored failure"
	if !ok || done.Output != want {
		t.Fatalf("restored result = %T %#v, want %#v", p, p, want)
	}
}

func TestIntegrationDependencyRequestAndFreshLoadRules(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	err := session.InstallDependencies(context.Background(), "example-package")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("got %T %v", err, err)
	}
	if got := run(t, session, "1 + 1"); got != int64(2) {
		t.Fatalf("got %#v", got)
	}
	dump, err := session.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = session.LoadSession(context.Background(), dump); err == nil || !strings.Contains(err.Error(), "fresh session") {
		t.Fatalf("got %v", err)
	}
}

func TestIntegrationTypeCheckingAndResourceLimits(t *testing.T) {
	pool := integrationPool(t)
	typed := integrationSession(t, pool, monty.CheckoutOptions{TypeCheck: true, TypeCheckStubs: "def double(x: int) -> int: ...", TypeCheckFormat: monty.TypeCheckConcise})
	_, err := typed.FeedRun(context.Background(), "double('bad')")
	var typing *monty.TypingError
	if !errors.As(err, &typing) || typing.Diagnostics == "" {
		t.Fatalf("got %T %v", err, err)
	}
	if got := run(t, typed, "x: int = 'bad'\nx", monty.FeedOptions{SkipTypeCheck: true}); got != "bad" {
		t.Fatalf("skip type check got %#v", got)
	}
	plainAssert := integrationSession(t, pool, monty.CheckoutOptions{AssertMessageAnnotations: false})
	_, err = plainAssert.FeedRun(context.Background(), "assert 2 == 5")
	var assertErr *monty.RuntimeError
	if !errors.As(err, &assertErr) || assertErr.Exception.Type != "AssertionError" || assertErr.Exception.Message != "" {
		t.Fatalf("got %T %#v", err, assertErr)
	}
	limited := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxRecursionDepth: 20}})
	_, err = limited.FeedRun(context.Background(), "def recurse(): return recurse()\nrecurse()")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("got %T %v", err, err)
	}
	duration := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxDuration: time.Millisecond}})
	_, err = duration.FeedRun(context.Background(), "while True:\n    pass")
	var montyErr monty.Error
	if !errors.As(err, &montyErr) {
		t.Fatalf("duration limit got %T %v", err, err)
	}
}
