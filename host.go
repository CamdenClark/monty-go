package monty

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"runtime/debug"
	"strings"
)

// ExternalLookup maps unresolved Monty names to host values or Go functions.
// Functions may use ordinary typed parameters and return T, error, or (T,error).
type ExternalLookup map[string]any

// Kwargs is the optional final parameter a reflected Go host function can use
// to receive Python keyword arguments.
type Kwargs map[string]Value

// HostFunc is the fully dynamic host-call form.
type HostFunc func(context.Context, []Value, Kwargs) (Value, error)

// AsyncFunction marks a host function whose work should run concurrently and
// whose result Monty receives through its external-future interface. Sandbox
// code calls it with await.
type AsyncFunction struct{ Function any }

// Async marks a reflected Go function or HostFunc as asynchronous.
func Async(fn any) AsyncFunction { return AsyncFunction{Function: fn} }

// OSHandler handles virtual filesystem, environment, and clock operations.
// Return NotHandled to ask Monty to raise its operation-specific default error.
type OSHandler func(context.Context, string, []Value, Kwargs) (Value, error)

type notHandled struct{}

// NotHandled is returned from an OSHandler when it does not handle a call.
var NotHandled Value = notHandled{}

// Decode recursively converts a value returned by Monty into T. Python
// dictionaries, dataclasses, and named tuples convert to Go structs or maps,
// and Python sequences convert to slices or arrays. Struct fields use the
// monty tag, then the json tag, then the Go field name. Missing fields retain
// their zero values and unknown keys are ignored.
func Decode[T any](value Value) (T, error) {
	var zero T
	target := reflect.TypeFor[T]()
	decoded, err := convertHostArg(value, target)
	if err != nil {
		return zero, fmt.Errorf("decode Monty value as %s: %w", target, err)
	}
	result := decoded.Interface()
	if result == nil {
		return zero, nil
	}
	return result.(T), nil
}

var (
	contextType = reflect.TypeFor[context.Context]()
	errorType   = reflect.TypeFor[error]()
	kwargsType  = reflect.TypeFor[Kwargs]()
	dictType    = reflect.TypeFor[Dict]()
	bigIntType  = reflect.TypeFor[big.Int]()
)

func isHostFunction(value any) bool {
	if value == nil {
		return false
	}
	if _, ok := value.(AsyncFunction); ok {
		return true
	}
	if _, ok := value.(HostFunc); ok {
		return true
	}
	return reflect.TypeOf(value).Kind() == reflect.Func
}

func callHost(ctx context.Context, fn any, args []Value, kwargs Dict) (out Value, err error) {
	if async, ok := fn.(AsyncFunction); ok {
		fn = async.Function
	}
	kw, err := kwargsMap(kwargs)
	if err != nil {
		return nil, err
	}
	if dynamic, ok := fn.(HostFunc); ok {
		return dynamic(ctx, args, kw)
	}
	v := reflect.ValueOf(fn)
	if !v.IsValid() || v.Kind() != reflect.Func {
		return nil, fmt.Errorf("host value %T is not callable", fn)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in host function: %v\n%s", p, debug.Stack())
		}
	}()
	t := v.Type()
	in := make([]reflect.Value, 0, t.NumIn())
	argIndex := 0
	paramIndex := 0
	if t.NumIn() > 0 && t.In(0).Implements(contextType) {
		in = append(in, reflect.ValueOf(ctx))
		paramIndex++
	}
	acceptsKw := t.NumIn() > paramIndex && t.In(t.NumIn()-1) == kwargsType
	positionalEnd := t.NumIn()
	if acceptsKw {
		positionalEnd--
	}
	for paramIndex < positionalEnd {
		paramType := t.In(paramIndex)
		if t.IsVariadic() && paramIndex == t.NumIn()-1 {
			paramType = paramType.Elem()
			for argIndex < len(args) {
				converted, e := convertHostArg(args[argIndex], paramType)
				if e != nil {
					return nil, fmt.Errorf("argument %d: %w", argIndex, e)
				}
				in = append(in, converted)
				argIndex++
			}
			paramIndex++
			break
		}
		if argIndex >= len(args) {
			return nil, fmt.Errorf("host function expects %d positional arguments, got %d", positionalEnd, len(args))
		}
		converted, e := convertHostArg(args[argIndex], paramType)
		if e != nil {
			return nil, fmt.Errorf("argument %d: %w", argIndex, e)
		}
		in = append(in, converted)
		argIndex++
		paramIndex++
	}
	if argIndex != len(args) {
		return nil, fmt.Errorf("host function expects %d positional arguments, got %d", argIndex, len(args))
	}
	if acceptsKw {
		in = append(in, reflect.ValueOf(kw))
	} else if len(kw) > 0 {
		return nil, fmt.Errorf("host function does not accept keyword arguments")
	}
	values := v.Call(in)
	switch len(values) {
	case 0:
		return nil, nil
	case 1:
		if t.Out(0).Implements(errorType) {
			if values[0].IsNil() {
				return nil, nil
			}
			return nil, values[0].Interface().(error)
		}
		return values[0].Interface(), nil
	case 2:
		if !t.Out(1).Implements(errorType) {
			return nil, fmt.Errorf("host function second return must implement error")
		}
		if !values[1].IsNil() {
			return nil, values[1].Interface().(error)
		}
		return values[0].Interface(), nil
	default:
		return nil, fmt.Errorf("host function must return at most (value, error)")
	}
}

