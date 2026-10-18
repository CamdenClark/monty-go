package monty

import (
	"context"
	"reflect"
	"testing"
)

func TestExternalModuleValidationAndCopy(t *testing.T) {
	attrs := map[string]Value{"answer": 42, "_secret": 7, "func": true}
	m, err := NewExternalModule(attrs)
	if err != nil {
		t.Fatal(err)
	}
	attrs["answer"] = 0
	value, err := m.value("tools", 0)
	if err != nil || m.ID() == ([16]byte{}) || !reflect.DeepEqual(value.Attrs, Dict{{"answer", 42}, {"func", true}}) {
		t.Fatalf("module: %#v %v", value, err)
	}
	for _, attrs := range []map[string]Value{{"x.y": 1}, {"1name": 1}, {"class": 1}, {"": 1}, {"sub": (*ExternalModule)(nil)}} {
		if _, err := NewExternalModule(attrs); err == nil {
			t.Fatalf("accepted %#v", attrs)
		}
	}
	for _, modules := range []ExternalModules{{"bad.name": m}, {"class": m}, {"valid": nil}} {
		if _, err := (&Monty{}).Checkout(context.Background(), CheckoutOptions{ExternalModules: modules}); err == nil {
			t.Fatalf("accepted %#v", modules)
		}
	}
	if _, err := encodeValue(m); err == nil {
		t.Fatal("ExternalModule accepted as ordinary input")
	}
}

func TestModuleArenaValidation(t *testing.T) {
	id := fieldMessage(2, fieldBytes(1, make([]byte, 16)))
	attrs := fieldMessage(3, nil)
	valid := fieldMessage(32, append(append(fieldString(1, "tools"), id...), attrs...))
	for _, node := range [][]byte{
		fieldMessage(32, id), fieldMessage(32, attrs),
		fieldMessage(32, append(id, fieldMessage(3, fieldMessage(1, fieldVarint(1, 0)))...)),
		fieldMessage(32, append(append([]byte{}, id...), append(attrs, attrs...)...)),
		fieldMessage(32, append(id, fieldVarint(3, 0)...)),
		fieldMessage(32, append(fieldMessage(2, fieldBytes(1, []byte{1})), attrs...)),
	} {
		if _, err := decodeArena(fieldMessage(2, node)); err == nil {
			t.Fatalf("accepted malformed module %x", node)
		}
	}
	if _, err := decodeArena(fieldMessage(2, valid)); err != nil {
		t.Fatalf("empty module: %v", err)
	}
}

func TestNewOSCallValidation(t *testing.T) {
	for _, body := range [][]byte{
		fieldVarint(31, 0), fieldVarint(32, 0), fieldVarint(33, 0),
		fieldMessage(31, fieldVarint(1, 7)),
		fieldMessage(31, []byte{10, 5, 'x'}),
		fieldMessage(32, fieldVarint(1, 7)),
		fieldMessage(32, append(fieldString(1, "tools"), fieldString(1, "tools")...)),
		fieldMessage(32, []byte{10, 5, 'x'}),
		fieldMessage(33, fieldVarint(1, 0)),
		fieldMessage(33, fieldString(2, "depth")),
		fieldMessage(33, fieldVarint(2, 1<<32)),
		fieldMessage(33, []byte{10, 5, 'x'}),
	} {
		if _, err := decodeOSCall(body); err == nil {
			t.Fatalf("accepted malformed OS call %x", body)
		}
	}
}
