package monty_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestSnapshotRestorePreservesSuspensionCount(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxSuspensions: 3}})
	ctx := context.Background()
	p, err := session.FeedStart(ctx, "first + second + third + fourth")
	if err != nil {
		t.Fatal(err)
	}
	// Restore at every name lookup. Each load re-emits the same suspension,
	// which must not be counted twice or reset the feed's existing count.
	for i := 0; i < 3; i++ {
		dump, err := p.(monty.Snapshot).Dump(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		restored := integrationSession(t, pool)
		session = restored
		snap, err := restored.LoadSnapshot(ctx, dump)
		if err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
		p, err = snap.(*monty.NameLookupSnapshot).Resume(ctx, 1)
		if i < 2 && err != nil {
			t.Fatalf("resume %d: %v", i, err)
		}
		if i == 2 {
			var runtimeErr *monty.RuntimeError
			if !errors.As(err, &runtimeErr) || !strings.Contains(err.Error(), "suspension count") {
				t.Fatalf("budget reset: %v", err)
			}
			if got := run(t, restored, "42"); got != int64(42) {
				t.Fatal(got)
			}
		}
	}
}

func TestPerFeedDurationResetsAndExcludesCallbacks(t *testing.T) {
	session := integrationSession(t, integrationPool(t), monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxFeedDuration: 20 * time.Millisecond}})
	for i := 0; i < 3; i++ {
		if got := run(t, session, "host_wait() + 1", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": func() int { time.Sleep(60 * time.Millisecond); return 41 }}}); got != int64(42) {
			t.Fatal(got)
		}
	}
	_, err := session.FeedRun(context.Background(), "host_wait()\nwhile True: pass", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": func() int { return 1 }}})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("feed limit: %v", err)
	}
	if got := run(t, session, "42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestPerTurnDurationResetsAndExcludesCallbacks(t *testing.T) {
	session := integrationSession(t, integrationPool(t), monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTurnDuration: 20 * time.Millisecond}})
	for i := 0; i < 3; i++ {
		if got := run(t, session, "host_wait() + host_wait()", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": func() int { time.Sleep(60 * time.Millisecond); return 21 }}}); got != int64(42) {
			t.Fatal(got)
		}
	}
	_, err := session.FeedRun(context.Background(), "host_wait()\nwhile True: pass", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": func() int { return 1 }}})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("turn limit: %v", err)
	}
	if got := run(t, session, "42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestCancellationDuringSystemSleep(t *testing.T) {
	for _, code := range []string{"import time\ntime.sleep(10)", "import asyncio\nawait asyncio.sleep(10)"} {
		t.Run(code, func(t *testing.T) {
			pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
			session := integrationSession(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err := session.FeedRun(ctx, code)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation cause: %v", err)
			}
			if err := session.Close(); err != nil {
				var crashed *monty.CrashedError
				if !errors.As(err, &crashed) {
					t.Fatal(err)
				}
			}
			fresh := integrationSession(t, pool)
			if got := run(t, fresh, "42"); got != int64(42) {
				t.Fatal(got)
			}
		})
	}
}

func TestCancellationDuringCooperativeHostCallback(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
			session := integrationSession(t, pool)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, stopped := make(chan struct{}), make(chan struct{})
			callback := func(ctx context.Context) (int, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return 0, ctx.Err()
			}
			var fn any = callback
			code := "host_wait()"
			if async {
				fn = monty.Async(callback)
				code = "await host_wait()"
			}
			done := make(chan error, 1)
			go func() {
				_, err := session.FeedRun(ctx, code, monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": fn}})
				done <- err
			}()
			awaitSignal(t, started)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("feed did not cancel")
			}
			awaitSignal(t, stopped)
			_ = session.Close()
			if got := run(t, integrationSession(t, pool), "42"); got != int64(42) {
				t.Fatal(got)
			}
		})
	}
}

func TestSessionCloseCancelsPendingAsyncCallback(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
	session := integrationSession(t, pool)
	started, stopped := make(chan struct{}), make(chan struct{})
	ctx := context.Background()
	p, err := session.FeedStart(ctx, "await host_wait()", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_wait": monty.Async(func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return 0, ctx.Err()
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.(monty.Snapshot).ResumeAuto(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, started)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, stopped)
	if got := run(t, integrationSession(t, pool), "42"); got != int64(42) {
		t.Fatal(got)
	}
}

func TestFuturesResumeInCompletionOrder(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	slowStarted, slowStopped, releaseSlow := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := session.FeedRun(ctx, `import asyncio
async def slower():
    return await slow()
async def faster():
    value = await fast()
    release_slow()
    return value
results = await asyncio.gather(slower(), faster())
results[0] + results[1]`, monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"slow": monty.Async(func(ctx context.Context) (int, error) {
			close(slowStarted)
			defer close(slowStopped)
			select {
			case <-releaseSlow:
				return 41, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}),
		"fast": monty.Async(func(ctx context.Context) (int, error) {
			select {
			case <-slowStarted:
				return 42, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}),
		"release_slow": func() { close(releaseSlow) },
	}})
	if err != nil || !reflect.DeepEqual(got, int64(83)) {
		t.Fatalf("blocked behind slow future: %#v %v", got, err)
	}
	awaitSignal(t, slowStopped)
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not reach expected state")
	}
}

func TestFeedBudgetAccumulatesAcrossTurns(t *testing.T) {
	// Every chunk is small, but the entire feed exceeds the feed budget.
	// With only a turn budget, each callback resume starts a new allowance.
	code := `total = 0
for chunk in range(500):
    for i in range(50000):
        total += i
    checkpoint()
total`
	options := monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"checkpoint": func() {}}}
	pool := integrationPool(t)
	turnLimited := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxTurnDuration: 50 * time.Millisecond}})
	if _, err := turnLimited.FeedRun(context.Background(), code, options); err != nil {
		t.Fatalf("turn budget did not reset: %v", err)
	}
	feedLimited := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxFeedDuration: 50 * time.Millisecond}})
	_, err := feedLimited.FeedRun(context.Background(), code, options)
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("feed budget did not accumulate: %v", err)
	}
}

func TestFeedCompletionCancelsRemainingCallbacks(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	started, stopped := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := session.FeedRun(ctx, `import asyncio
try:
    await asyncio.gather(slow(), fail())
except ValueError:
    result = 42
result`, monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"slow": monty.Async(func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return 0, ctx.Err()
		}),
		"fail": monty.Async(func(ctx context.Context) error {
			select {
			case <-started:
				return &monty.HostError{Type: "ValueError", Message: "failed"}
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	}})
	if err != nil || got != int64(42) {
		t.Fatalf("feed completion: %#v %v", got, err)
	}
	awaitSignal(t, stopped)
	if got := run(t, session, "42"); got != int64(42) {
		t.Fatal(got)
	}
}
