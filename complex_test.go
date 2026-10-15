package monty

import (
	"math"
	"reflect"
	"testing"
)

func assertComplexBits(t *testing.T, got, want complex128) {
	t.Helper()
	if math.Float64bits(real(got)) != math.Float64bits(real(want)) || math.Float64bits(imag(got)) != math.Float64bits(imag(want)) {
		t.Fatalf("complex bits = (%016x, %016x), want (%016x, %016x)", math.Float64bits(real(got)), math.Float64bits(imag(got)), math.Float64bits(real(want)), math.Float64bits(imag(want)))
	}
}

func TestComplexEncoding(t *testing.T) {
	type named64 complex64
	type named128 complex128
	for _, input := range []any{complex64(1.5 - 2.25i), complex128(1.5 - 2.25i), named64(1.5 - 2.25i), named128(1.5 - 2.25i)} {
		t.Run(reflect.TypeOf(input).String(), func(t *testing.T) {
			a := arenaEncoder{}
			root, err := a.add(input)
			if err != nil {
				t.Fatal(err)
			}
			values, err := decodeArena(a.encode())
			if err != nil {
				t.Fatal(err)
			}
			got, ok := values[root].(complex128)
			if !ok {
				t.Fatalf("complex decoded as %T", values[root])
			}
			assertComplexBits(t, got, 1.5-2.25i)
		})
	}
	// Independent wire fixture: complex uses node 31 with two present doubles.
	wire := fieldMessage(2, fieldMessage(31, append(fieldFixed64(1, 0x3ff8000000000000), fieldFixed64(2, 0xc002000000000000)...)))
	values, err := decodeArena(wire)
	if err != nil {
		t.Fatal(err)
	}
	assertComplexBits(t, values[0].(complex128), 1.5-2.25i)
}

func TestComplexMalformedParts(t *testing.T) {
	realPart := fieldFixed64(1, math.Float64bits(1))
	imagPart := fieldFixed64(2, math.Float64bits(2))
	for name, body := range map[string][]byte{
		"both missing":    nil,
		"real missing":    imagPart,
		"imag missing":    realPart,
		"real wrong type": append(fieldVarint(1, 1), imagPart...),
		"imag wrong type": append(realPart, fieldString(2, "2")...),
		"truncated":       append(realPart, 0x11, 0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeArena(fieldMessage(2, fieldMessage(31, body))); err == nil {
				t.Fatal("malformed complex accepted")
			}
		})
	}
	if _, err := decodeArena(fieldMessage(2, fieldVarint(31, 1))); err == nil {
		t.Fatal("non-message complex accepted")
	}
}

func TestDecodeComplex(t *testing.T) {
	type named64 complex64
	type named128 complex128
	for _, target := range []reflect.Type{reflect.TypeFor[complex64](), reflect.TypeFor[complex128](), reflect.TypeFor[named64](), reflect.TypeFor[named128]()} {
		for _, z := range []complex128{1.5 - 2.25i, complex(math.Copysign(0, -1), math.Copysign(0, -1)), complex(math.Inf(1), math.Inf(-1))} {
			got, err := convertHostArg(z, target)
			if err != nil {
				t.Fatalf("convert %v to %s: %v", z, target, err)
			}
			assertComplexBits(t, got.Complex(), z)
		}
	}
	for _, z := range []complex128{complex(math.MaxFloat64, 0), complex(0, -math.MaxFloat64)} {
		if _, err := Decode[complex64](z); err == nil {
			t.Fatalf("overflowing complex %v accepted", z)
		}
	}
	if _, err := Decode[float64](complex128(1 + 2i)); err == nil {
		t.Fatal("complex converted to real number")
	}
	want := map[string][]named64{"values": {1 + 2i, 3 - 4i}}
	got, err := Decode[map[string][]named64](Dict{{"values", List{complex128(1 + 2i), complex128(3 - 4i)}}})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("nested complex conversion = %#v, %v", got, err)
	}
}

func FuzzComplexValueRoundTrip(f *testing.F) {
	for _, parts := range [][2]uint64{{0, 0}, {1 << 63, 1 << 63}, {0x3ff8000000000000, 0xc002000000000000}, {0x7ff0000000000000, 0xfff0000000000000}, {0x7ff8000000000042, 0xfff8000000000123}} {
		f.Add(parts[0], parts[1])
	}
	f.Fuzz(func(t *testing.T, realBits, imagBits uint64) {
		want := complex(math.Float64frombits(realBits), math.Float64frombits(imagBits))
		a := arenaEncoder{}
		root, err := a.add(want)
		if err != nil {
			t.Fatal(err)
		}
		values, err := decodeArena(a.encode())
		if err != nil {
			t.Fatal(err)
		}
		assertComplexBits(t, values[root].(complex128), want)
	})
}
