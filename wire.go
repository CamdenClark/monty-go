package monty

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/big"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	protocolVersion = 2
	maxFrameLen     = 256 << 20
)

func fieldVarint(tag int, value uint64) []byte {
	b := protowire.AppendTag(nil, protowire.Number(tag), protowire.VarintType)
	return protowire.AppendVarint(b, value)
}

func fieldSInt64(tag int, value int64) []byte { return fieldVarint(tag, protowire.EncodeZigZag(value)) }
func fieldBool(tag int, value bool) []byte {
	if value {
		return fieldVarint(tag, 1)
	}
	return fieldVarint(tag, 0)
}
func fieldFixed64(tag int, value uint64) []byte {
	b := protowire.AppendTag(nil, protowire.Number(tag), protowire.Fixed64Type)
	return protowire.AppendFixed64(b, value)
}
func fieldBytes(tag int, value []byte) []byte {
	b := protowire.AppendTag(nil, protowire.Number(tag), protowire.BytesType)
	return protowire.AppendBytes(b, value)
}
func fieldString(tag int, value string) []byte { return fieldBytes(tag, []byte(value)) }
func fieldMessage(tag int, body []byte) []byte { return fieldBytes(tag, body) }

type wireField struct {
	tag     int
	type_   protowire.Type
	varint  uint64
	fixed64 uint64
	bytes   []byte
}

