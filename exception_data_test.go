package monty

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestExceptionDataRoundTripAndHostErrors(t *testing.T) {
	text, doc := "é", "{"
	for _, data := range []ExceptionData{
		&UnicodeErrorData{Encoding: "utf-8", ObjectBytes: []byte{255}, Start: 0, End: 1, Reason: "invalid start byte"},
		&UnicodeErrorData{Encoding: "ascii", ObjectString: &text, Start: 0, End: 1, Reason: "ordinal not in range(128)"},
		&JSONErrorData{Message: "invalid JSON", Document: &doc, Position: 1, Line: 1, Column: 2},
		&JSONErrorData{Message: "document omitted", Position: 1000000, Line: 10, Column: 2},
	} {
		want := RaisedException{Type: "ValueError", Message: "test", Data: data}
		got, err := decodeRaised(encodeRaised(want))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("structured round trip: %#v, %v", got, err)
		}
		for _, hostErr := range []error{&HostError{Type: want.Type, Message: want.Message, Data: data}, want, &want, &RuntimeError{Exception: want}} {
			if got := raisedFromError(fmt.Errorf("wrapped: %w", hostErr)); !reflect.DeepEqual(got, want) {
				t.Fatalf("wrapped %T lost payload: %#v", hostErr, got)
			}
		}
	}
}

func TestExceptionDataMalformed(t *testing.T) {
	for name, data := range map[string][]byte{
		"unicode object missing":   fieldMessage(1, fieldString(1, "utf-8")),
		"multiple unicode objects": fieldMessage(1, append(fieldBytes(2, nil), fieldString(3, "")...)),
		"unicode field type":       fieldMessage(1, fieldVarint(2, 1)),
		"json field type":          fieldMessage(2, fieldString(3, "1")),
		"payload type":             fieldVarint(1, 0),
		"multiple payloads":        append(fieldMessage(2, nil), fieldMessage(2, nil)...),
		"large JSON document":      fieldMessage(2, fieldString(2, strings.Repeat("x", maxExceptionDataFieldSize+1))),
		"large Unicode object":     fieldMessage(1, fieldString(3, strings.Repeat("x", maxExceptionDataFieldSize+1))),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeExceptionData(data); err == nil {
				t.Fatal("malformed exception data accepted")
			}
		})
	}
}