func kwargsMap(dict Dict) (Kwargs, error) {
	result := make(Kwargs, len(dict))
	for _, p := range dict {
		k, ok := p.Key.(string)
		if !ok {
			return nil, fmt.Errorf("keyword name has type %T", p.Key)
		}
		result[k] = p.Value
	}
	return result, nil
}

func convertHostArg(value Value, target reflect.Type) (reflect.Value, error) {
	if value == nil {
		switch target.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func:
			return reflect.Zero(target), nil
		}
		return reflect.Value{}, fmt.Errorf("None cannot convert to %s", target)
	}
	v := reflect.ValueOf(value)
	if v.Type().AssignableTo(target) {
		return v, nil
	}
	if n, ok := numericTo(value, target); ok {
		return n, nil
	}
	if (isSigned(v.Kind()) || isUnsigned(v.Kind()) || isFloat(v.Kind())) &&
		(isSigned(target.Kind()) || isUnsigned(target.Kind()) || isFloat(target.Kind())) {
		return reflect.Value{}, fmt.Errorf("%v overflows %s", value, target)
	}
	if v.Type().ConvertibleTo(target) && safeDirectConversion(v, target) {
		return v.Convert(target), nil
	}
	if target.Kind() == reflect.Interface {
		if v.Type().Implements(target) {
			return v, nil
		}
		if target.NumMethod() == 0 {
			return v, nil
		}
		return reflect.Value{}, fmt.Errorf("%T does not implement %s", value, target)
	}
	if target.Kind() == reflect.Pointer {
		converted, err := convertHostArg(value, target.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		p := reflect.New(target.Elem())
		p.Elem().Set(converted)
		return p, nil
	}
	if b, ok := value.([]byte); ok && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) && target.Elem().Kind() == reflect.Uint8 {
		switch target.Kind() {
		case reflect.Slice:
			out := reflect.MakeSlice(target, len(b), len(b))
			reflect.Copy(out, reflect.ValueOf(b))
			return out, nil
		case reflect.Array:
			if len(b) != target.Len() {
				return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
			}
			out := reflect.New(target).Elem()
			reflect.Copy(out, reflect.ValueOf(b))
			return out, nil
		}
	}
	switch target.Kind() {
	case reflect.Slice:
		items, ok := sequenceValues(value)
		if !ok {
			return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
		}
		out := reflect.MakeSlice(target, len(items), len(items))
		for i, item := range items {
			x, err := convertHostArg(item, target.Elem())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("index %d: %w", i, err)
			}
			out.Index(i).Set(x)
		}
		return out, nil
	case reflect.Array:
		items, ok := sequenceValues(value)
		if !ok || len(items) != target.Len() {
			return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
		}
		out := reflect.New(target).Elem()
		for i, item := range items {
			x, err := convertHostArg(item, target.Elem())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("index %d: %w", i, err)
			}
			out.Index(i).Set(x)
		}
		return out, nil
	case reflect.Map:
		d, ok := resultDict(value)
		if !ok {
			return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
		}
		out := reflect.MakeMapWithSize(target, len(d))
		for _, p := range d {
			k, e := convertHostArg(p.Key, target.Key())
			if e != nil {
				return reflect.Value{}, fmt.Errorf("map key: %w", e)
			}
			if !k.Comparable() {
				return reflect.Value{}, fmt.Errorf("map key of type %s is not comparable", k.Type())
			}
			x, e := convertHostArg(p.Value, target.Elem())
			if e != nil {
				return reflect.Value{}, fmt.Errorf("map value: %w", e)
			}
			out.SetMapIndex(k, x)
		}
		return out, nil
	case reflect.Struct:
		d, ok := resultDict(value)
		if !ok {
			return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
		}
		m, e := DictMap(d)
		if e != nil {
			return reflect.Value{}, e
		}
		out := reflect.New(target).Elem()
		for i := range target.NumField() {
			f := target.Field(i)
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
			raw, exists := m[name]
			if !exists {
				continue
			}
			x, e := convertHostArg(raw, f.Type)
			if e != nil {
				return reflect.Value{}, fmt.Errorf("field %s: %w", f.Name, e)
			}
			out.Field(i).Set(x)
		}
		return out, nil
	}
	return reflect.Value{}, fmt.Errorf("%T cannot convert to %s", value, target)
}

