package monty

import (
	"bytes"
	"math"
	"math/big"
	"reflect"
	"testing"
)

type fuzzInt32 int32
type fuzzUint32 uint32
type fuzzInt8 int8
type fuzzUint16 uint16
type fuzzFloat32 float32
type fuzzFloat64 float64
type fuzzBool bool
type fuzzString string
type fuzzBytes []byte
type fuzzStruct struct {
	Name    fuzzString `json:"name"`
	Enabled fuzzBool   `monty:"enabled"`
	Payload fuzzBytes  `json:"payload,omitempty"`
	Count   fuzzUint16
	Hidden  string `json:"-"`
}

func FuzzNumericValueConversion(f *testing.F) {
	seeds := []struct {
		source, target uint8
		signed         int64
		unsigned       uint64
		floatBits      uint64
		magnitude      []byte
	}{
		{source: 0, target: 0, signed: 0},
		{source: 0, target: 1, signed: math.MinInt64},
		{source: 0, target: 4, signed: math.MaxInt64},
		{source: 3, target: 0, unsigned: math.MaxUint64},
		{source: 3, target: 5, unsigned: math.MaxInt64 + 1},
		{source: 6, target: 9, magnitude: append([]byte{1}, make([]byte, 32)...)},
		{source: 7, target: 14, magnitude: append([]byte{1}, make([]byte, 32)...)},
		{source: 8, target: 14, floatBits: math.Float64bits(math.Inf(1))},
		{source: 8, target: 13, floatBits: math.Float64bits(math.NaN())},
		{source: 9, target: 11, floatBits: uint64(math.Float32bits(math.MaxFloat32))},
	}
	for _, seed := range seeds {
		f.Add(seed.source, seed.target, seed.signed, seed.unsigned, seed.floatBits, seed.magnitude)
	}

	targets := []reflect.Type{
		reflect.TypeFor[int](), reflect.TypeFor[int8](), reflect.TypeFor[int16](), reflect.TypeFor[int32](), reflect.TypeFor[int64](),
		reflect.TypeFor[uint](), reflect.TypeFor[uint8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint32](), reflect.TypeFor[uint64](), reflect.TypeFor[uintptr](),
		reflect.TypeFor[float32](), reflect.TypeFor[float64](), reflect.TypeFor[fuzzFloat32](), reflect.TypeFor[fuzzFloat64](),
		reflect.TypeFor[fuzzInt8](), reflect.TypeFor[fuzzUint16](), reflect.TypeFor[big.Int](),
	}

	f.Fuzz(func(t *testing.T, sourceSelector, targetSelector uint8, signed int64, unsigned, floatBits uint64, magnitude []byte) {
		if len(magnitude) > 1<<10 {
			t.Skip()
		}
		input, integer, floating := fuzzNumericInput(sourceSelector, signed, unsigned, floatBits, magnitude)
		wire, err := encodeValue(input)
		if err != nil {
			t.Fatalf("encode %T: %v", input, err)
		}
		decoded, err := decodeValue(wire)
		if err != nil {
			t.Fatalf("decode %T: %v", input, err)
		}
		assertDecodedNumeric(t, decoded, integer, floating)

		target := targets[int(targetSelector)%len(targets)]
		got, gotErr := convertHostArg(decoded, target)
		want, wantOK := expectedNumericConversion(integer, floating, target)
		if !wantOK {
			if gotErr == nil {
				t.Fatalf("%#v (%T) unexpectedly converted to %s as %#v", decoded, decoded, target, got.Interface())
			}
			return
		}
		if gotErr != nil {
			t.Fatalf("%#v (%T) should convert to %s: %v", decoded, decoded, target, gotErr)
		}
		assertReflectValueEqual(t, got, want)
	})
}

