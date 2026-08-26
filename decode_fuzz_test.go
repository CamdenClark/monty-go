package monty

import (
	"bytes"
	"math/big"
	"reflect"
	"strconv"
	"testing"
)

type fuzzDecodedProfile struct {
	City   string `json:"city"`
	Active bool   `monty:"active"`
}

type fuzzDecodedResult struct {
	Name     string              `monty:"name"`
	Profile  *fuzzDecodedProfile `monty:"profile"`
	Tags     []string            `monty:"tags"`
	Scores   map[string]int16    `monty:"scores"`
	Pair     [2]uint8            `monty:"pair"`
	Optional *int64              `monty:"optional"`
	Missing  string              `monty:"missing"`
	Ignored  string              `monty:"-"`
}

// FuzzDecodeNestedResult checks a successful recursive conversion against a
// value assembled independently, after the same wire round trip used by a real
// Monty result.
func FuzzDecodeNestedResult(f *testing.F) {
	f.Add("Ada", "London", []byte{1, 2, 255}, int64(42), uint8(3), uint8(4), true, int64(9))
	f.Add("", "", []byte{}, int64(-1), uint8(0), uint8(255), false, int64(0))

	f.Fuzz(func(t *testing.T, name, city string, tagBytes []byte, score int64, first, second uint8, hasOptional bool, optional int64) {
		if len(name) > 64<<10 || len(city) > 64<<10 || len(tagBytes) > 1<<10 {
			t.Skip()
		}
		tags := make(List, len(tagBytes))
		wantTags := make([]string, len(tagBytes))
		for i, item := range tagBytes {
			text := strconv.Itoa(int(item))
			tags[i], wantTags[i] = text, text
		}
		var optionalValue Value
		var wantOptional *int64
		if hasOptional {
			optionalValue = optional
			copy := optional
			wantOptional = &copy
		}
		boundedScore := int16(score)
		input := Dict{
			{Key: "name", Value: name},
			{Key: "profile", Value: Dict{{Key: "city", Value: city}, {Key: "active", Value: hasOptional}}},
			{Key: "tags", Value: tags},
			{Key: "scores", Value: Dict{{Key: "primary", Value: int64(boundedScore)}}},
			{Key: "pair", Value: Tuple{int64(first), int64(second)}},
			{Key: "optional", Value: optionalValue},
			{Key: "unknown", Value: "ignored"},
		}
		wire, err := encodeValue(input)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeValue(wire)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode[fuzzDecodedResult](decoded)
		if err != nil {
			t.Fatal(err)
		}
		want := fuzzDecodedResult{
			Name:     name,
			Profile:  &fuzzDecodedProfile{City: city, Active: hasOptional},
			Tags:     wantTags,
			Scores:   map[string]int16{"primary": boundedScore},
			Pair:     [2]uint8{first, second},
			Optional: wantOptional,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded %#v, want %#v", got, want)
		}
	})
}

type fuzzArbitraryResult struct {
	Value  *int16            `monty:"value"`
	Items  []uint8           `monty:"items"`
	Nested map[string]string `monty:"nested"`
}

// FuzzDecodeArbitraryResult exercises successful and rejected conversions of
// recursively shaped boundary values. Every outcome is valid except a panic.
func FuzzDecodeArbitraryResult(f *testing.F) {
	f.Add(uint8(0), []byte{})
	f.Add(uint8(1), []byte{7, 1, 2, 3})
	f.Add(uint8(7), []byte{9, 6, 5, 4, 3, 2, 1})
	f.Add(uint8(8), []byte{'A'})
	// An interface key can hold a non-comparable List. This must return an
	// error instead of reaching reflect.SetMapIndex and panicking.
	f.Add(uint8(3), []byte("A0000"))

	f.Fuzz(func(t *testing.T, target uint8, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		value := fuzzBoundaryValue(data, 0)
		switch target % 9 {
		case 0:
			_, _ = Decode[fuzzArbitraryResult](value)
		case 1:
			_, _ = Decode[*fuzzArbitraryResult](value)
		case 2:
			_, _ = Decode[map[string]any](value)
		case 3:
			_, _ = Decode[map[any]any](value)
		case 4:
			_, _ = Decode[[]int16](value)
		case 5:
			_, _ = Decode[[3]uint8](value)
		case 6:
			_, _ = Decode[int8](value)
		case 7:
			_, _ = Decode[string](value)
		case 8:
			_, _ = Decode[any](value)
		}
	})
}

func fuzzBoundaryValue(data []byte, depth int) Value {
	if len(data) == 0 {
		return nil
	}
	tag, body := data[0], data[1:]
	if depth >= 4 || len(body) == 0 {
		switch tag % 6 {
		case 0:
			return int64(int8(tag))
		case 1:
			return tag&1 != 0
		case 2:
			return string(body)
		case 3:
			return append([]byte(nil), body...)
		case 4:
			return float64(int8(tag)) / 3
		default:
			return nil
		}
	}
	middle := len(body) / 2
	left := fuzzBoundaryValue(body[:middle], depth+1)
	right := fuzzBoundaryValue(body[middle:], depth+1)
	switch tag % 12 {
	case 0:
		return List{left, right}
	case 1:
		return Tuple{left, right}
	case 2:
		return Set{left, right}
	case 3:
		return FrozenSet{left, right}
	case 4:
		return Dict{{Key: "value", Value: left}, {Key: "items", Value: right}}
	case 5:
		return Dict{{Key: left, Value: right}}
	case 6:
		return Dataclass{Attrs: Dict{{Key: "value", Value: left}, {Key: "nested", Value: right}}}
	case 7:
		fields := []string{"value", "items"}
		values := []Value{left, right}
		if tag&0x80 != 0 {
			fields = fields[:1]
		}
		return NamedTuple{FieldNames: fields, Values: values}
	case 8:
		value := new(big.Int).SetBytes(body)
		if tag&0x80 != 0 {
			value.Neg(value)
		}
		return value
	case 9:
		return Repr(string(body))
	case 10:
		return Path(string(body))
	default:
		return Exception{Type: string(body[:middle]), Message: string(body[middle:])}
	}
}