func resultDict(value Value) (Dict, bool) {
	switch x := value.(type) {
	case Dict:
		return x, true
	case ClassInstance:
		return x.Attrs, true
	case Dataclass:
		return x.Attrs, true
	case NamedTuple:
		if len(x.FieldNames) != len(x.Values) {
			return nil, false
		}
		dict := make(Dict, len(x.Values))
		for i, item := range x.Values {
			dict[i] = Pair{Key: x.FieldNames[i], Value: item}
		}
		return dict, true
	default:
		return nil, false
	}
}

func safeDirectConversion(v reflect.Value, target reflect.Type) bool {
	a, b := v.Kind(), target.Kind()
	return (isSigned(a) && isSigned(b)) || (isUnsigned(a) && isUnsigned(b)) || (isFloat(a) && isFloat(b)) ||
		(a == reflect.Bool && b == reflect.Bool) || (a == reflect.String && b == reflect.String)
}
func isSigned(k reflect.Kind) bool   { return k >= reflect.Int && k <= reflect.Int64 }
func isUnsigned(k reflect.Kind) bool { return k >= reflect.Uint && k <= reflect.Uintptr }
func isFloat(k reflect.Kind) bool    { return k == reflect.Float32 || k == reflect.Float64 }
func sequenceValues(v Value) ([]Value, bool) {
	switch x := v.(type) {
	case List:
		return []Value(x), true
	case Tuple:
		return []Value(x), true
	case Set:
		return []Value(x), true
	case FrozenSet:
		return []Value(x), true
	default:
		return nil, false
	}
}
func numericTo(value Value, target reflect.Type) (reflect.Value, bool) {
	var z *big.Int
	switch x := value.(type) {
	case int64:
		z = big.NewInt(x)
	case *big.Int:
		if x == nil {
			return reflect.Value{}, false
		}
		z = x
	case float64:
		if target.Kind() != reflect.Float32 && target.Kind() != reflect.Float64 {
			return reflect.Value{}, false
		}
		out := reflect.New(target).Elem()
		if !math.IsInf(x, 0) && out.OverflowFloat(x) {
			return reflect.Value{}, false
		}
		out.SetFloat(x)
		return out, true
	default:
		return reflect.Value{}, false
	}
	out := reflect.New(target).Elem()
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !z.IsInt64() {
			return reflect.Value{}, false
		}
		n := z.Int64()
		if out.OverflowInt(n) {
			return reflect.Value{}, false
		}
		out.SetInt(n)
		return out, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if z.Sign() < 0 || !z.IsUint64() {
			return reflect.Value{}, false
		}
		n := z.Uint64()
		if out.OverflowUint(n) {
			return reflect.Value{}, false
		}
		out.SetUint(n)
		return out, true
	case reflect.Float32, reflect.Float64:
		f, _ := new(big.Float).SetInt(z).Float64()
		if math.IsInf(f, 0) || out.OverflowFloat(f) {
			return reflect.Value{}, false
		}
		out.SetFloat(f)
		return out, true
	}
	if target == bigIntType {
		out.Set(reflect.ValueOf(*new(big.Int).Set(z)))
		return out, true
	}
	return reflect.Value{}, false
}
