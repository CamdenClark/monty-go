package monty

import (
	"bytes"
	"reflect"
	"testing"
)

func FuzzModuleValueRoundTrip(f *testing.F) {
	f.Add("tools", []byte{1, 2, 3}, int64(42))
	f.Add("pkg.sub", []byte{}, int64(-1))
	f.Fuzz(func(t *testing.T, name string, data []byte, number int64) {
		if len(name) > 1024 || len(data) > 4096 {
			t.Skip()
		}
		var id [16]byte
		copy(id[:], data)
		want := Module{Name: name, ID: id, Attrs: Dict{
			{"value", number}, {"data", string(data)},
			{"call", Function{Name: name + ".call"}},
			{"sub", Module{Name: name + ".sub", ID: [16]byte{2}, Attrs: Dict{}}},
			{"nested", List{Module{Name: name + ".empty", ID: id, Attrs: Dict{}}}},
		}}
		a := arenaEncoder{}
		root, err := a.add(want)
		if err != nil {
			t.Fatal(err)
		}
		values, err := decodeArena(a.encode())
		if err != nil || !reflect.DeepEqual(values[root], want) {
			t.Fatalf("module round trip: %#v %v", values, err)
		}
		b := arenaEncoder{}
		if _, err := b.add(values[root]); err != nil || !bytes.Equal(a.encode(), b.encode()) {
			t.Fatalf("module encoding did not stabilize: %v", err)
		}
	})
}

func FuzzMonty12OSCalls(f *testing.F) {
	f.Add("tools", uint32(0), uint8(0))
	f.Add("/data", uint32(3), uint8(255))
	f.Fuzz(func(t *testing.T, name string, depth uint32, flags uint8) {
		if len(name) > 4096 {
			t.Skip()
		}
		call, err := decodeOSCall(fieldMessage(31, fieldString(1, name)))
		if err != nil || call.Name != "import" || !reflect.DeepEqual(call.Args, []Value{name}) {
			t.Fatalf("import: %#v %v", call, err)
		}
		stubs := append(fieldString(1, name), fieldString(1, name+"_other")...)
		call, err = decodeOSCall(fieldMessage(32, stubs))
		if err != nil || call.Name != "import_stubs" || !reflect.DeepEqual(call.Args, []Value{List{name, name + "_other"}}) {
			t.Fatalf("stubs: %#v %v", call, err)
		}
		body := append(fieldString(1, name), fieldBool(3, flags&1 != 0)...)
		body = append(body, fieldString(4, "**")...)
		body = append(body, fieldString(4, "*.py")...)
		body = append(body, fieldBool(6, flags&2 != 0)...)
		var wantDepth, wantCase Value
		if flags&4 != 0 {
			body = append(body, fieldVarint(2, uint64(depth))...)
			wantDepth = depth
		}
		if flags&8 != 0 {
			body = append(body, fieldBool(5, flags&16 != 0)...)
			wantCase = flags&16 != 0
		}
		call, err = decodeOSCall(fieldMessage(33, body))
		want := []Value{name, wantDepth, flags&1 != 0, List{"**", "*.py"}, wantCase, flags&2 != 0}
		if err != nil || call.Name != "Path.scan" || !reflect.DeepEqual(call.Args, want) {
			t.Fatalf("scan: %#v %v", call, err)
		}
	})
}
