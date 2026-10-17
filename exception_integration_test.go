package monty_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestStructuredRuntimeExceptions(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	for _, tc := range []struct {
		name, code string
		check      func(*testing.T, monty.ExceptionData)
	}{
		{"json", "import json\njson.loads('{\"x\": }')", func(t *testing.T, data monty.ExceptionData) {
			x, ok := data.(*monty.JSONErrorData)
			if !ok || x.Document == nil || *x.Document != `{"x": }` || x.Position != 6 || x.Line != 1 || x.Column != 7 || x.Message == "" {
				t.Fatalf("JSON data: %#v", data)
			}
		}},
		{"unicode decode", "b'\\xff'.decode('utf-8')", func(t *testing.T, data monty.ExceptionData) {
			x, ok := data.(*monty.UnicodeErrorData)
			if !ok || !reflect.DeepEqual(x.ObjectBytes, []byte{255}) || x.ObjectString != nil || x.Start != 0 || x.End != 1 || x.Encoding != "utf-8" || x.Reason == "" {
				t.Fatalf("Unicode decode data: %#v", data)
			}
		}},
		{"unicode encode", "'é'.encode('ascii')", func(t *testing.T, data monty.ExceptionData) {
			x, ok := data.(*monty.UnicodeErrorData)
			if !ok || x.ObjectString == nil || *x.ObjectString != "é" || x.Start != 0 || x.End != 1 || x.Encoding != "ascii" || x.Reason == "" {
				t.Fatalf("Unicode encode data: %#v", data)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := session.FeedRun(context.Background(), tc.code)
			var runtimeErr *monty.RuntimeError
			if !errors.As(err, &runtimeErr) {
				t.Fatal(err)
			}
			tc.check(t, runtimeErr.Exception.Data)
		})
	}
	doc := "{"
	want := &monty.JSONErrorData{Message: "bad JSON", Document: &doc, Position: 1, Line: 1, Column: 2}
	_, err := session.FeedRun(context.Background(), "host_parse()", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{
		"host_parse": func() error {
			return &monty.HostError{Type: "json.JSONDecodeError", Message: "bad", Data: want}
		},
	}})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || !reflect.DeepEqual(runtimeErr.Exception.Data, want) {
		t.Fatalf("host exception payload: %#v, %v", runtimeErr, err)
	}
}
