package monty

import (
	"fmt"
	"google.golang.org/protobuf/encoding/protowire"
)

// arenaEncoder writes post-order nodes in one arena per message.
// The existing reflection codec normalizes Go inputs before arena construction.
type arenaEncoder struct{ nodes [][]byte }

func (a *arenaEncoder) add(value Value) (uint32, error) {
	b, err := encodeValue(value)
	if err != nil {
		return 0, err
	}
	return a.addLegacy(b)
}

var legacyNodeTags = map[int]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 6, 7: 7, 8: 8, 9: 9, 11: 11, 12: 12, 13: 13, 14: 14, 15: 15, 16: 16, 17: 17, 18: 18, 19: 19, 20: 20, 21: 21, 22: 22, 23: 23, 25: 25, 26: 26, 27: 27, 28: 28, 29: 29, 30: 30}

func (a *arenaEncoder) addLegacy(b []byte) (uint32, error) {
	var node []byte
	err := parseFields(b, func(f wireField) error {
		tag, ok := legacyNodeTags[f.tag]
		if !ok {
			return fmt.Errorf("value kind %d cannot be supplied to Monty 1.0", f.tag)
		}
		body := f.bytes
		switch f.tag {
		case 11, 12, 15, 16:
			body = nil
			if err := parseFields(f.bytes, func(c wireField) error {
				if c.tag != 1 {
					return nil
				}
				i, err := a.addLegacy(c.bytes)
				if err == nil {
					body = append(body, fieldVarint(1, uint64(i))...)
				}
				return err
			}); err != nil {
				return err
			}
		case 14:
			body = nil
			if err := parseFields(f.bytes, func(c wireField) error {
				if c.tag != 1 {
					return nil
				}
				pair := []byte{}
				err := parseFields(c.bytes, func(p wireField) error {
					i, err := a.addLegacy(p.bytes)
					if err == nil {
						pair = append(pair, fieldVarint(p.tag, uint64(i))...)
					}
					return err
				})
				body = append(body, fieldMessage(1, pair)...)
				return err
			}); err != nil {
				return err
			}
		case 13:
			body = nil
			if err := parseFields(f.bytes, func(c wireField) error {
				if c.tag == 3 {
					i, err := a.addLegacy(c.bytes)
					if err != nil {
						return err
					}
					body = append(body, fieldVarint(3, uint64(i))...)
				} else {
					body = append(body, fieldBytes(c.tag, c.bytes)...)
				}
				return nil
			}); err != nil {
				return err
			}

		}
		switch f.type_ {
		case protowire.VarintType:
			node = fieldVarint(tag, f.varint)
		case protowire.Fixed64Type:
			node = fieldFixed64(tag, f.fixed64)
		default:
			node = fieldMessage(tag, body)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	i := uint32(len(a.nodes))
	a.nodes = append(a.nodes, node)
	return i, nil
}
func (a *arenaEncoder) encode() []byte {
	b := fieldVarint(1, uint64(len(a.nodes)))
	for _, n := range a.nodes {
		b = append(b, fieldMessage(2, n)...)
	}
	return b
}
func arenaRef(values []Value, i uint64) (Value, error) {
	if i >= uint64(len(values)) {
		return nil, fmt.Errorf("arena reference %d outside %d nodes", i, len(values))
	}
	return values[i], nil
}
func indexes(data []byte, tag int) ([]uint64, error) {
	var out []uint64
	err := parseFields(data, func(f wireField) error {
		if f.tag != tag {
			return nil
		}
		if f.type_ == protowire.VarintType {
			out = append(out, f.varint)
			return nil
		}
		if f.type_ != protowire.BytesType {
			return fmt.Errorf("invalid index wire type")
		}
		b := f.bytes
		for len(b) > 0 {
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			out = append(out, v)
			b = b[n:]
		}
		return nil
	})
	return out, err
}
func arenaPairs(data []byte, values []Value) (Dict, error) {
	d := Dict{}
	err := parseFields(data, func(f wireField) error {
		if f.tag != 1 {
			return nil
		}
		var k, v uint64
		if err := parseFields(f.bytes, func(p wireField) error {
			if p.tag == 1 {
				k = p.varint
			}
			if p.tag == 2 {
				v = p.varint
			}
			return nil
		}); err != nil {
			return err
		}
		key, err := arenaRef(values, k)
		if err != nil {
			return err
		}
		val, err := arenaRef(values, v)
		if err != nil {
			return err
		}
		d = append(d, Pair{key, val})
		return nil
	})
	return d, err
}
func decodeArena(data []byte) ([]Value, error) {
	values := []Value{}
	err := parseFields(data, func(f wireField) error {
		if f.tag != 2 {
			return nil
		}
		if f.type_ != protowire.BytesType {
			return fmt.Errorf("arena node must be a message")
		}
		var value Value
		count := 0
		err := parseFields(f.bytes, func(n wireField) error {
			count++
			if count > 1 {
				return fmt.Errorf("arena node has multiple kinds")
			}
			expected := protowire.BytesType
			switch n.tag {
			case 4, 5:
				expected = protowire.VarintType
			case 7:
				expected = protowire.Fixed64Type
			}
			if n.type_ != expected {
				return fmt.Errorf("arena node kind %d has wire type %d, want %d", n.tag, n.type_, expected)
			}
			switch n.tag {
			case 11, 12, 15, 16:
				ids, err := indexes(n.bytes, 1)
				if err != nil {
					return err
				}
				items := []Value{}
				for _, i := range ids {
					v, err := arenaRef(values, i)
					if err != nil {
						return err
					}
					items = append(items, v)
				}
				switch n.tag {
				case 11:
					value = List(items)
				case 12:
					value = Tuple(items)
				case 15:
					value = Set(items)
				case 16:
					value = FrozenSet(items)
				}
			case 14:
				d, err := arenaPairs(n.bytes, values)
				if err != nil {
					return err
				}
				value = d
			case 13:
				x := NamedTuple{TypeName: decodeStringField(n.bytes, 1)}
				if err := parseFields(n.bytes, func(p wireField) error {
					if p.tag == 2 {
						x.FieldNames = append(x.FieldNames, string(p.bytes))
					}
					return nil
				}); err != nil {
					return err
				}
				ids, err := indexes(n.bytes, 3)
				if err != nil {
					return err
				}
				for _, i := range ids {
					v, err := arenaRef(values, i)
					if err != nil {
						return err
					}
					x.Values = append(x.Values, v)
				}
				value = x
			case 18:
				var x Time
				if err := parseFields(n.bytes, func(p wireField) error {
					switch p.tag {
					case 1:
						x.Hour = int(p.varint)
					case 2:
						x.Minute = int(p.varint)
					case 3:
						x.Second = int(p.varint)
					case 4:
						x.Microsecond = int(p.varint)
					case 5:
						v := int(int32(p.varint))
						x.OffsetSeconds = &v
					case 6:
						v := string(p.bytes)
						x.TimezoneName = &v
					case 7:
						x.Fold = int(p.varint)
					}
					return nil
				}); err != nil {
					return err
				}
				value = x
			case 23:
				x := ClassType{Name: decodeStringField(n.bytes, 1), Attrs: Dict{}}
				hasID := false
				if err := parseFields(n.bytes, func(p wireField) error {
					switch p.tag {
					case 2:
						hasID = true
						id, err := decodeUUID(p.bytes)
						if err != nil {
							return err
						}
						x.ID = id
					case 3:
						x.Origin = TypeOrigin(p.varint)
					case 4:
						x.IsDataclass = p.varint != 0
					case 5:
						d, err := arenaPairs(p.bytes, values)
						if err != nil {
							return err
						}
						x.Attrs = d
					}
					return nil
				}); err != nil {
					return err
				}
				if x.Origin < TypeOriginBuiltin || x.Origin > TypeOriginHost {
					return fmt.Errorf("invalid type origin %d", x.Origin)
				}
				if hasID != (x.Origin != TypeOriginBuiltin) {
					return fmt.Errorf("invalid class identity")
				}
				if x.Origin == TypeOriginBuiltin {
					value = Type(x.Name)
				} else {
					value = x
				}
			case 24:
				id, err := decodeUUID(decodeBytesField(n.bytes, 2))
				if err != nil {
					return err
				}
				x := ClassInstance{ID: id, Attrs: Dict{}}
				var i uint64
				if err := parseFields(n.bytes, func(p wireField) error {
					if p.tag == 1 {
						i = p.varint
					}
					if p.tag == 3 {
						d, err := arenaPairs(p.bytes, values)
						if err != nil {
							return err
						}
						x.Attrs = d
					}
					return nil
				}); err != nil {
					return err
				}
				c, err := arenaRef(values, i)
				if err != nil {
					return err
				}
				ct, ok := c.(ClassType)
				if !ok {
					return fmt.Errorf("class instance references %T", c)
				}
				x.Type = ct
				value = x
			case 30:
				value = Cycle{Placeholder: string(n.bytes)}
			default:
				old := 0
				for k, v := range legacyNodeTags {
					if v == n.tag {
						old = k
						break
					}
				}
				if old == 0 {
					return fmt.Errorf("unsupported Monty node kind %d", n.tag)
				}
				var b []byte
				switch n.type_ {
				case protowire.VarintType:
					b = fieldVarint(old, n.varint)
				case protowire.Fixed64Type:
					b = fieldFixed64(old, n.fixed64)
				default:
					b = fieldMessage(old, n.bytes)
				}
				v, err := decodeValue(b)
				if err != nil {
					return err
				}
				value = v
			}
			return nil
		})
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("arena node has no kind")
		}
		values = append(values, value)
		return nil
	})
	return values, err
}
func messageArena(data []byte, tag int) ([]Value, error) {
	return decodeArena(decodeBytesField(data, tag))
}
