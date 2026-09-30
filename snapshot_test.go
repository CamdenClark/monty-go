package monty

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSnapshotMetadataRoundTrip(t *testing.T) {
	want := snapshotMetadata{duration: time.Second, suspensions: 42}
	state := []byte("opaque worker state")
	got, meta, err := decodeSnapshot(encodeSnapshot(state, want))
	if err != nil || meta != want || !bytes.Equal(got, state) {
		t.Fatalf("%q %#v %v", got, meta, err)
	}
}
func TestSnapshotRejectsMalformedMetadata(t *testing.T) {
	for name, data := range map[string][]byte{
		"raw":           []byte("worker dump"),
		"truncated":     []byte(snapshotMagic + "\x0a\x05x"),
		"missing state": encodeSnapshot(nil, snapshotMetadata{}),
		"wrong runtime": append(append([]byte(snapshotMagic), fieldBytes(1, []byte("state"))...), fieldString(2, "0.0.21")...),
		"overflow":      append(append(append([]byte(snapshotMagic), fieldBytes(1, []byte("state"))...), fieldString(2, RuntimeVersion)...), fieldVarint(3, ^uint64(0))...),
		"duplicate":     append(encodeSnapshot([]byte("state"), snapshotMetadata{}), fieldVarint(4, 1)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeSnapshot(data); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestSnapshotRestoresCumulativeBudget(t *testing.T) {
	state, meta, err := decodeSnapshot(encodeSnapshot([]byte("state"), snapshotMetadata{duration: 2 * time.Second}))
	if err != nil || len(state) == 0 {
		t.Fatal(err)
	}
	session := Session{maxDuration: meta.duration, durationBackstop: true}
	session.observeEvent(childEvent{totalExecutionMicros: 2000001})
	// An already exhausted restored budget must fail before contacting a worker.
	_, err = session.executionExchange(context.Background(), request{kind: reqFeed}, nil)
	var crashed *CrashedError
	if !errors.As(err, &crashed) || !crashed.TimedOut {
		t.Fatalf("restored budget reset: %v", err)
	}
}