func parseFields(data []byte, fn func(wireField) error) error {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("invalid protobuf tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		f := wireField{tag: int(num), type_: typ}
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			f.varint, data = v, data[n:]
		case protowire.Fixed64Type:
			v, n := protowire.ConsumeFixed64(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			f.fixed64, data = v, data[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			f.bytes, data = v, data[n:]
		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		case protowire.StartGroupType:
			_, n := protowire.ConsumeGroup(num, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		default:
			return fmt.Errorf("invalid protobuf wire type %d", typ)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

func consumeSingleField(data []byte) (int, []byte, error) {
	var tag int
	var body []byte
	count := 0
	err := parseFields(data, func(f wireField) error { count++; tag, body = f.tag, f.bytes; return nil })
	if err != nil {
		return 0, nil, err
	}
	if count != 1 {
		return 0, nil, fmt.Errorf("expected one field, got %d", count)
	}
	return tag, body, nil
}

func writeFrame(w io.Writer, body []byte) error {
	if len(body) > maxFrameLen {
		return fmt.Errorf("frame of %d bytes exceeds maximum of %d", len(body), maxFrameLen)
	}
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(body)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(prefix[:])
	if size > maxFrameLen {
		return nil, fmt.Errorf("frame of %d bytes exceeds maximum of %d", size, maxFrameLen)
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("truncated frame: %w", err)
	}
	return body, nil
}

func decodeValue(data []byte) (Value, error) {
	var result Value
	seen := false
	err := parseFields(data, func(f wireField) error {
		if seen {
			return fmt.Errorf("MontyObject has multiple kind fields")
		}
		seen = true
		switch f.tag {
		case 1:
			result = Ellipsis{}
		case 2:
			result = nil
		case 4:
			result = f.varint != 0
		case 5:
			result = protowire.DecodeZigZag(f.varint)
		case 6:
			var negative bool
			magnitude := []byte{}
			if err := parseFields(f.bytes, func(x wireField) error {
				if x.tag == 1 {
					negative = x.varint != 0
				}
				if x.tag == 2 {
					magnitude = x.bytes
				}
				return nil
			}); err != nil {
				return err
			}
			z := new(big.Int).SetBytes(magnitude)
			if negative {
				z.Neg(z)
			}
			result = z
		case 7:
			result = math.Float64frombits(f.fixed64)
		case 8:
			result = string(f.bytes)
		case 9:
			result = append([]byte(nil), f.bytes...)
		case 11, 12, 15, 16:
			items, err := decodeObjectList(f.bytes)
			if err != nil {
				return err
			}
			switch f.tag {
			case 11:
				result = List(items)
			case 12:
				result = Tuple(items)
			case 15:
				result = Set(items)
			case 16:
				result = FrozenSet(items)
			}
		case 13:
			x, err := decodeNamedTuple(f.bytes)
			if err != nil {
				return err
			}
			result = x
		case 14:
			x, err := decodeDict(f.bytes)
			if err != nil {
				return err
			}
			result = x
		case 17:
			result = decodeDate(f.bytes)
		case 18:
			result = decodeTime(f.bytes)
		case 19:
			result = decodeDateTime(f.bytes)
		case 20:
			result = decodeTimeDelta(f.bytes)
		case 21:
			result = decodeTimeZone(f.bytes)
		case 22:
			result = decodeExceptionValue(f.bytes)
		case 23:
			x, err := decodeClassType(f.bytes)
			if err != nil {
				return err
			}
			if x.Origin == TypeOriginBuiltin {
				result = Type(x.Name)
			} else {
				result = x
			}
		case 26:
			result = BuiltinFunction(string(f.bytes))
		case 27:
			result = Path(string(f.bytes))
		case 28:
			result = decodeFileHandle(f.bytes)
		case 24:
			x, err := decodeClassInstance(f.bytes)
			if err != nil {
				return err
			}
			result = x
		case 25:
			result = decodeFunction(f.bytes)
		case 29:
			result = Repr(string(f.bytes))
		case 30:
			var x Cycle
			_ = parseFields(f.bytes, func(v wireField) error {
				if v.tag == 1 {
					x.Identity = v.varint
				}
				if v.tag == 2 {
					x.Placeholder = string(v.bytes)
				}
				return nil
			})
			result = x
		case 3:
			result = NotImplemented{}
		default:
			return fmt.Errorf("unknown MontyObject kind tag %d", f.tag)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !seen {
		return nil, fmt.Errorf("MontyObject has no kind")
	}
	return result, nil
}

func decodeObjectList(data []byte) ([]Value, error) {
	items := []Value{}
	err := parseFields(data, func(f wireField) error {
		if f.tag != 1 {
			return nil
		}
		v, err := decodeValue(f.bytes)
		if err != nil {
			return err
		}
		items = append(items, v)
		return nil
	})
	return items, err
}

func decodeDict(data []byte) (Dict, error) {
	result := Dict{}
	err := parseFields(data, func(f wireField) error {
		if f.tag != 1 {
			return nil
		}
		var pair Pair
		if err := parseFields(f.bytes, func(p wireField) error {
			v, err := decodeValue(p.bytes)
			if err != nil {
				return err
			}
			if p.tag == 1 {
				pair.Key = v
			} else if p.tag == 2 {
				pair.Value = v
			}
			return nil
		}); err != nil {
			return err
		}
		result = append(result, pair)
		return nil
	})
	return result, err
}

func decodeNamedTuple(data []byte) (NamedTuple, error) {
	var x NamedTuple
	err := parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.TypeName = string(f.bytes)
		case 2:
			x.FieldNames = append(x.FieldNames, string(f.bytes))
		case 3:
			v, err := decodeValue(f.bytes)
			if err != nil {
				return err
			}
			x.Values = append(x.Values, v)
		}
		return nil
	})
	return x, err
}

func decodeDate(data []byte) Date {
	var x Date
	_ = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Year = int(int32(f.varint))
		case 2:
			x.Month = timeMonth(f.varint)
		case 3:
			x.Day = int(f.varint)
		}
		return nil
	})
	return x
}

func timeMonth(v uint64) time.Month { return time.Month(v) }

func decodeDateTime(data []byte) DateTime {
	var x DateTime
	_ = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Year = int(int32(f.varint))
		case 2:
			x.Month = timeMonth(f.varint)
		case 3:
			x.Day = int(f.varint)
		case 4:
			x.Hour = int(f.varint)
		case 5:
			x.Minute = int(f.varint)
		case 6:
			x.Second = int(f.varint)
		case 7:
			x.Microsecond = int(f.varint)
		case 8:
			v := int(int32(f.varint))
			x.OffsetSeconds = &v
		case 9:
			v := string(f.bytes)
			x.TimezoneName = &v
		}
		return nil
	})
	return x
}
func decodeTimeDelta(data []byte) TimeDelta {
	var x TimeDelta
	_ = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Days = int(int32(f.varint))
		case 2:
			x.Seconds = int(int32(f.varint))
		case 3:
			x.Microseconds = int(int32(f.varint))
		}
		return nil
	})
	return x
}
func decodeTimeZone(data []byte) TimeZone {
	var x TimeZone
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			x.OffsetSeconds = int(int32(f.varint))
		}
		if f.tag == 2 {
			v := string(f.bytes)
			x.Name = &v
		}
		return nil
	})
	return x
}
func decodeExceptionValue(data []byte) Exception {
	var x Exception
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			x.Type = string(f.bytes)
		}
		if f.tag == 2 {
			x.Message = string(f.bytes)
		}
		return nil
	})
	return x
}
func decodeFileHandle(data []byte) FileHandle {
	var x FileHandle
	_ = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Path = string(f.bytes)
		case 2:
			x.Mode = string(f.bytes)
		case 3:
			x.Position = f.varint
		}
		return nil
	})
	return x
}
func decodeFunction(data []byte) Function {
	var x Function
	_ = parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			x.Name = string(f.bytes)
		}
		if f.tag == 2 {
			v := string(f.bytes)
			x.Docstring = &v
		}
		return nil
	})
	return x
}

// DictMap converts a Dict with string keys to a Go map.
func DictMap(dict Dict) (map[string]Value, error) {
	result := make(map[string]Value, len(dict))
	for _, pair := range dict {
		key, ok := pair.Key.(string)
		if !ok {
			return nil, fmt.Errorf("dictionary key has type %T, not string", pair.Key)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate dictionary key %q", key)
		}
		result[key] = pair.Value
	}
	return result, nil
}
