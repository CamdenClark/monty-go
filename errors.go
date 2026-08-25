package monty

import (
	"errors"
	"fmt"
	"strings"
)

// Error is the base interface implemented by Monty execution errors.
type Error interface {
	error
	MontyError()
}

// StackFrame is one structured Python traceback frame.
type StackFrame struct {
	Filename                         string
	Line, Column, EndLine, EndColumn uint32
	FrameName, PreviewLine           string
	HideCaret, HideFrameName         bool
}

// RaisedException contains the exception which terminated a feed.
type RaisedException struct {
	Type    string
	Message string
	Frames  []StackFrame
}

func (e RaisedException) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

// Traceback renders a compact Python-style traceback from structured frames.
func (e RaisedException) Traceback() string {
	var b strings.Builder
	if len(e.Frames) > 0 {
		b.WriteString("Traceback (most recent call last):\n")
	}
	for _, f := range e.Frames {
		name := f.FrameName
		if name == "" {
			name = "<module>"
		}
		fmt.Fprintf(&b, "  File %q, line %d", f.Filename, f.Line)
		if !f.HideFrameName {
			fmt.Fprintf(&b, ", in %s", name)
		}
		b.WriteByte('\n')
		if f.PreviewLine != "" {
			fmt.Fprintf(&b, "    %s\n", f.PreviewLine)
		}
	}
	b.WriteString(e.Error())
	return b.String()
}

// RuntimeError reports a Python exception. Ordinary runtime errors leave the
// session usable.
type RuntimeError struct {
	Exception RaisedException
}

func (e *RuntimeError) Error() string { return e.Exception.Error() }
func (*RuntimeError) MontyError()     {}

// SyntaxError is a runtime error whose exception is SyntaxError.
type SyntaxError struct{ *RuntimeError }

// TypingError reports diagnostics produced by Monty's bundled type checker.
type TypingError struct{ Diagnostics string }

func (e *TypingError) Error() string   { return "Monty type checking failed:\n" + e.Diagnostics }
func (*TypingError) MontyError()       {}
func (e *TypingError) Display() string { return e.Diagnostics }

// CrashedError means the worker died and this session cannot be reused.
type CrashedError struct {
	Message    string
	TimedOut   bool
	ExitStatus string
	cause      error
}

func (e *CrashedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "Monty worker crashed"
}

// Unwrap returns the worker failure, cancellation, or deadline cause, if any.
func (e *CrashedError) Unwrap() error { return e.cause }
func (*CrashedError) MontyError()     {}

// ProtocolError indicates an invalid protocol exchange. The session is lost.
type ProtocolError struct{ Message string }

func (e *ProtocolError) Error() string { return "Monty protocol error: " + e.Message }
func (*ProtocolError) MontyError()     {}

// HostError asks Monty to raise a particular Python exception from a host callback.
type HostError struct {
	Type    string
	Message string
}

func (e *HostError) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

func raisedFromError(err error) RaisedException {
	if err == nil {
		return RaisedException{Type: "RuntimeError", Message: "host error"}
	}
	var host *HostError
	if errors.As(err, &host) {
		return RaisedException{Type: host.Type, Message: host.Message}
	}
	return RaisedException{Type: "RuntimeError", Message: err.Error()}
}

func classifyRuntime(x RaisedException) error {
	r := &RuntimeError{Exception: x}
	if x.Type == "SyntaxError" || x.Type == "IndentationError" || x.Type == "TabError" {
		return &SyntaxError{r}
	}
	return r
}
