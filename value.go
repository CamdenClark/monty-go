package monty

import (
	"encoding"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Value is any value that can cross the Monty boundary. Concrete container
// types below preserve Python distinctions which a plain Go value cannot.
type Value = any

// Ellipsis represents Python's Ellipsis singleton.
type Ellipsis struct{}

// NotImplemented represents Python's NotImplemented singleton.
type NotImplemented struct{}

// List represents a Python list. Ordinary Go slices and arrays encode as lists.
type List []Value

// Tuple represents a Python tuple.
type Tuple []Value

// Set represents a Python set.
type Set []Value

// FrozenSet represents a Python frozenset.
type FrozenSet []Value

// Pair is one insertion-ordered Python dictionary entry.
type Pair struct {
	Key   Value
	Value Value
}

// Dict preserves arbitrary Python key types and insertion order.
type Dict []Pair

// NamedTuple is the lossless representation of a Python named tuple.
type NamedTuple struct {
	TypeName   string
	FieldNames []string
	Values     []Value
}

// Date represents datetime.date.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateTime represents datetime.datetime. OffsetSeconds and TimezoneName are
// nil for a naive datetime.
type DateTime struct {
	Year                              int
	Month                             time.Month
	Day                               int
	Hour, Minute, Second, Microsecond int
	OffsetSeconds                     *int
	TimezoneName                      *string
}

// TimeDelta represents datetime.timedelta in Monty's normalized form.
type TimeDelta struct {
	Days, Seconds, Microseconds int
}

// TimeZone represents datetime.timezone.
type TimeZone struct {
	OffsetSeconds int
	Name          *string
}

// Exception is a Python exception value (not an execution error with traceback).
type Exception struct {
	Type    string
	Message string
}

// Type is a Python built-in type object, such as "int" or "ValueError".
type Type string

// InstanceType is the legacy name-only sandbox type representation.
//
// Deprecated: protocol v2 uses class descriptors with UUIDs. InstanceType inputs
// are rejected.
type InstanceType string

// BuiltinFunction is a Python builtin function value.
type BuiltinFunction string

// Path is a virtual POSIX pathlib.Path. It is never a host path.
type Path string

// Function is a marker for a host function supplied through name lookup.
type Function struct {
	Name      string
	Docstring *string
}

// FileHandle is host-side metadata for a sandbox file. It never contains a
// live operating-system descriptor.
type FileHandle struct {
	Path     string
	Mode     string
	Position uint64
}

// NewFileHandle validates and canonicalizes a Monty file handle.
func NewFileHandle(path, mode string, position uint64) (FileHandle, error) {
	canonical, err := CanonicalFileMode(mode)
	if err != nil {
		return FileHandle{}, err
	}
	return FileHandle{Path: path, Mode: canonical, Position: position}, nil
}

func (f FileHandle) Binary() bool { return strings.Contains(f.Mode, "b") }
func (f FileHandle) Readable() bool {
	return strings.HasPrefix(f.Mode, "r") || strings.Contains(f.Mode, "+")
}
func (f FileHandle) Writable() bool {
	return strings.HasPrefix(f.Mode, "w") || strings.HasPrefix(f.Mode, "a") || strings.Contains(f.Mode, "+")
}

// CanonicalFileMode validates the file modes supported by Monty.
func CanonicalFileMode(mode string) (string, error) {
	if mode == "" {
		return "", fmt.Errorf("must have exactly one of read/write/append mode")
	}
	var action byte
	binary, text := false, false
	for i := range len(mode) {
		switch c := mode[i]; c {
		case 'r', 'w', 'a':
			if action != 0 {
				return "", fmt.Errorf("must have exactly one of read/write/append mode")
			}
			action = c
		case 'x':
			return "", fmt.Errorf("exclusive creation mode is not supported")
		case 'b':
			if binary {
				return "", fmt.Errorf("invalid mode: binary mode specified twice")
			}
			binary = true
		case 't':
			if text {
				return "", fmt.Errorf("invalid mode: text mode specified twice")
			}
			text = true
		case '+':
			return "", fmt.Errorf("update modes ('+') are not yet supported")
		default:
			return "", fmt.Errorf("invalid mode character %q", c)
		}
	}
	if action == 0 {
		return "", fmt.Errorf("must have exactly one of read/write/append mode")
	}
	if binary && text {
		return "", fmt.Errorf("cannot use text and binary mode together")
	}
	result := string(action)
	if binary {
		result += "b"
	}
	return result, nil
}

// Dataclass is the legacy protocol v1 dataclass representation.
//
// Deprecated: protocol v2 returns ClassInstance. Dataclass inputs are rejected.
type Dataclass struct {
	Name       string
	TypeID     uint64
	FieldNames []string
	Attrs      Dict
	Frozen     bool
}

// Repr is Monty's output-only fallback for a value without a host representation.
type Repr string

// Cycle is Monty's output-only marker breaking reference cycles.
type Cycle struct {
	Identity    uint64
	Placeholder string
}

const maxValueDepth = 100

type valueEncoder struct {
	stack map[visit]bool
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func encodeValue(value any) ([]byte, error) {
	e := valueEncoder{stack: make(map[visit]bool)}
	return e.encode(reflect.ValueOf(value), 0)
}

func (e *valueEncoder) encode(v reflect.Value, depth int) ([]byte, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("max input depth exceeded")
	}
	if !v.IsValid() {
		return fieldMessage(2, nil), nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return fieldMessage(2, nil), nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return fieldMessage(2, nil), nil
		}
		key := visit{v.Type(), v.Pointer()}
		if e.stack[key] {
			return nil, fmt.Errorf("cyclic host value of type %s", v.Type())
		}
		e.stack[key] = true
		defer delete(e.stack, key)
	}
	if (v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && !v.IsNil() && v.Len() > 0 {
		key := visit{v.Type(), v.Pointer()}
		if e.stack[key] {
			return nil, fmt.Errorf("cyclic host value of type %s", v.Type())
		}
		e.stack[key] = true
		defer delete(e.stack, key)
	}

	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case Ellipsis:
			return fieldMessage(1, nil), nil
		case NotImplemented:
			return fieldMessage(3, nil), nil
		case Tuple:
			return e.encodeObjectList(12, []Value(x), depth)
		case List:
			return e.encodeObjectList(11, []Value(x), depth)
		case Set:
			return e.encodeObjectList(15, []Value(x), depth)
		case FrozenSet:
			return e.encodeObjectList(16, []Value(x), depth)
		case Dict:
			return e.encodeDict(x, depth)
		case NamedTuple:
			return e.encodeNamedTuple(x, depth)
		case Date:
			return fieldMessage(17, encodeDate(x)), nil
		case Time:
			return fieldMessage(18, encodeTime(x)), nil
		case ClassType, ClassInstance:
			return nil, fmt.Errorf("%T is output-only; host objects are not supported", x)
		case DateTime:
			return fieldMessage(19, encodeDateTime(x)), nil
		case TimeDelta:
			return fieldMessage(20, encodeTimeDelta(x)), nil
		case TimeZone:
			return fieldMessage(21, encodeTimeZone(x)), nil
		case Exception:
			return fieldMessage(22, encodeException(x)), nil
		case Type:
			return fieldMessage(23, append(fieldString(1, string(x)), fieldVarint(3, 1)...)), nil
		case BuiltinFunction:
			return fieldString(26, string(x)), nil
		case Path:
			return fieldString(27, string(x)), nil
		case FileHandle:
			return fieldMessage(28, encodeFileHandle(x)), nil
		case Dataclass:
			return nil, fmt.Errorf("Dataclass inputs are no longer supported by Monty protocol v2; use a Dict or Go struct")
		case Function:
			return fieldMessage(25, encodeFunction(x)), nil
		case InstanceType:
			return nil, fmt.Errorf("InstanceType cannot be used as an execution input")
		case *big.Int:
			if x == nil {
				return fieldMessage(2, nil), nil
			}
			return fieldMessage(6, encodeBigInt(x)), nil
		case big.Int:
			return fieldMessage(6, encodeBigInt(&x)), nil
		case time.Time:
			return fieldMessage(19, encodeGoTime(x)), nil
		case time.Duration:
			return fieldMessage(20, encodeDuration(x)), nil
		}
		if m, ok := v.Interface().(encoding.TextMarshaler); ok {
			text, err := m.MarshalText()
			if err != nil {
				return nil, fmt.Errorf("marshal %s: %w", v.Type(), err)
			}
			return fieldString(8, string(text)), nil
		}
	}

	switch v.Kind() {
	case reflect.Pointer:
		return e.encode(v.Elem(), depth)
	case reflect.Bool:
		return fieldBool(4, v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fieldSInt64(5, v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u <= math.MaxInt64 {
			return fieldSInt64(5, int64(u)), nil
		}
		z := new(big.Int).SetUint64(u)
		return fieldMessage(6, encodeBigInt(z)), nil
	case reflect.Float32, reflect.Float64:
		return fieldFixed64(7, math.Float64bits(v.Convert(reflect.TypeFor[float64]()).Float())), nil
	case reflect.String:
		return fieldString(8, v.String()), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return fieldBytes(9, nil), nil
			}
			return fieldBytes(9, v.Bytes()), nil
		}
		return e.encodeReflectList(11, v, depth)
	case reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			reflect.Copy(reflect.ValueOf(b), v)
			return fieldBytes(9, b), nil
		}
		return e.encodeReflectList(11, v, depth)
	case reflect.Map:
		return e.encodeMap(v, depth)
	case reflect.Struct:
		return e.encodeStruct(v, depth)
	default:
		return nil, fmt.Errorf("unsupported host value type %s", v.Type())
	}
}

