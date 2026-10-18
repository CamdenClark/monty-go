package monty_test

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

// FuzzMonty12Statements exercises the real worker, boundary conversion and
// host suspension against a Go oracle for slices and structural patterns.
func FuzzMonty12Statements(f *testing.F) {
	f.Add([]byte{1, 2, 3}, int64(42), false)
	f.Add([]byte{}, int64(-7), true)
	f.Add([]byte{255, 0, 127}, int64(0), true)
	pool, err := monty.New(context.Background(), monty.Options{MaxProcesses: 1})
	if err != nil {
		if os.Getenv("MONTY_REQUIRE_INTEGRATION") == "1" || os.Getenv("MONTY_BIN") != "" {
			f.Fatal(err)
		}
		f.Skipf("Monty integration binary unavailable: %v", err)
	}
	f.Cleanup(func() { _ = pool.Close() })
	f.Fuzz(func(t *testing.T, data []byte, marker int64, reverse bool) {
		if len(data) > 128 {
			t.Skip()
		}
		marker %= 100
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		session, err := pool.Checkout(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		input := make([]int64, len(data))
		for i, b := range data {
			input[i] = int64(b)
		}
		want := append([]int64(nil), input...)
		if reverse {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		index := min(1, len(want))
		want = append(want, 0)
		copy(want[index+1:], want[index:])
		want[index] = marker
		for i := 0; i < len(want); i += 2 {
			want[i] = marker
		}
		got, err := session.FeedRun(ctx, `if reverse:
    values[:] = values[::-1]
values[1:1] = [marker]
values[::2] = [marker] * ((len(values) + 1) // 2)
match {'items': values, 'marker': marker}:
    case {'items': [head, *tail], 'marker': n} if accept(n):
        result = [head, tail, n]
    case {'items': [head, *tail], 'marker': n}:
        result = [head, tail, -n]
result`, monty.FeedOptions{
			Inputs:         map[string]any{"values": input, "marker": marker, "reverse": reverse},
			ExternalLookup: monty.ExternalLookup{"accept": func(n int64) bool { return n >= 0 }},
		})
		if err != nil {
			t.Fatal(err)
		}
		tail := make(monty.List, len(want)-1)
		for i, n := range want[1:] {
			tail[i] = n
		}
		positive := marker
		if positive < 0 {
			positive = -positive
		}
		if expected := (monty.List{want[0], tail, positive}); !reflect.DeepEqual(got, expected) {
			t.Fatalf("got %#v, want %#v", got, expected)
		}
	})
}
