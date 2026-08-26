package monty

import "testing"

func TestConfigureRequestSendsRuntimeVersion(t *testing.T) {
	request := configureRequest(configureWire{})
	if got := decodeStringField(request.body, 5); got != RuntimeVersion {
		t.Fatalf("monty_version = %q, want runtime version %q", got, RuntimeVersion)
	}
}