func (e *valueEncoder) encodeObjectList(tag int, values []Value, depth int) ([]byte, error) {
	body := []byte{}
	for _, item := range values {
		encoded, err := e.encode(reflect.ValueOf(item), depth+1)
		if err != nil {
			return nil, err
		}
		body = append(body, fieldMessage(1, encoded)...)
	}
	return fieldMessage(tag, body), nil
}

func (e *valueEncoder) encodeReflectList(tag int, v reflect.Value, depth int) ([]byte, error) {
	body := []byte{}
	for i := range v.Len() {
		encoded, err := e.encode(v.Index(i), depth+1)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		body = append(body, fieldMessage(1, encoded)...)
	}
	return fieldMessage(tag, body), nil
}

func (e *valueEncoder) encodeDict(dict Dict, depth int) ([]byte, error) {
	body := []byte{}
	for i, pair := range dict {
		key, err := e.encode(reflect.ValueOf(pair.Key), depth+1)
		if err != nil {
			return nil, fmt.Errorf("dict key %d: %w", i, err)
		}
		value, err := e.encode(reflect.ValueOf(pair.Value), depth+1)
		if err != nil {
			return nil, fmt.Errorf("dict value %d: %w", i, err)
		}
		body = append(body, fieldMessage(1, append(fieldMessage(1, key), fieldMessage(2, value)...))...)
	}
	return fieldMessage(14, body), nil
}

