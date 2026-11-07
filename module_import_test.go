package monty

import (
	"context"
	"fmt"
	"testing"
)

func TestImportFileRejectsUnsafeSearchInputs(t *testing.T) {
	for _, name := range []string{"../secret", "pkg./secret", "pkg..secret", "pkg.\x00secret"} {
		t.Run(name, func(t *testing.T) {
			value, err := importFile(context.Background(), func(context.Context, string, []Value, Kwargs) (Value, error) {
				t.Fatal("unsafe module name reached filesystem handler")
				return nil, nil
			}, name, List{"/project"})
			if err != nil || value != NotHandled {
				t.Fatalf("unsafe import: %#v, %v", value, err)
			}
		})
	}
	value, err := importFile(context.Background(), func(context.Context, string, []Value, Kwargs) (Value, error) {
		t.Fatal("unsafe search directory reached filesystem handler")
		return nil, nil
	}, "secret", List{"relative", "/nul\x00dir", int64(0)})
	if err != nil || value != NotHandled {
		t.Fatalf("unsafe directories: %#v, %v", value, err)
	}
}

func TestImportFileCapsSearchDirectories(t *testing.T) {
	dirs := List{"relative", nil}
	for i := range 33 {
		dirs = append(dirs, fmt.Sprintf("/d%d", i))
	}
	reads := 0
	value, err := importFile(context.Background(), func(_ context.Context, _ string, args []Value, _ Kwargs) (Value, error) {
		reads++
		if args[0] == "/d32/secret.py" || args[0] == "/d32/secret/__init__.py" {
			t.Fatal("import read beyond the search directory limit")
		}
		return NotHandled, nil
	}, "pkg.secret", dirs)
	if err != nil || value != NotHandled || reads != 64 {
		t.Fatalf("bounded search: %#v, %v, %d reads", value, err, reads)
	}
}
