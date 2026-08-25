package monty

import (
	"context"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValueWireRoundTrip(t *testing.T) {
	offset, zone := -7*3600, "PDT"
	values := []Value{
		nil, Ellipsis{}, NotImplemented{}, false, int64(0), int64(-99), new(big.Int).Lsh(big.NewInt(1), 200),
		1.5, "hello", []byte{0, 255}, List{int64(1), "x"}, Tuple{}, Set{int64(1)}, FrozenSet{"x"},
		Dict{{Key: Tuple{int64(1)}, Value: "tuple key"}}, NamedTuple{TypeName: "Point", FieldNames: []string{"x", "y"}, Values: []Value{int64(1), int64(2)}},
		Date{2026, 8, 24}, DateTime{Year: 2026, Month: 8, Day: 24, Hour: 12, OffsetSeconds: &offset, TimezoneName: &zone},
		TimeDelta{-1, 86399, 999999}, TimeZone{offset, &zone}, Exception{"ValueError", "bad"}, Type("int"),
		BuiltinFunction("len"), Path("/a/b"), FileHandle{"/a", "rb", 3}, Dataclass{"Point", 9, []string{"x"}, Dict{{Key: "x", Value: int64(1)}}, true},
		Function{Name: "host"}, InstanceType("Thing"),
	}
	for _, value := range values {
		name := "nil"
		if value != nil {
			name = reflect.TypeOf(value).String()
		}
		t.Run(name, func(t *testing.T) {
			wire, err := encodeValue(value)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeValue(wire)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, value) {
				t.Fatalf("got %#v (%T), want %#v (%T)", got, got, value, value)
			}
		})
	}
}

func TestValueEncodingGoTypesAndCycles(t *testing.T) {
	type input struct {
		Name   string `json:"name"`
		Hidden string `monty:"-"`
		Count  int
	}
	wire, err := encodeValue(input{Name: "Ada", Hidden: "secret", Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeValue(wire)
	if err != nil {
		t.Fatal(err)
	}
	want := Dict{{Key: "name", Value: "Ada"}, {Key: "Count", Value: int64(2)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	wire, err = encodeValue([]int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := decodeValue(wire); !reflect.DeepEqual(got, List{int64(1), int64(2)}) {
		t.Fatalf("slice got %#v", got)
	}
	wire, err = encodeValue(map[string]int{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := decodeValue(wire); !reflect.DeepEqual(got, Dict{{Key: "a", Value: int64(1)}, {Key: "b", Value: int64(2)}}) {
		t.Fatalf("map got %#v", got)
	}
	wire, err = encodeValue(-time.Microsecond)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := decodeValue(wire); !reflect.DeepEqual(got, TimeDelta{Days: -1, Seconds: 86399, Microseconds: 999999}) {
		t.Fatalf("duration got %#v", got)
	}

	tm := time.Date(2026, 8, 24, 12, 30, 5, 123000, time.FixedZone("PDT", -7*3600))
	wire, err = encodeValue(tm)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeValue(wire)
	if err != nil {
		t.Fatal(err)
	}
	dt := decoded.(DateTime)
	if dt.Year != 2026 || dt.Microsecond != 123 || dt.OffsetSeconds == nil || *dt.OffsetSeconds != -7*3600 {
		t.Fatalf("got %#v", dt)
	}

	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := encodeValue(cycle); err == nil || !strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("got %v", err)
	}
}

func TestCanonicalFileMode(t *testing.T) {
	for input, want := range map[string]string{"r": "r", "rt": "r", "br": "rb", "wb": "wb"} {
		got, err := CanonicalFileMode(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "rr", "x", "rbt", "r+", "r++", "q"} {
		if _, err := CanonicalFileMode(input); err == nil {
			t.Errorf("%q should fail", input)
		}
	}
	h, err := NewFileHandle("/x", "br", 4)
	if err != nil {
		t.Fatal(err)
	}
	if h.Mode != "rb" || !h.Binary() || !h.Readable() || h.Writable() {
		t.Fatalf("got %#v", h)
	}
}

func TestReflectedHostFunctions(t *testing.T) {
	type person struct {
		Name string `monty:"name"`
		Age  int    `monty:"age"`
	}
	fn := func(ctx context.Context, p person, nums ...int) (int, error) {
		if ctx == nil {
			t.Fatal("nil context")
		}
		total := p.Age
		for _, n := range nums {
			total += n
		}
		return total, nil
	}
	got, err := callHost(context.Background(), fn, []Value{Dict{{Key: "name", Value: "Ada"}, {Key: "age", Value: int64(30)}}, int64(5), int64(7)}, nil)
	if err != nil || got != 42 {
		t.Fatalf("got %#v, %v", got, err)
	}
	_, err = callHost(context.Background(), func(v int8) {}, []Value{int64(300)}, nil)
	if err == nil {
		t.Fatal("expected numeric overflow")
	}
	_, err = callHost(context.Background(), func() { panic("boom") }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "panic in host function") {
		t.Fatalf("got %v", err)
	}
	complexFn := func(slice []int, array [2]string, values map[string]int, pointer *int) int {
		return slice[0] + len(array[1]) + values["x"] + *pointer
	}
	got, err = callHost(context.Background(), complexFn, []Value{
		List{int64(1)}, Tuple{"a", "bb"}, Dict{{Key: "x", Value: int64(3)}}, int64(4),
	}, nil)
	if err != nil || got != 10 {
		t.Fatalf("complex conversion got %#v, %v", got, err)
	}
}

func TestPrintCollectors(t *testing.T) {
	stringsOut, err := NewCollectString(5)
	if err != nil {
		t.Fatal(err)
	}
	if err = stringsOut.Write(Stdout, "hello"); err != nil {
		t.Fatal(err)
	}
	if stringsOut.Output() != "hello" {
		t.Fatal(stringsOut.Output())
	}
	if err = stringsOut.Write(Stderr, "!"); err == nil {
		t.Fatal("expected cap error")
	}
	streams, err := NewCollectStreams()
	if err != nil {
		t.Fatal(err)
	}
	_ = streams.Write(Stdout, "a")
	_ = streams.Write(Stderr, "b")
	want := []CollectedStreamEntry{{Stdout, "a"}, {Stderr, "b"}}
	if !reflect.DeepEqual(streams.Output(), want) {
		t.Fatalf("got %#v", streams.Output())
	}
}