func (e *valueEncoder) encodeMap(v reflect.Value, depth int) ([]byte, error) {
	if v.IsNil() {
		return e.encodeDict(nil, depth)
	}
	keys := v.MapKeys()
	if v.Type().Key().Kind() == reflect.String {
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	}
	dict := make(Dict, 0, len(keys))
	for _, key := range keys {
		dict = append(dict, Pair{key.Interface(), v.MapIndex(key).Interface()})
	}
	return e.encodeDict(dict, depth)
}

func (e *valueEncoder) encodeStruct(v reflect.Value, depth int) ([]byte, error) {
	t := v.Type()
	dict := make(Dict, 0, v.NumField())
	for i := range v.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("monty")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.Split(f.Tag.Get("json"), ",")[0]
		}
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		dict = append(dict, Pair{name, v.Field(i).Interface()})
	}
	return e.encodeDict(dict, depth)
}

func (e *valueEncoder) encodeNamedTuple(x NamedTuple, depth int) ([]byte, error) {
	body := fieldString(1, x.TypeName)
	for _, name := range x.FieldNames {
		body = append(body, fieldString(2, name)...)
	}
	for i, value := range x.Values {
		encoded, err := e.encode(reflect.ValueOf(value), depth+1)
		if err != nil {
			return nil, fmt.Errorf("named tuple value %d: %w", i, err)
		}
		body = append(body, fieldMessage(3, encoded)...)
	}
	return fieldMessage(13, body), nil
}

