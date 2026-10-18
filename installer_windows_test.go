package monty

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAcquireInstallLockPendingWindowsDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Hold a handle open while DeleteFile marks the lock for deletion. A
	// CREATE_NEW attempt must retry ERROR_ACCESS_DENIED until this closes.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if handle != syscall.InvalidHandle {
			_ = syscall.CloseHandle(handle)
		}
	})
	if err := syscall.DeleteFile(name); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() { release, err := acquireInstallLock(ctx, path); done <- result{release, err} }()
	select {
	case got := <-done:
		if got.release != nil {
			got.release()
		}
		t.Fatalf("returned while lock was pending deletion: %v", got.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = syscall.InvalidHandle
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release()
}
