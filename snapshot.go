package monty

import (
	"bytes"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const snapshotMagic = "monty-go snapshot\x00\x01"

type snapshotMetadata struct {
	duration    time.Duration
	suspensions uint64
}

// The worker dump is opaque. Keep host-enforced budgets alongside it so
// restoring a suspended feed does not restart its suspension allowance.
func encodeSnapshot(state []byte, meta snapshotMetadata) []byte {
	b := append([]byte(snapshotMagic), fieldBytes(1, state)...)
	b = append(b, fieldString(2, RuntimeVersion)...)
	b = append(b, fieldVarint(3, uint64(meta.duration))...)
	return append(b, fieldVarint(4, meta.suspensions)...)
}

func decodeSnapshot(data []byte) ([]byte, snapshotMetadata, error) {
	var meta snapshotMetadata
	if !bytes.HasPrefix(data, []byte(snapshotMagic)) {
		return nil, meta, fmt.Errorf("invalid Monty Go snapshot format")
	}
	var state []byte
	var version string
	seen := map[int]bool{}
	err := parseFields(data[len(snapshotMagic):], func(f wireField) error {
		if f.tag < 1 || f.tag > 4 {
			return nil
		}
		if seen[f.tag] {
			return fmt.Errorf("snapshot field %d repeated", f.tag)
		}
		seen[f.tag] = true
		expected := protowire.VarintType
		if f.tag <= 2 {
			expected = protowire.BytesType
		}
		if f.type_ != expected {
			return fmt.Errorf("invalid snapshot field %d wire type", f.tag)
		}
		switch f.tag {
		case 1:
			state = f.bytes
		case 2:
			version = string(f.bytes)
		case 3:
			if f.varint > math.MaxInt64 {
				return fmt.Errorf("invalid snapshot duration")
			}
			meta.duration = time.Duration(f.varint)
		case 4:
			meta.suspensions = f.varint
		}
		return nil
	})
	if err != nil {
		return nil, meta, err
	}
	if len(state) == 0 {
		return nil, meta, fmt.Errorf("snapshot has no worker state")
	}
	if version != RuntimeVersion {
		return nil, meta, fmt.Errorf("snapshot requires Monty %s; installed runtime is %s", version, RuntimeVersion)
	}
	return state, meta, nil
}