func encodeBigInt(x *big.Int) []byte {
	negative := x.Sign() < 0
	magnitude := new(big.Int).Abs(x).Bytes()
	return append(fieldBool(1, negative), fieldBytes(2, magnitude)...)
}

func encodeDate(x Date) []byte {
	return append(append(fieldVarint(1, uint64(int64(x.Year))), fieldVarint(2, uint64(x.Month))...), fieldVarint(3, uint64(x.Day))...)
}
func encodeDateTime(x DateTime) []byte {
	b := encodeDate(Date{x.Year, x.Month, x.Day})
	b = append(b, fieldVarint(4, uint64(x.Hour))...)
	b = append(b, fieldVarint(5, uint64(x.Minute))...)
	b = append(b, fieldVarint(6, uint64(x.Second))...)
	b = append(b, fieldVarint(7, uint64(x.Microsecond))...)
	if x.OffsetSeconds != nil {
		b = append(b, fieldVarint(8, uint64(int64(*x.OffsetSeconds)))...)
	}
	if x.TimezoneName != nil {
		b = append(b, fieldString(9, *x.TimezoneName)...)
	}
	return b
}
func encodeGoTime(x time.Time) []byte {
	_, offset := x.Zone()
	name, _ := x.Zone()
	return encodeDateTime(DateTime{Year: x.Year(), Month: x.Month(), Day: x.Day(), Hour: x.Hour(), Minute: x.Minute(), Second: x.Second(), Microsecond: x.Nanosecond() / 1000, OffsetSeconds: &offset, TimezoneName: &name})
}
func encodeTimeDelta(x TimeDelta) []byte {
	return append(append(fieldVarint(1, uint64(int64(x.Days))), fieldVarint(2, uint64(int64(x.Seconds)))...), fieldVarint(3, uint64(int64(x.Microseconds)))...)
}
func encodeDuration(x time.Duration) []byte {
	micros := x.Microseconds()
	days := micros / (24 * 60 * 60 * 1_000_000)
	micros -= days * (24 * 60 * 60 * 1_000_000)
	if micros < 0 {
		days--
		micros += 24 * 60 * 60 * 1_000_000
	}
	return encodeTimeDelta(TimeDelta{int(days), int(micros / 1_000_000), int(micros % 1_000_000)})
}
func encodeTimeZone(x TimeZone) []byte {
	b := fieldVarint(1, uint64(int64(x.OffsetSeconds)))
	if x.Name != nil {
		b = append(b, fieldString(2, *x.Name)...)
	}
	return b
}
func encodeException(x Exception) []byte {
	b := fieldString(1, x.Type)
	if x.Message != "" {
		b = append(b, fieldString(2, x.Message)...)
	}
	return b
}
func encodeFileHandle(x FileHandle) []byte {
	return append(append(fieldString(1, x.Path), fieldString(2, x.Mode)...), fieldVarint(3, x.Position)...)
}
func encodeFunction(x Function) []byte {
	b := fieldString(1, x.Name)
	if x.Docstring != nil {
		b = append(b, fieldString(2, *x.Docstring)...)
	}
	return b
}
