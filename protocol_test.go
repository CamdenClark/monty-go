package monty

import (
	"context"
	"reflect"
	"testing"
)

func TestConfigureRequestMetadata(t *testing.T) {
	request := configureRequest(configureWire{})
	if got := decodeStringField(request.body, 5); got != RuntimeVersion {
		t.Fatalf("monty_version = %q, want runtime version %q", got, RuntimeVersion)
	}
	var version uint64
	var flush *uint64
	if err := parseFields(request.body, func(f wireField) error {
		if f.tag == 9 {
			version = f.varint
		}
		if f.tag == 10 {
			v := f.varint
			flush = &v
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if version != protocolVersion || flush == nil || *flush != 0 {
		t.Fatalf("configuration: protocol %d, flush %v", version, flush)
	}
}

func TestImportSearchPathAndTypeHintTargets(t *testing.T) {
	importArgs := append(fieldString(1, "pkg.child"), fieldString(2, "/first")...)
	importArgs = append(importArgs, fieldString(2, "/second")...)
	call, err := decodeOSCall(fieldMessage(31, importArgs))
	if err != nil || !reflect.DeepEqual(call.Args, []Value{"pkg.child", List{"/first", "/second"}}) {
		t.Fatalf("import arguments: %#v, %v", call, err)
	}
	id := [16]byte{1, 2, 3}
	for _, target := range []struct {
		wire []byte
		want Value
	}{
		{fieldString(1, "tools.work"), "tools.work"},
		{fieldMessage(2, fieldBytes(1, id[:])), id},
	} {
		call, err := decodeOSCall(fieldMessage(34, target.wire))
		if err != nil || call.Name != "typing.get_type_hints" || !reflect.DeepEqual(call.Args, []Value{target.want}) {
			t.Fatalf("type hint target: %#v, %v", call, err)
		}
	}
	for name, payload := range map[string][]byte{
		"missing":         nil,
		"invalid UUID":    fieldMessage(2, fieldBytes(1, []byte{1})),
		"multiple":        append(fieldString(1, "work"), fieldMessage(2, fieldBytes(1, id[:]))...),
		"wrong wire type": fieldVarint(1, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeOSCall(fieldMessage(34, payload)); err == nil {
				t.Fatal("malformed type hint target accepted")
			}
		})
	}
}

func TestErrorEventPreservesOutstandingSuspension(t *testing.T) {
	for _, kind := range []eventKind{eventError, eventTypingError} {
		for _, suspended := range []*bool{nil, boolPointer(true), boolPointer(false)} {
			session := &Session{}
			state := newRunState(session, FeedOptions{})
			session.pending = state
			body := fieldString(1, "type diagnostics")
			if kind == eventError {
				body = fieldMessage(1, encodeRaised(RaisedException{Type: "RuntimeError", Message: "rejected answer"}))
			}
			wire := fieldMessage(int(kind), body)
			if suspended != nil {
				wire = append(wire, fieldBool(29, *suspended)...)
			}
			event, err := decodeEvent(wire)
			if err != nil {
				t.Fatal(err)
			}
			_, err = state.progress(context.Background(), event)
			if err == nil {
				t.Fatal("error event did not report an error")
			}
			preserve := suspended == nil || *suspended
			if (session.pending == state) != preserve || (state.ctx.Err() == nil) != preserve {
				t.Fatalf("kind %d, suspended %v: pending=%v, context=%v", kind, suspended, session.pending != nil, state.ctx.Err())
			}
			state.cancel()
		}
	}
}

func boolPointer(value bool) *bool { return &value }
