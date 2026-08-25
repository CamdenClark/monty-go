package monty_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestSnapshotResumeErrorIsCatchableInPython(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	p, err := session.FeedStart(context.Background(), "\ntry:\n    host_call()\nexcept ValueError as e:\n    result = str(e)\nresult")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("got %T", p)
	}
	p, err = call.ResumeError(context.Background(), &monty.HostError{Type: "ValueError", Message: "manual failure"})
	if err != nil {
		t.Fatal(err)
	}
	done, ok := p.(*monty.Complete)
	if !ok || done.Output != "manual failure" {
		t.Fatalf("got %T %#v", p, p)
	}
}

func TestSnapshotDumpIsRepeatableBeforeResume(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	p, err := session.FeedStart(context.Background(), "host_call()")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := p.(*monty.FunctionSnapshot)
	first, err := snapshot.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("dumps differ: %d and %d bytes", len(first), len(second))
	}
	if _, err = snapshot.Resume(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestIdleDumpCreatesIndependentREPLBranches(t *testing.T) {
	pool := integrationPool(t)
	base := integrationSession(t, pool)
	run(t, base, "value = 40")
	dump, err := base.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	left := integrationSession(t, pool)
	right := integrationSession(t, pool)
	if err = left.LoadSession(context.Background(), dump); err != nil {
		t.Fatal(err)
	}
	if err = right.LoadSession(context.Background(), dump); err != nil {
		t.Fatal(err)
	}
	run(t, left, "value += 1")
	run(t, right, "value += 2")
	if got := run(t, left, "value"); got != int64(41) {
		t.Fatalf("left %#v", got)
	}
	if got := run(t, right, "value"); got != int64(42) {
		t.Fatalf("right %#v", got)
	}
	if got := run(t, base, "value"); got != int64(40) {
		t.Fatalf("base %#v", got)
	}
}

func TestLoadSnapshotRejectsUsedSession(t *testing.T) {
	pool := integrationPool(t)
	source := integrationSession(t, pool)
	p, err := source.FeedStart(context.Background(), "host_call()")
	if err != nil {
		t.Fatal(err)
	}
	dump, err := p.(monty.Snapshot).Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	used := integrationSession(t, pool)
	run(t, used, "1 + 1")
	_, err = used.LoadSnapshot(context.Background(), dump)
	if err == nil || !strings.Contains(err.Error(), "fresh session") {
		t.Fatalf("got %v", err)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	pool := integrationPool(t)
	session, err := pool.Checkout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPoolCloseRejectsNewCheckout(t *testing.T) {
	pool := integrationPool(t)
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Checkout(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("got %v", err)
	}
}

func TestPoolCloseUnblocksPendingCheckout(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
	held := integrationSession(t, pool)
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := pool.Checkout(context.Background())
		result <- err
	}()
	<-started
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("got %T %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending checkout did not unblock when the pool closed")
	}
	_ = held
}

func TestPoolCloseKeepsCheckedOutSessionUsable(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if got := run(t, session, "21 * 2"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestPoolReusesWorkerProcess(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
	first, err := pool.Checkout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstPID := first.WorkerPID()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second := integrationSession(t, pool)
	if second.WorkerPID() != firstPID {
		t.Fatalf("worker changed from %d to %d", firstPID, second.WorkerPID())
	}
}

func TestPoolRecyclesWorkerAtCheckoutLimit(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1, MaxCheckoutsPerWorker: 1})
	first, err := pool.Checkout(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstPID := first.WorkerPID()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second := integrationSession(t, pool)
	if second.WorkerPID() == firstPID {
		t.Fatalf("worker %d was not recycled", firstPID)
	}
}

func TestCheckoutHonorsCanceledContext(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1})
	held := integrationSession(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pool.Checkout(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %T %v", err, err)
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Checkout(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled checkout with an available worker got %T %v", err, err)
	}
	healthy := integrationSession(t, pool)
	if got := run(t, healthy, "6 * 7"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestMountOverlayCreatedFileDoesNotPersist(t *testing.T) {
	host := t.TempDir()
	mount, err := monty.NewMountDir(monty.MountOptions{HostPath: host, VirtualPath: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	session := integrationSession(t, integrationPool(t))
	code := "from pathlib import Path\np=Path('/data/new.txt')\np.write_text('temporary')\n(p.exists(), p.read_text())"
	want := monty.Tuple{true, "temporary"}
	if got := run(t, session, code, monty.FeedOptions{Mount: mount}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if _, err = os.Stat(host + "/new.txt"); !os.IsNotExist(err) {
		t.Fatalf("overlay file persisted: %v", err)
	}
}

func TestMountTraversalFallsThroughToDefaultDenial(t *testing.T) {
	host := t.TempDir()
	mount, err := monty.NewMountDir(monty.MountOptions{HostPath: host, VirtualPath: "/data", Mode: monty.MountReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	session := integrationSession(t, integrationPool(t))
	_, err = session.FeedRun(context.Background(), "open('/data/../secret.txt').read()", monty.FeedOptions{Mount: mount})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "PermissionError" {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestTypeCheckJSONFormatIsValidJSON(t *testing.T) {
	session := integrationSession(t, integrationPool(t), monty.CheckoutOptions{TypeCheck: true, TypeCheckFormat: monty.TypeCheckJSON})
	_, err := session.FeedRun(context.Background(), "value: int = 'wrong'")
	var typing *monty.TypingError
	if !errors.As(err, &typing) {
		t.Fatalf("got %T %v", err, err)
	}
	if !json.Valid([]byte(typing.Diagnostics)) {
		t.Fatalf("invalid JSON diagnostics: %s", typing.Diagnostics)
	}
}

func TestDefaultAssertMessageIsAnnotated(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	_, err := session.FeedRun(context.Background(), "assert 2 == 5")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "AssertionError" || !strings.Contains(runtimeErr.Exception.Message, "2 == 5") {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestCollectorUnlimitedModeAcceptsLargeOutput(t *testing.T) {
	collector, err := monty.NewCollectString(-1)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", monty.DefaultMaxPrintCollectBytes+1)
	if err = collector.Write(monty.Stdout, text); err != nil {
		t.Fatal(err)
	}
	if len(collector.Output()) != len(text) {
		t.Fatalf("got %d bytes", len(collector.Output()))
	}
}

func TestDurationLimitLeavesPoolAvailable(t *testing.T) {
	pool := integrationPool(t, monty.Options{MinProcesses: 1, MaxProcesses: 1, DurationLimitGrace: 10 * time.Millisecond})
	limited := integrationSession(t, pool, monty.CheckoutOptions{Limits: monty.ResourceLimits{MaxDuration: time.Millisecond}})
	_, err := limited.FeedRun(context.Background(), "while True:\n    pass")
	var montyErr monty.Error
	if !errors.As(err, &montyErr) {
		t.Fatalf("got %T %v", err, err)
	}
	_ = limited.Close()
	replacement := integrationSession(t, pool)
	if got := run(t, replacement, "6 * 7"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}