// FuzzMalformedProtocolDecoders passes untrusted bytes directly to the value
// and event decoders. Generated-value fuzzers do not cover malformed protobuf
// tags, lengths, or truncated frames.
func FuzzMalformedProtocolDecoders(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01})
	f.Add([]byte{0x0a, 0x03, 'a'})

	offset, zoneName, docstring := 3600, "UTC+1", "documentation"
	boundaryValues := []Value{
		Ellipsis{}, nil, true, int64(-42), big.NewInt(1 << 62), 3.5,
		"text", []byte{0, 1, 255}, List{"item"}, Tuple{int64(1)},
		NamedTuple{TypeName: "Point", FieldNames: []string{"x"}, Values: []Value{int64(1)}},
		Dict{{Key: "key", Value: "value"}}, Set{int64(1)}, FrozenSet{int64(2)},
		Date{Year: 2026, Month: 8, Day: 25},
		DateTime{Year: 2026, Month: 8, Day: 25, Hour: 12, OffsetSeconds: &offset, TimezoneName: &zoneName},
		TimeDelta{Days: -1, Seconds: 2, Microseconds: 3}, TimeZone{OffsetSeconds: 3600, Name: &zoneName},
		Exception{Type: "ValueError", Message: "bad value"}, Type("int"), BuiltinFunction("len"), Path("/virtual"),
		FileHandle{Path: "/virtual/file", Mode: "rb", Position: 4},
		Dataclass{Name: "Point", TypeID: 7, FieldNames: []string{"x"}, Attrs: Dict{{Key: "x", Value: int64(1)}}, Frozen: true},
		Function{Name: "callback", Docstring: &docstring}, Repr("<opaque>"), Cycle{Identity: 9, Placeholder: "[...]"},
		InstanceType("Widget"), NotImplemented{},
	}
	for _, value := range boundaryValues {
		wire, err := encodeValue(value)
		if err != nil {
			f.Fatalf("encode seed %T: %v", value, err)
		}
		f.Add(wire)
	}

	valueWire, err := encodeValue(Dict{{Key: "ok", Value: true}})
	if err != nil {
		f.Fatal(err)
	}
	printBody := append(fieldVarint(1, uint64(Stdout)), fieldString(2, "hello")...)
	callBody := append(fieldString(1, "callback"), fieldMessage(2, valueWire)...)
	callBody = append(callBody, fieldVarint(4, 7)...)
	osBody := fieldMessage(21, append(fieldString(1, "KEY"), fieldMessage(2, valueWire)...))
	completeBody := fieldMessage(1, valueWire)
	raisedBody := encodeRaised(RaisedException{Type: "ValueError", Message: "bad value"})
	errorBody := fieldMessage(1, raisedBody)
	protocolBodies := [][]byte{printBody, callBody, osBody, completeBody, raisedBody, errorBody}
	for _, body := range protocolBodies {
		f.Add(body)
	}
	eventBodies := []struct {
		kind eventKind
		body []byte
	}{
		{eventPrint, printBody}, {eventFunctionCall, callBody}, {eventOSCall, osBody},
		{eventNameLookup, fieldString(1, "missing")}, {eventResolveFutures, fieldVarint(1, 7)},
		{eventComplete, completeBody}, {eventError, errorBody}, {eventTypingError, errorBody},
		{eventDump, fieldBytes(1, []byte("snapshot"))}, {eventOK, nil},
		{eventFatal, fieldString(1, "fatal")}, {eventShutdownDump, fieldBytes(1, []byte("snapshot"))},
	}
	for _, event := range eventBodies {
		wire := fieldMessage(int(event.kind), event.body)
		f.Add(wire)
		var frame bytes.Buffer
		if err := writeFrame(&frame, wire); err != nil {
			f.Fatal(err)
		}
		f.Add(frame.Bytes())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		value, err := decodeValue(data)
		if err == nil {
			canonical, encodeErr := encodeValue(value)
			if encodeErr == nil {
				canonicalValue, decodeErr := decodeValue(canonical)
				if decodeErr != nil {
					t.Fatalf("canonical value failed to decode: %v", decodeErr)
				}
				second, secondErr := encodeValue(canonicalValue)
				if secondErr != nil {
					t.Fatalf("canonical value failed to encode: %v", secondErr)
				}
				if !bytes.Equal(second, canonical) {
					t.Fatalf("value encoding did not stabilize: %x then %x", canonical, second)
				}
			}
		}
		_, _ = decodeEvent(data)
		_ = decodePrint(data)
		_, _ = decodeFunctionCall(data)
		_, _ = decodeOSCall(data)
		_, _ = decodeComplete(data)
		_, _ = decodeRaised(data)
		_, _ = decodeErrorEvent(data)
		_ = decodeStringField(data, 1)
		_ = decodeUint32List(data, 1)
		_ = decodeBytesField(data, 1)
		_, _ = readFrame(bytes.NewReader(data))
	})
}