func FuzzPrimitiveValueConversion(f *testing.F) {
	f.Add(uint8(0), true, "", []byte{})
	f.Add(uint8(1), false, "", []byte{})
	f.Add(uint8(3), false, "monty", []byte{})
	f.Add(uint8(5), false, "", []byte{0, 1, 127, 255})
	f.Add(uint8(6), false, "", []byte{0, 1, 127, 255})
	f.Add(uint8(7), false, "", []byte{0, 1, 127, 255})

	f.Fuzz(func(t *testing.T, selector uint8, boolean bool, text string, blob []byte) {
		if len(text) > 64<<10 || len(blob) > 64<<10 {
			t.Skip()
		}
		var input any
		var target reflect.Type
		switch selector % 8 {
		case 0:
			input, target = boolean, reflect.TypeFor[bool]()
		case 1:
			input, target = boolean, reflect.TypeFor[fuzzBool]()
		case 2:
			input, target = text, reflect.TypeFor[string]()
		case 3:
			input, target = text, reflect.TypeFor[fuzzString]()
		case 4:
			input, target = blob, reflect.TypeFor[[]byte]()
		case 5:
			input, target = blob, reflect.TypeFor[fuzzBytes]()
		case 6:
			input, target = blob, reflect.TypeFor[[4]byte]()
		case 7:
			input, target = blob, reflect.TypeFor[*fuzzBytes]()
		}

		wire, err := encodeValue(input)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeValue(wire)
		if err != nil {
			t.Fatal(err)
		}
		got, err := convertHostArg(decoded, target)
		if target.Kind() == reflect.Array && len(blob) != target.Len() {
			if err == nil {
				t.Fatalf("%d bytes unexpectedly converted to %s", len(blob), target)
			}
			return
		}
		if err != nil {
			t.Fatalf("%T should convert to %s: %v", decoded, target, err)
		}
		assertPrimitiveConversion(t, got, boolean, text, blob)
	})
}

func FuzzStructuredValueConversion(f *testing.F) {
	f.Add("monty", true, []byte{0, 127, 255}, uint16(42), "must stay hidden")
	f.Add("", false, []byte{}, uint16(0), "-")

	f.Fuzz(func(t *testing.T, name string, enabled bool, payload []byte, count uint16, hidden string) {
		if len(name) > 64<<10 || len(payload) > 64<<10 || len(hidden) > 64<<10 {
			t.Skip()
		}
		input := Dict{
			{Key: "name", Value: name},
			{Key: "enabled", Value: enabled},
			{Key: "payload", Value: payload},
			{Key: "Count", Value: int64(count)},
			{Key: "-", Value: hidden},
		}
		wire, err := encodeValue(input)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeValue(wire)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := convertHostArg(decoded, reflect.TypeFor[fuzzStruct]())
		if err != nil {
			t.Fatalf("convert struct: %v", err)
		}
		got := converted.Interface().(fuzzStruct)
		if string(got.Name) != name || bool(got.Enabled) != enabled || !bytes.Equal(got.Payload, payload) || uint16(got.Count) != count {
			t.Fatalf("converted struct = %#v", got)
		}
		if got.Hidden != "" {
			t.Fatalf("json-ignored field was populated with %q", got.Hidden)
		}

		wire, err = encodeValue(got)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := decodeValue(wire)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := DictMap(roundTrip.(Dict))
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["-"]; exists {
			t.Fatal("encoded struct included json-ignored field")
		}
		if len(fields) != 4 {
			t.Fatalf("encoded struct fields = %#v", fields)
		}
	})
}

func fuzzNumericInput(selector uint8, signed int64, unsigned, floatBits uint64, magnitude []byte) (any, *big.Int, *float64) {
	switch selector % 10 {
	case 0:
		return signed, big.NewInt(signed), nil
	case 1:
		x := int8(signed)
		return x, big.NewInt(int64(x)), nil
	case 2:
		x := fuzzInt32(signed)
		return x, big.NewInt(int64(x)), nil
	case 3:
		return unsigned, new(big.Int).SetUint64(unsigned), nil
	case 4:
		x := uint8(unsigned)
		return x, new(big.Int).SetUint64(uint64(x)), nil
	case 5:
		x := fuzzUint32(unsigned)
		return x, new(big.Int).SetUint64(uint64(x)), nil
	case 6:
		x := new(big.Int).SetBytes(magnitude)
		return x, new(big.Int).Set(x), nil
	case 7:
		x := new(big.Int).Neg(new(big.Int).SetBytes(magnitude))
		return x, new(big.Int).Set(x), nil
	case 8:
		x := math.Float64frombits(floatBits)
		return x, nil, &x
	default:
		x := float64(math.Float32frombits(uint32(floatBits)))
		return float32(x), nil, &x
	}
}

