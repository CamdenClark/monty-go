package monty

import (
	"bytes"
	"math"
	"reflect"
	"testing"
)

// Build a valid graph independently of the encoder. References always point
// backwards, and expected containers reuse their referenced Go values.
func arenaGraph(data []byte) ([]byte, []Value) {
	nodes := [][]byte{fieldSInt64(5, 42), fieldString(8, "seed"), fieldMessage(2, nil)}
	values := []Value{int64(42), "seed", nil}
	if len(data) > 96 {
		data = data[:96]
	}
	for i, c := range data {
		var node []byte
		var value Value
		left, right := uint64(c)%uint64(len(values)), uint64(i)%uint64(len(values))
		switch c % 8 {
		case 0:
			node = fieldSInt64(5, int64(int8(c)))
			value = int64(int8(c))
		case 1:
			node = fieldString(8, string([]byte{'a' + c%26}))
			value = string([]byte{'a' + c%26})
		case 2:
			node = fieldBool(4, c&1 != 0)
			value = c&1 != 0
		case 3, 4, 5:
			// Alternate packed and unpacked references, both accepted by protobuf.
			refs := append(fieldVarint(1, left), fieldVarint(1, right)...)
			if i%2 == 0 {
				refs = fieldBytes(1, []byte{byte(left), byte(right)})
			}
			tag := 11
			if c%8 == 4 {
				tag = 12
			}
			if c%8 == 5 {
				tag = 16
			}
			node = fieldMessage(tag, refs)
			items := []Value{values[left], values[right]}
			switch tag {
			case 11:
				value = List(items)
			case 12:
				value = Tuple(items)
			case 16:
				value = FrozenSet(items)
			}
		case 6:
			node = fieldMessage(14, fieldMessage(1, append(fieldVarint(1, left), fieldVarint(2, right)...)))
			value = Dict{{values[left], values[right]}}
		case 7:
			node = fieldFixed64(7, math.Float64bits(float64(c)/2))
			value = float64(c) / 2
		}
		nodes = append(nodes, node)
		values = append(values, value)
	}
	wire := fieldVarint(1, uint64(len(nodes)))
	for _, node := range nodes {
		wire = append(wire, fieldMessage(2, node)...)
	}
	return wire, values
}

func FuzzArenaGraph(f *testing.F) {
	for _, data := range [][]byte{nil, {3, 3, 3}, {6, 4, 5, 3, 7}, bytes.Repeat([]byte{3}, 40), {0, 1, 2, 3, 4, 5, 6, 7}} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		wire, want := arenaGraph(data)
		got, err := decodeArena(wire)
		if err != nil {
			t.Fatal(err)
		}
		// Compare each node shallowly: walking shared graphs recursively can be
		// exponential even when the arena itself is small.
		if len(got) != len(want) {
			t.Fatal("node count changed")
		}
		bad := append(append([]byte(nil), wire...), fieldMessage(2, fieldMessage(11, fieldVarint(1, uint64(len(want)))))...)
		if _, err := decodeArena(bad); err == nil {
			t.Fatal("generated self reference accepted")
		}
		for i := range want {
			if reflect.TypeOf(got[i]) != reflect.TypeOf(want[i]) {
				t.Fatalf("node %d type: %T want %T", i, got[i], want[i])
			}
			switch w := want[i].(type) {
			case List:
				assertGraphItems(t, got, want, []Value(got[i].(List)), []Value(w))
			case Tuple:
				assertGraphItems(t, got, want, []Value(got[i].(Tuple)), []Value(w))
			case FrozenSet:
				assertGraphItems(t, got, want, []Value(got[i].(FrozenSet)), []Value(w))
			case Dict:
				d := got[i].(Dict)
				if len(d) != len(w) {
					t.Fatal("dict length")
				}
				for j := range w {
					assertGraphItems(t, got, want, []Value{d[j].Key, d[j].Value}, []Value{w[j].Key, w[j].Value})
				}
			default:
				if !reflect.DeepEqual(got[i], want[i]) {
					t.Fatalf("node %d: %#v want %#v", i, got[i], want[i])
				}
			}
		}
	})
}
func assertGraphItems(t *testing.T, gotNodes, wantNodes, got, want []Value) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatal("container length")
	}
	for i := range want {
		if reflect.TypeOf(got[i]) != reflect.TypeOf(want[i]) {
			t.Fatal("child type changed")
		}
		g, w := reflect.ValueOf(got[i]), reflect.ValueOf(want[i])
		if g.IsValid() && g.Kind() == reflect.Slice {
			if g.Len() != w.Len() {
				t.Fatal("child length changed")
			}
			found := false
			for j, node := range wantNodes {
				n := reflect.ValueOf(node)
				if n.IsValid() && n.Type() == w.Type() && n.Kind() == reflect.Slice && n.Pointer() == w.Pointer() {
					if g.Pointer() != reflect.ValueOf(gotNodes[j]).Pointer() {
						t.Fatal("reference points to wrong arena node")
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatal("oracle reference has no node")
			}
		} else if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatal("leaf changed")
		}
	}
}

func FuzzArenaRoundTrip(f *testing.F) {
	for _, data := range [][]byte{nil, {0, 1, 2}, []byte("nested values"), bytes.Repeat([]byte{255}, 64)} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			data = data[:256]
		}
		offset := int(int8(len(data))) * 60
		name := "fixed"
		items := List{nil, true, int64(-42), string(data), append([]byte(nil), data...), Time{Hour: len(data) % 24, Microsecond: len(data) * 100, OffsetSeconds: &offset, TimezoneName: &name, Fold: len(data) % 2}}
		want := Dict{{"items", Tuple{items, Set{int64(1)}, FrozenSet{int64(2)}}}, {"named", NamedTuple{TypeName: "Point", FieldNames: []string{"value"}, Values: []Value{items}}}}
		a := arenaEncoder{}
		root, err := a.add(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeArena(a.encode())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[root], want) {
			t.Fatal("round trip changed value")
		}
		b := arenaEncoder{}
		_, err = b.add(got[root])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.encode(), b.encode()) {
			t.Fatal("encoding did not stabilize")
		}
	})
}

func FuzzArenaMalformed(f *testing.F) {
	valid, _ := arenaGraph([]byte{3, 6, 4, 5})
	for _, data := range [][]byte{nil, valid, fieldMessage(2, nil), fieldMessage(2, fieldMessage(11, fieldVarint(1, 100))), fieldMessage(2, append(fieldString(8, "a"), fieldString(8, "b")...)), fieldMessage(2, fieldMessage(11, fieldBytes(1, []byte{128})))} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		// No recursive formatting or re-encoding: valid hostile graphs can have
		// exponentially many paths while occupying only a linear amount of wire.
		_, _ = decodeArena(data)
		_, _ = decodeComplete(data)
		_, _ = decodeFunctionCall(data)
		_, _ = decodeOSCall(data)
	})
}
