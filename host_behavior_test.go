package monty_test

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestHostFunctionReceivesTypedSlice(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"sum_values": func(values []int) int {
		total := 0
		for _, v := range values {
			total += v
		}
		return total
	}}
	if got := run(t, session, "sum_values([10, 20, 12])", monty.FeedOptions{ExternalLookup: lookup}); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestHostFunctionReceivesTypedMapAndStruct(t *testing.T) {
	type request struct {
		Name  string `monty:"name"`
		Count int    `monty:"count"`
	}
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"score": func(req request, weights map[string]int) int { return req.Count * weights[req.Name] }}
	if got := run(t, session, "score({'name':'x','count':6}, {'x':7})", monty.FeedOptions{ExternalLookup: lookup}); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestHostFunctionReceivesVariadicArguments(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"add": func(values ...int) int {
		total := 0
		for _, v := range values {
			total += v
		}
		return total
	}}
	if got := run(t, session, "add(10, 20, 12)", monty.FeedOptions{ExternalLookup: lookup}); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestHostFunctionWithNoReturnProducesNone(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	called := false
	lookup := monty.ExternalLookup{"notify": func() { called = true }}
	if got := run(t, session, "notify()", monty.FeedOptions{ExternalLookup: lookup}); got != nil {
		t.Fatalf("got %#v", got)
	}
	if !called {
		t.Fatal("host function was not called")
	}
}

func TestHostFunctionReturnsArbitraryPrecisionInteger(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	large := new(big.Int).Lsh(big.NewInt(1), 100)
	lookup := monty.ExternalLookup{"large": func() *big.Int { return new(big.Int).Set(large) }}
	got := run(t, session, "large() + 1", monty.FeedOptions{ExternalLookup: lookup})
	want := new(big.Int).Add(large, big.NewInt(1))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDynamicHostFunctionReceivesKeywordArguments(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"format": monty.HostFunc(func(_ context.Context, args []any, kwargs monty.Kwargs) (any, error) {
		return args[0].(string) + kwargs["suffix"].(string), nil
	})}
	if got := run(t, session, "format('answer', suffix=':42')", monty.FeedOptions{ExternalLookup: lookup}); got != "answer:42" {
		t.Fatalf("got %#v", got)
	}
}

func TestTypedHostFunctionRejectsUnexpectedKeywordArguments(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"double": func(value int) int { return value * 2 }}
	_, err := session.FeedRun(context.Background(), "double(value=21)", monty.FeedOptions{ExternalLookup: lookup})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "RuntimeError" || !strings.Contains(runtimeErr.Exception.Message, "positional") {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestHostFunctionPanicBecomesPythonRuntimeError(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"explode": func() { panic("host boom") }}
	got := run(t, session, "\ntry:\n    explode()\nexcept RuntimeError as e:\n    message = str(e)\nmessage", monty.FeedOptions{ExternalLookup: lookup})
	if !strings.Contains(got.(string), "panic in host function: host boom") {
		t.Fatalf("got %#v", got)
	}
}

func TestExternalLookupIsLazy(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	options := monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"unsupported_until_read": make(chan int)}}
	if got := run(t, session, "40 + 2", options); got != int64(42) {
		t.Fatalf("got %#v", got)
	}
}

func TestInputsAreEagerlyValidated(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	_, err := session.FeedRun(context.Background(), "42", monty.FeedOptions{Inputs: map[string]any{"unsupported": make(chan int)}})
	if err == nil || !strings.Contains(err.Error(), "unsupported host value type") {
		t.Fatalf("got %v", err)
	}
	if got := run(t, session, "6 * 7"); got != int64(42) {
		t.Fatalf("validation mutated session: %#v", got)
	}
}

func TestInputsTakePrecedenceOverExternalLookup(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	called := false
	options := monty.FeedOptions{Inputs: map[string]any{"value": "input"}, ExternalLookup: monty.ExternalLookup{"value": func() string { called = true; return "external" }}}
	if got := run(t, session, "value", options); got != "input" {
		t.Fatalf("got %#v", got)
	}
	if called {
		t.Fatal("external lookup should not run")
	}
}

func TestNestedHostReturnTranslation(t *testing.T) {
	type payload struct {
		Answer int      `monty:"answer"`
		Tags   []string `monty:"tags"`
	}
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"payload": func() payload { return payload{42, []string{"go", "monty"}} }}
	want := monty.Dict{{Key: "answer", Value: int64(42)}, {Key: "tags", Value: monty.List{"go", "monty"}}}
	if got := run(t, session, "payload()", monty.FeedOptions{ExternalLookup: lookup}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func TestUnsupportedHostReturnRaisesPythonRuntimeError(t *testing.T) {
	session := integrationSession(t, integrationPool(t))
	lookup := monty.ExternalLookup{"unsupported": func() any { return make(chan int) }}
	_, err := session.FeedRun(context.Background(), "unsupported()", monty.FeedOptions{ExternalLookup: lookup})
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "RuntimeError" || !strings.Contains(runtimeErr.Exception.Message, "unsupported host value type") {
		t.Fatalf("got %T %v", err, err)
	}
}
