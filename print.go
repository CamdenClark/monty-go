package monty

import (
	"fmt"
	"strings"
	"sync"
)

// DefaultMaxPrintCollectBytes is the default host-side collector cap (10 MiB).
const DefaultMaxPrintCollectBytes = 10 * 1024 * 1024

func printLimit(values []int) (int, error) {
	if len(values) > 1 {
		return 0, fmt.Errorf("expected at most one maxBytes value")
	}
	if len(values) == 0 {
		return DefaultMaxPrintCollectBytes, nil
	}
	if values[0] < -1 {
		return 0, fmt.Errorf("maxBytes must be non-negative or -1 for unlimited")
	}
	return values[0], nil
}

// CollectString accumulates stdout and stderr text in arrival order.
type CollectString struct {
	mu         sync.Mutex
	output     strings.Builder
	bytes, max int
	failed     error
}

// NewCollectString creates a collector. The optional cap defaults to 10 MiB;
// pass -1 only for trusted output to disable the cap.
func NewCollectString(maxBytes ...int) (*CollectString, error) {
	max, err := printLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	return &CollectString{max: max}, nil
}
func (c *CollectString) Write(_ PrintStream, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed != nil {
		return c.failed
	}
	if c.max >= 0 && c.bytes+len(text) > c.max {
		c.failed = &HostError{Type: "MemoryError", Message: fmt.Sprintf("print collection exceeded %d bytes", c.max)}
		return c.failed
	}
	c.bytes += len(text)
	_, _ = c.output.WriteString(text)
	return nil
}
func (c *CollectString) Callback() PrintCallback { return c.Write }
func (c *CollectString) Output() string          { c.mu.Lock(); defer c.mu.Unlock(); return c.output.String() }
func (c *CollectString) Err() error              { c.mu.Lock(); defer c.mu.Unlock(); return c.failed }

// CollectedStreamEntry retains a print chunk's stream and text.
type CollectedStreamEntry struct {
	Stream PrintStream
	Text   string
}

// CollectStreams accumulates print chunks while preserving stream identity.
type CollectStreams struct {
	mu         sync.Mutex
	output     []CollectedStreamEntry
	bytes, max int
	failed     error
}

func NewCollectStreams(maxBytes ...int) (*CollectStreams, error) {
	max, err := printLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	return &CollectStreams{max: max}, nil
}
func (c *CollectStreams) Write(stream PrintStream, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed != nil {
		return c.failed
	}
	if c.max >= 0 && c.bytes+len(text) > c.max {
		c.failed = &HostError{Type: "MemoryError", Message: fmt.Sprintf("print collection exceeded %d bytes", c.max)}
		return c.failed
	}
	c.bytes += len(text)
	c.output = append(c.output, CollectedStreamEntry{stream, text})
	return nil
}
func (c *CollectStreams) Callback() PrintCallback { return c.Write }
func (c *CollectStreams) Output() []CollectedStreamEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]CollectedStreamEntry(nil), c.output...)
}
func (c *CollectStreams) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.failed }
