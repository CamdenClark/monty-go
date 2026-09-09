package monty

import "fmt"

// Time represents datetime.time, including its optional fixed offset and fold.
type Time struct {
	Hour, Minute, Second, Microsecond int
	OffsetSeconds                     *int
	TimezoneName                      *string
	Fold                              int
}

// TypeOrigin identifies where a Python class was defined.
type TypeOrigin uint32

const (
	TypeOriginBuiltin TypeOrigin = 1
	TypeOriginSandbox TypeOrigin = 2
	TypeOriginHost    TypeOrigin = 3
)

// ClassType is an output-only Python class descriptor. Builtin types use Type.
// ID preserves the upstream UUID identity across values in the same session.
type ClassType struct {
	Name        string
	ID          [16]byte
	Origin      TypeOrigin
	IsDataclass bool
	Attrs       Dict
}

// ClassInstance is an output-only Python instance, including dataclass instances.
// Decode can convert its attributes to a Go struct. Host objects are unsupported.
type ClassInstance struct {
	Type  ClassType
	ID    [16]byte
	Attrs Dict
}

func decodeUUID(data []byte) ([16]byte, error) {
	var id [16]byte
	seen := false
	err := parseFields(data, func(f wireField) error {
		if f.tag == 1 {
			if len(f.bytes) != len(id) {
				return fmt.Errorf("UUID must contain 16 bytes")
			}
			copy(id[:], f.bytes)
			seen = true
		}
		return nil
	})
	if err == nil && !seen {
		err = fmt.Errorf("UUID has no data")
	}
	return id, err
}

func decodeClassType(data []byte) (ClassType, error) {
	x := ClassType{Attrs: Dict{}}
	hasID := false
	err := parseFields(data, func(f wireField) error {
		var err error
		switch f.tag {
		case 1:
			x.Name = string(f.bytes)
		case 2:
			x.ID, err = decodeUUID(f.bytes)
			hasID = true
		case 3:
			x.Origin = TypeOrigin(f.varint)
		case 4:
			x.IsDataclass = f.varint != 0
		case 5:
			x.Attrs, err = decodeDict(f.bytes)
		}
		return err
	})
	if err != nil {
		return x, err
	}
	if x.Origin < TypeOriginBuiltin || x.Origin > TypeOriginHost {
		return x, fmt.Errorf("invalid type origin %d", x.Origin)
	}
	if hasID != (x.Origin != TypeOriginBuiltin) {
		return x, fmt.Errorf("invalid identity for type origin %d", x.Origin)
	}
	return x, nil
}

func decodeClassInstance(data []byte) (ClassInstance, error) {
	x := ClassInstance{Attrs: Dict{}}
	hasID := false
	err := parseFields(data, func(f wireField) error {
		var err error
		switch f.tag {
		case 1:
			x.Type, err = decodeClassType(f.bytes)
		case 2:
			x.ID, err = decodeUUID(f.bytes)
			hasID = true
		case 3:
			x.Attrs, err = decodeDict(f.bytes)
		}
		return err
	})
	if err == nil && (!hasID || (x.Type.Origin != TypeOriginSandbox && x.Type.Origin != TypeOriginHost)) {
		err = fmt.Errorf("class instance requires a class and instance identity")
	}
	return x, err
}

func encodeTime(x Time) []byte {
	b := fieldVarint(1, uint64(x.Hour))
	b = append(b, fieldVarint(2, uint64(x.Minute))...)
	b = append(b, fieldVarint(3, uint64(x.Second))...)
	b = append(b, fieldVarint(4, uint64(x.Microsecond))...)
	if x.OffsetSeconds != nil {
		b = append(b, fieldVarint(5, uint64(int64(*x.OffsetSeconds)))...)
	}
	if x.TimezoneName != nil {
		b = append(b, fieldString(6, *x.TimezoneName)...)
	}
	return append(b, fieldVarint(7, uint64(x.Fold))...)
}

func decodeTime(data []byte) Time {
	var x Time
	_ = parseFields(data, func(f wireField) error {
		switch f.tag {
		case 1:
			x.Hour = int(f.varint)
		case 2:
			x.Minute = int(f.varint)
		case 3:
			x.Second = int(f.varint)
		case 4:
			x.Microsecond = int(f.varint)
		case 5:
			v := int(int32(f.varint))
			x.OffsetSeconds = &v
		case 6:
			v := string(f.bytes)
			x.TimezoneName = &v
		case 7:
			x.Fold = int(f.varint)
		}
		return nil
	})
	return x
}
