package monty

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// ExceptionData is a structured exception payload. It is either
// *UnicodeErrorData or *JSONErrorData; most exceptions have no payload.
type ExceptionData interface{ isExceptionData() }

// Match the worker's size cap for JSON documents and Unicode objects.
const maxExceptionDataFieldSize = 64 << 10

// UnicodeErrorData preserves UnicodeDecodeError/UnicodeEncodeError attributes.
// ObjectBytes is used for decoding errors; ObjectString for encoding errors.
// Set only one object representation. Start/End use bytes for decoding errors
// and character indexes for encoding errors, with End exclusive.
type UnicodeErrorData struct {
	Encoding     string
	ObjectBytes  []byte
	ObjectString *string
	Start, End   uint64
	Reason       string
}

func (*UnicodeErrorData) isExceptionData() {}

// JSONErrorData preserves json.JSONDecodeError attributes. Document is absent
// if the worker omitted it because of its size or invalid UTF-8 in byte input.
// Position is a character index; Line and Column are one-based.
type JSONErrorData struct {
	Message      string
	Document     *string
	Position     uint64
	Line, Column uint64
}

func (*JSONErrorData) isExceptionData() {}

func encodeExceptionData(data ExceptionData) []byte {
	switch x := data.(type) {
	case *UnicodeErrorData:
		if x == nil {
			return nil
		}
		b := fieldString(1, x.Encoding)
		if x.ObjectString != nil {
			b = append(b, fieldString(3, *x.ObjectString)...)
		} else {
			b = append(b, fieldBytes(2, x.ObjectBytes)...)
		}
		b = append(b, fieldVarint(4, x.Start)...)
		b = append(b, fieldVarint(5, x.End)...)
		b = append(b, fieldString(6, x.Reason)...)
		return fieldMessage(1, b)
	case *JSONErrorData:
		if x == nil {
			return nil
		}
		b := fieldString(1, x.Message)
		if x.Document != nil {
			b = append(b, fieldString(2, *x.Document)...)
		}
		b = append(b, fieldVarint(3, x.Position)...)
		b = append(b, fieldVarint(4, x.Line)...)
		b = append(b, fieldVarint(5, x.Column)...)
		return fieldMessage(2, b)
	default:
		return nil
	}
}

func decodeExceptionData(data []byte) (ExceptionData, error) {
	var result ExceptionData
	err := parseFields(data, func(f wireField) error {
		if f.tag != 1 && f.tag != 2 {
			return nil
		}
		if result != nil || f.type_ != protowire.BytesType {
			return fmt.Errorf("invalid structured exception payload")
		}
		if f.tag == 1 {
			x := &UnicodeErrorData{}
			objectSeen := false
			err := parseFields(f.bytes, func(p wireField) error {
				switch p.tag {
				case 1, 2, 3, 6:
					if p.type_ != protowire.BytesType {
						return fmt.Errorf("invalid UnicodeErrorData field %d", p.tag)
					}
					if len(p.bytes) > maxExceptionDataFieldSize {
						return fmt.Errorf("UnicodeErrorData field %d exceeds 64 KiB", p.tag)
					}
				case 4, 5:
					if p.type_ != protowire.VarintType {
						return fmt.Errorf("invalid UnicodeErrorData field %d", p.tag)
					}
				}
				switch p.tag {
				case 1:
					x.Encoding = string(p.bytes)
				case 2, 3:
					if objectSeen {
						return fmt.Errorf("UnicodeErrorData has multiple objects")
					}
					objectSeen = true
					if p.tag == 2 {
						x.ObjectBytes = append([]byte{}, p.bytes...)
					} else {
						s := string(p.bytes)
						x.ObjectString = &s
					}
				case 4:
					x.Start = p.varint
				case 5:
					x.End = p.varint
				case 6:
					x.Reason = string(p.bytes)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if !objectSeen {
				return fmt.Errorf("UnicodeErrorData has no object")
			}
			result = x
		} else {
			x := &JSONErrorData{}
			if err := parseFields(f.bytes, func(p wireField) error {
				if (p.tag == 1 || p.tag == 2) && p.type_ != protowire.BytesType || (p.tag >= 3 && p.tag <= 5) && p.type_ != protowire.VarintType {
					return fmt.Errorf("invalid JSONErrorData field %d", p.tag)
				}
				if (p.tag == 1 || p.tag == 2) && len(p.bytes) > maxExceptionDataFieldSize {
					return fmt.Errorf("JSONErrorData field %d exceeds 64 KiB", p.tag)
				}
				switch p.tag {
				case 1:
					x.Message = string(p.bytes)
				case 2:
					s := string(p.bytes)
					x.Document = &s
				case 3:
					x.Position = p.varint
				case 4:
					x.Line = p.varint
				case 5:
					x.Column = p.varint
				}
				return nil
			}); err != nil {
				return err
			}
			result = x
		}
		return nil
	})
	return result, err
}
