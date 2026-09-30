package monty

import (
	"reflect"
	"testing"
)

func TestArenaRoundTrip(t *testing.T) {
	values := []Value{Dict{{"items", Tuple{int64(42), "a", nil}}, {"time", Time{Hour: 12, Minute: 30}}}, NamedTuple{TypeName: "Point", FieldNames: []string{"x"}, Values: []Value{int64(1)}}}
	a := arenaEncoder{}
	var roots []uint32
	for _, v := range values {
		i, err := a.add(v)
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, i)
	}
	decoded, err := decodeArena(a.encode())
	if err != nil {
		t.Fatal(err)
	}
	for i, root := range roots {
		if !reflect.DeepEqual(decoded[root], values[i]) {
			t.Fatalf("got %#v want %#v", decoded[root], values[i])
		}
	}
}

func TestArenaSharedReferencesAndBounds(t *testing.T) {
	// Two list entries reference the same child node.
	node := fieldMessage(11, append(fieldVarint(1, 0), fieldVarint(1, 0)...))
	values, err := decodeArena(append(fieldMessage(2, fieldString(8, "shared")), fieldMessage(2, node)...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values[1], List{"shared", "shared"}) {
		t.Fatal(values)
	}
	if _, err := decodeArena(fieldMessage(2, node)); err == nil {
		t.Fatal("forward reference accepted")
	}
	if _, err := decodeArena(fieldMessage(2, nil)); err == nil {
		t.Fatal("empty node accepted")
	}
}

func TestPrintSegmentsPreserveOrder(t *testing.T) {
	b := append(fieldMessage(1, append(fieldVarint(1, 1), fieldString(2, "out")...)), fieldMessage(1, append(fieldVarint(1, 2), fieldString(2, "err")...))...)
	if got := decodePrintSegments(b); !reflect.DeepEqual(got, []printEvent{{Stdout, "out"}, {Stderr, "err"}}) {
		t.Fatal(got)
	}
}

func TestArenaMutableReferences(t *testing.T) {
	nodes := [][]byte{
		fieldSInt64(5, 1),
		fieldMessage(11, fieldVarint(1, 0)),
		fieldMessage(11, append(fieldVarint(1, 1), fieldVarint(1, 1)...)),
	}
	var wire []byte
	for _, n := range nodes {
		wire = append(wire, fieldMessage(2, n)...)
	}
	values, err := decodeArena(wire)
	if err != nil {
		t.Fatal(err)
	}
	outer := values[2].(List)
	outer[0].(List)[0] = int64(99)
	if outer[1].(List)[0] != int64(99) || values[1].(List)[0] != int64(99) {
		t.Fatal("shared list lost identity")
	}
}

func TestArenaInvalidReferences(t *testing.T) {
	leaf := fieldMessage(2, fieldString(8, "key"))
	for name, node := range map[string][]byte{
		"self":                    fieldMessage(11, fieldVarint(1, 1)),
		"out of range":            fieldMessage(12, fieldVarint(1, 100)),
		"truncated packed index":  fieldMessage(15, fieldBytes(1, []byte{128})),
		"dict key":                fieldMessage(14, fieldMessage(1, append(fieldVarint(1, 9), fieldVarint(2, 0)...))),
		"dict value":              fieldMessage(14, fieldMessage(1, append(fieldVarint(1, 0), fieldVarint(2, 9)...))),
		"named tuple":             fieldMessage(13, fieldVarint(3, 9)),
		"class":                   fieldMessage(24, fieldVarint(1, 9)),
		"class references string": fieldMessage(24, fieldVarint(1, 0)),
		"multiple kinds":          append(fieldString(8, "x"), fieldBool(4, true)...),
		"unknown kind":            fieldMessage(99, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArena(append(leaf, fieldMessage(2, node)...)); err == nil {
				t.Fatal("malformed arena accepted")
			}
		})
	}
}

func TestArenaNodeCountIsOnlyAHint(t *testing.T) {
	values, err := decodeArena(append(fieldVarint(1, ^uint64(0)), fieldMessage(2, fieldString(8, "one"))...))
	if err != nil || !reflect.DeepEqual(values, []Value{"one"}) {
		t.Fatalf("%#v %v", values, err)
	}
}

func TestArenaRejectsWrongWireTypes(t *testing.T) {
	for name, wire := range map[string][]byte{
		"node must be a message": fieldVarint(2, 0),
		"string must be bytes":   fieldMessage(2, fieldVarint(8, 1)),
		"integer must be varint": fieldMessage(2, fieldString(5, "1")),
		"float must be fixed64":  fieldMessage(2, fieldVarint(7, 1)),
		"list must be a message": fieldMessage(2, fieldVarint(11, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArena(wire); err == nil {
				t.Fatal("wrong wire type accepted")
			}
		})
	}
}

func TestArenaClassMetadata(t *testing.T) {
	id := []byte("0123456789abcdef")
	attr := fieldMessage(1, append(fieldVarint(1, 0), fieldVarint(2, 1)...))
	class := append(fieldString(1, "Point"), fieldMessage(2, fieldBytes(1, id))...)
	class = append(class, fieldVarint(3, 2)...)
	class = append(class, fieldBool(4, true)...)
	nodes := [][]byte{fieldString(8, "x"), fieldSInt64(5, 42), fieldMessage(23, class), fieldMessage(24, append(append(fieldVarint(1, 2), fieldMessage(2, fieldBytes(1, id))...), fieldMessage(3, attr)...))}
	var wire []byte
	for _, n := range nodes {
		wire = append(wire, fieldMessage(2, n)...)
	}
	values, err := decodeArena(wire)
	if err != nil {
		t.Fatal(err)
	}
	var uuid [16]byte
	copy(uuid[:], id)
	want := ClassInstance{Type: ClassType{Name: "Point", ID: uuid, Origin: TypeOriginSandbox, IsDataclass: true, Attrs: Dict{}}, ID: uuid, Attrs: Dict{{"x", int64(42)}}}
	if !reflect.DeepEqual(values[3], want) {
		t.Fatalf("got %#v want %#v", values[3], want)
	}
}
