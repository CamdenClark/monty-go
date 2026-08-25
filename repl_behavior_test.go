package monty_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestREPLAssignmentReturnsNone(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	if got := run(t, session, "answer = 42"); got != nil {
		t.Fatalf("assignment returned %#v", got)
	}
}

func TestREPLFunctionDefinitionPersists(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "def double(value):\n    return value * 2")
	if got := run(t, session, "double(21)"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestREPLImportPersists(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "import json")
	want := monty.Dict{{Key: "ok", Value: true}}
	if got := run(t, session, "json.loads('{\"ok\": true}')"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func TestREPLRuntimeErrorPreservesGlobals(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "before_error = 40")
	if _, err := session.FeedRun(context.Background(), "1 / 0"); err == nil {
		t.Fatal("expected division error")
	}
	if got := run(t, session, "before_error + 2"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestREPLInputBindingPersists(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	if got := run(t, session, "input_value", monty.FeedOptions{Inputs: map[string]any{"input_value": 41}}); got != int64(41) {
		t.Fatalf("input returned %#v", got)
	}
	if got := run(t, session, "input_value + 1"); got != int64(42) {
		t.Fatalf("persisted input returned %#v", got)
	}
}

func TestREPLInputOverridesExistingGlobal(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "value = 1")
	if got := run(t, session, "value", monty.FeedOptions{Inputs: map[string]any{"value": 42}}); got != int64(42) {
		t.Fatalf("override returned %#v", got)
	}
	if got := run(t, session, "value"); got != int64(42) {
		t.Fatalf("override did not persist: %#v", got)
	}
}

func TestREPLSessionsAreIsolated(t *testing.T) {
	pool := integrationPool(t)
	first := integrationSession(t, pool)
	second := integrationSession(t, pool)
	run(t, first, "private_value = 42")
	_, err := second.FeedRun(context.Background(), "private_value")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "NameError" {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestREPLMultilineLoop(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	code := "total = 0\nfor value in range(7):\n    if value % 2 == 0:\n        total += value\ntotal"
	if got := run(t, session, code); got != int64(12) {
		t.Fatalf("got %#v", got)
	}
}

func TestREPLDeleteStatementReportsNotImplemented(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "temporary = 'present'")
	_, err := session.FeedRun(context.Background(), "del temporary")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "NotImplementedError" {
		t.Fatalf("got %T %v", err, err)
	}
	if got := run(t, session, "temporary"); got != "present" {
		t.Fatalf("failed syntax changed state: %#v", got)
	}
}

func TestREPLClosurePersists(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "def make_adder(amount):\n    def add(value):\n        return value + amount\n    return add\nadd_two = make_adder(2)")
	if got := run(t, session, "add_two(40)"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestREPLComprehensionVariableDoesNotLeak(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "squares = [item * item for item in range(3)]")
	_, err := session.FeedRun(context.Background(), "item")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "NameError" {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestREPLFunctionMutatesGlobal(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	run(t, session, "counter = 40\ndef increment():\n    global counter\n    counter += 1\nincrement()\nincrement()")
	if got := run(t, session, "counter"); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}
