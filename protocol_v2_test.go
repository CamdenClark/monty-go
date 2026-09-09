package monty

import (
	"reflect"
	"testing"
)

func TestProtocolV2Configuration(t *testing.T) {
	request := configureRequest(configureWire{})
	var version uint64
	flush := false
	_ = parseFields(request.body, func(f wireField) error {
		if f.tag == 9 {
			version = f.varint
		}
		if f.tag == 10 {
			flush = f.varint == 0
		}
		return nil
	})
	if version != 2 || !flush {
		t.Fatalf("configuration: version %d, line buffering %v", version, flush)
	}
}

func TestProtocolV2RejectsLegacyAndHostInputs(t *testing.T) {
	for _, value := range []Value{Dataclass{}, InstanceType("Point"), ClassType{}, ClassInstance{}} {
		if _, err := encodeValue(value); err == nil {
			t.Errorf("accepted %T", value)
		}
	}
}

func protocolV2ClassSeeds() [][]byte {
	id := fieldBytes(1, []byte("0123456789abcdef"))
	class := append(fieldString(1, "Point"), fieldMessage(2, id)...)
	class = append(class, fieldVarint(3, uint64(TypeOriginSandbox))...)
	class = append(class, fieldBool(4, true)...)
	instance := append(fieldMessage(1, class), fieldMessage(2, id)...)
	return [][]byte{fieldMessage(23, class), fieldMessage(24, instance), fieldMessage(23, fieldVarint(3, 99)), fieldMessage(24, fieldMessage(2, fieldBytes(1, []byte{1}))), fieldMessage(10, id), fieldString(29, "<opaque>"), fieldMessage(30, fieldVarint(1, 42))}
}

func TestProtocolV2ObjectReceiver(t *testing.T) {
	id := [16]byte{1, 2, 3}
	call, err := decodeFunctionCall(append(fieldString(1, "method"), fieldMessage(5, fieldBytes(1, id[:]))...))
	if err != nil || !call.MethodCall || call.ObjectID == nil || *call.ObjectID != id {
		t.Fatalf("receiver = %#v, %v", call, err)
	}
	if _, err := decodeFunctionCall(fieldMessage(5, fieldBytes(1, []byte{1}))); err == nil {
		t.Fatal("accepted malformed UUID")
	}
}

func FuzzProtocolV2Time(f *testing.F) {
	f.Add(uint8(23), uint8(59), uint8(59), uint32(999999), int32(-3600), "UTC-1", true, true)
	f.Add(uint8(0), uint8(0), uint8(0), uint32(0), int32(0), "", false, false)
	f.Fuzz(func(t *testing.T, hour, minute, second uint8, micro uint32, offset int32, name string, aware, fold bool) {
		if len(name) > 1024 {
			t.Skip()
		}
		x := Time{Hour: int(hour % 24), Minute: int(minute % 60), Second: int(second % 60), Microsecond: int(micro % 1000000)}
		if aware {
			o := int(offset % 86400)
			x.OffsetSeconds = &o
			x.TimezoneName = &name
		}
		if fold {
			x.Fold = 1
		}
		b, err := encodeValue(x)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeValue(b)
		if err != nil || !reflect.DeepEqual(got, x) {
			t.Fatalf("time = %#v, want %#v: %v", got, x, err)
		}
	})
}

func TestProtocolV2ClassValues(t *testing.T) {
	seeds := protocolV2ClassSeeds()
	classValue, err := decodeValue(seeds[0])
	if err != nil {
		t.Fatal(err)
	}
	class := classValue.(ClassType)
	if class.Name != "Point" || class.ID != ([16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}) || class.Origin != TypeOriginSandbox || !class.IsDataclass {
		t.Fatalf("class = %#v", class)
	}
	instanceValue, err := decodeValue(seeds[1])
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceValue.(ClassInstance)
	if !reflect.DeepEqual(instance.Type, class) || instance.ID != class.ID {
		t.Fatalf("instance = %#v", instance)
	}
	for _, seed := range seeds[2:5] {
		if _, err := decodeValue(seed); err == nil {
			t.Errorf("accepted malformed or unsupported value %x", seed)
		}
	}
	state := &runState{}
	id := [16]byte{1}
	progress, err := state.progress(childEvent{kind: eventNameLookup, body: append(fieldString(1, "value"), fieldMessage(2, fieldBytes(1, id[:]))...)})
	if err != nil {
		t.Fatal(err)
	}
	lookup := progress.(*NameLookupSnapshot)
	if lookup.ObjectID == nil || *lookup.ObjectID != id || lookup.Name != "value" {
		t.Fatalf("lookup = %#v", lookup)
	}
}
