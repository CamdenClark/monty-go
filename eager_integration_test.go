package monty_test

import (
	"context"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestEagerAwaitSnapshots(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	p, err := session.FeedStart(ctx, "await host_async()")
	if err != nil {
		t.Fatal(err)
	}
	call := p.(*monty.FunctionSnapshot)
	if !call.AllowEagerAwait {
		t.Fatal("immediate await did not allow eager resolution")
	}
	blob, err := call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	snap, err := restored.LoadSnapshot(ctx, blob)
	if err != nil {
		t.Fatal(err)
	}
	p, err = snap.(*monty.FunctionSnapshot).ResumeEager(ctx, 42)
	if err != nil || p.(*monty.Complete).Output != int64(42) {
		t.Fatalf("restored eager result: %#v, %v", p, err)
	}
	p, err = call.ResumeEager(ctx, 43)
	if err != nil || p.(*monty.Complete).Output != int64(43) {
		t.Fatalf("eager result: %#v, %v", p, err)
	}
	if _, err := call.ResumeEager(ctx, 44); err == nil {
		t.Fatal("eager snapshot resumed twice")
	}
	p, err = session.FeedStart(ctx, "host_sync()")
	if err != nil {
		t.Fatal(err)
	}
	call = p.(*monty.FunctionSnapshot)
	if call.AllowEagerAwait {
		t.Fatal("synchronous call allowed eager await")
	}
	if _, err := call.ResumeEager(ctx, 42); err == nil {
		t.Fatal("forbidden eager resume accepted")
	}
	if _, err := call.Resume(ctx, 42); err != nil {
		t.Fatal("invalid eager attempt consumed the snapshot:", err)
	}
}

func TestEagerAwaitErrorsAndConversion(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, callback := range []any{
		monty.Async(func() error { return &monty.HostError{Type: "ValueError", Message: "chosen"} }),
		monty.Async(func() any { return make(chan int) }),
	} {
		if got := run(t, session, "try:\n    await host_async()\nexcept (ValueError, RuntimeError):\n    result = 42\nresult", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"host_async": callback}}); got != int64(42) {
			t.Fatal(got)
		}
	}
	p, err := session.FeedStart(context.Background(), "try:\n    missing_name\nexcept ValueError:\n    result = 42\nresult")
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.(*monty.NameLookupSnapshot).ResumeError(context.Background(), &monty.HostError{Type: "ValueError", Message: "lookup failed"})
	if err != nil || p.(*monty.Complete).Output != int64(42) {
		t.Fatalf("lookup exception: %#v, %v", p, err)
	}
}
