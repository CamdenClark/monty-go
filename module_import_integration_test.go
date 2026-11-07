package monty_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestFileImportsAndRestoredCache(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	files := map[string]string{
		"/project/helpers/__init__.py": "def double(n): return n * 2\n",
		"/project/helpers.py":          "raise AssertionError('package must precede module')\n",
		"/project/pkg/__init__.py":     "__path__ = ['/allowed/../private']\n",
		"/private/sub.py":              "value = 21\n",
	}
	handler := func(_ context.Context, name string, args []monty.Value, _ monty.Kwargs) (monty.Value, error) {
		if name == "Path.read_text" {
			if source, ok := files[args[0].(string)]; ok {
				return source, nil
			}
			return nil, &monty.HostError{Type: "FileNotFoundError", Message: "missing"}
		}
		return monty.NotHandled, nil
	}
	got := run(t, session, "import helpers\nfrom pkg.sub import value\n(helpers.double(value), helpers.__file__)", monty.FeedOptions{Cwd: "/project", OS: handler})
	if !reflect.DeepEqual(got, monty.Tuple{int64(42), "/project/helpers/__init__.py"}) {
		t.Fatalf("file imports: %#v", got)
	}
	dump, err := session.Dump(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	if err := restored.LoadSession(context.Background(), dump); err != nil {
		t.Fatal(err)
	}
	// Cached code survives restoration and does not need the original handler.
	if got := run(t, restored, "import helpers\nimport pkg.sub\nhelpers.double(pkg.sub.value)"); got != int64(42) {
		t.Fatalf("restored imports: %#v", got)
	}
}

func TestFileImportErrors(t *testing.T) {
	pool := integrationPool(t)
	for _, tc := range []struct {
		name  string
		value monty.Value
		err   error
		want  string
	}{
		{"declined", monty.NotHandled, nil, "ModuleNotFoundError"},
		{"permission", nil, &monty.HostError{Type: "PermissionError"}, "ModuleNotFoundError"},
		{"read error", nil, &monty.HostError{Type: "OSError", Message: "disk failed"}, "OSError"},
		{"wrong source type", []byte("value = 42"), nil, "RuntimeError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := integrationSession(t, pool)
			_, err := session.FeedRun(context.Background(), "import missing", monty.FeedOptions{OS: func(_ context.Context, name string, _ []monty.Value, _ monty.Kwargs) (monty.Value, error) {
				if name == "Path.read_text" {
					return tc.value, tc.err
				}
				return monty.NotHandled, nil
			}})
			var runtimeErr *monty.RuntimeError
			if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != tc.want {
				t.Fatalf("import error: %v, want %s", err, tc.want)
			}
			if got := run(t, session, "6 * 7"); got != int64(42) {
				t.Fatalf("session after failed import: %#v", got)
			}
		})
	}
}

func TestFileImportManualSnapshot(t *testing.T) {
	pool := integrationPool(t)
	session := integrationSession(t, pool)
	ctx := context.Background()
	p, err := session.FeedStart(ctx, "import helpers\nhelpers.double(21)", monty.FeedOptions{Cwd: "/project"})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok || call.FunctionName != "import" {
		t.Fatalf("import suspension: %#v", p)
	}
	if !reflect.DeepEqual(call.Args, []monty.Value{"helpers", monty.List{"/project"}}) {
		t.Fatalf("module search path: %#v", call.Args)
	}
	dump, err := call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	snapshot, err := restored.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	p, err = snapshot.(*monty.FunctionSnapshot).Resume(ctx, monty.ModuleFile{Path: "/project/helpers.py", Source: "def double(n): return n * 2\n"})
	if err != nil {
		t.Fatal(err)
	}
	if complete, ok := p.(*monty.Complete); !ok || complete.Output != int64(42) {
		t.Fatalf("resumed file import: %#v", p)
	}
}
