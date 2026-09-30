package monty

import "testing"

func TestConfigureRequestMetadata(t *testing.T) {
	request := configureRequest(configureWire{})
	if got := decodeStringField(request.body, 5); got != RuntimeVersion {
		t.Fatalf("monty_version = %q, want runtime version %q", got, RuntimeVersion)
	}
	var version uint64
	var flush *uint64
	if err := parseFields(request.body, func(f wireField) error {
		if f.tag == 9 {
			version = f.varint
		}
		if f.tag == 10 {
			v := f.varint
			flush = &v
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if version != protocolVersion || flush == nil || *flush != 0 {
		t.Fatalf("configuration: protocol %d, flush %v", version, flush)
	}
}