func assertDecodedNumeric(t *testing.T, got any, integer *big.Int, floating *float64) {
	t.Helper()
	if floating != nil {
		x, ok := got.(float64)
		if !ok || math.Float64bits(x) != math.Float64bits(*floating) {
			t.Fatalf("decoded numeric = %#v (%T), want float bits %016x", got, got, math.Float64bits(*floating))
		}
		return
	}
	var z *big.Int
	switch x := got.(type) {
	case int64:
		z = big.NewInt(x)
	case *big.Int:
		z = x
	default:
		t.Fatalf("decoded numeric = %#v (%T)", got, got)
	}
	if z.Cmp(integer) != 0 {
		t.Fatalf("decoded integer = %s, want %s", z, integer)
	}
}

func expectedNumericConversion(integer *big.Int, floating *float64, target reflect.Type) (reflect.Value, bool) {
	out := reflect.New(target).Elem()
	if floating != nil {
		if target.Kind() != reflect.Float32 && target.Kind() != reflect.Float64 {
			return reflect.Value{}, false
		}
		x := *floating
		if !math.IsInf(x, 0) && out.OverflowFloat(x) {
			return reflect.Value{}, false
		}
		out.SetFloat(x)
		return out, true
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !integer.IsInt64() || out.OverflowInt(integer.Int64()) {
			return reflect.Value{}, false
		}
		out.SetInt(integer.Int64())
		return out, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if !integer.IsUint64() || out.OverflowUint(integer.Uint64()) {
			return reflect.Value{}, false
		}
		out.SetUint(integer.Uint64())
		return out, true
	case reflect.Float32, reflect.Float64:
		x, _ := new(big.Float).SetInt(integer).Float64()
		if math.IsInf(x, 0) || out.OverflowFloat(x) {
			return reflect.Value{}, false
		}
		out.SetFloat(x)
		return out, true
	}
	if target == reflect.TypeFor[big.Int]() {
		out.Set(reflect.ValueOf(*new(big.Int).Set(integer)))
		return out, true
	}
	return reflect.Value{}, false
}

func assertReflectValueEqual(t *testing.T, got, want reflect.Value) {
	t.Helper()
	if isFloat(got.Kind()) {
		if math.Float64bits(got.Float()) != math.Float64bits(want.Float()) {
			t.Fatalf("converted float bits = %016x, want %016x", math.Float64bits(got.Float()), math.Float64bits(want.Float()))
		}
		return
	}
	if !reflect.DeepEqual(got.Interface(), want.Interface()) {
		t.Fatalf("converted value = %#v (%s), want %#v", got.Interface(), got.Type(), want.Interface())
	}
}

func assertPrimitiveConversion(t *testing.T, got reflect.Value, boolean bool, text string, blob []byte) {
	t.Helper()
	for got.Kind() == reflect.Pointer {
		got = got.Elem()
	}
	switch got.Kind() {
	case reflect.Bool:
		if got.Bool() != boolean {
			t.Fatalf("converted bool = %v, want %v", got.Bool(), boolean)
		}
	case reflect.String:
		if got.String() != text {
			t.Fatalf("converted string = %q, want %q", got.String(), text)
		}
	case reflect.Slice, reflect.Array:
		converted := make([]byte, got.Len())
		reflect.Copy(reflect.ValueOf(converted), got)
		if !bytes.Equal(converted, blob) {
			t.Fatalf("converted bytes = %v, want %v", converted, blob)
		}
	default:
		t.Fatalf("unexpected converted kind %s", got.Kind())
	}
}
