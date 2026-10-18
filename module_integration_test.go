package monty_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	monty "github.com/camdenclark/monty-go"
)

func TestExternalModules(t *testing.T) {
	ctx := context.Background()
	sub, err := monty.NewExternalModule(map[string]any{"twice": monty.Async(func(n int) int { return n * 2 })}, monty.ExternalModuleOptions{Stubs: "async def twice(n: int) -> int: ..."})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := monty.NewExternalModule(map[string]any{
		"add":      func(a, b int) int { return a + b },
		"describe": func(kw monty.Kwargs) string { return kw["name"].(string) },
		"fail":     func() error { return &monty.HostError{Type: "ValueError", Message: "from module"} },
		"_secret":  func() int { t.Fatal("private callback invoked"); return 0 },
		"VERSION":  3, "sub": sub,
	}, monty.ExternalModuleOptions{Stubs: "from . import sub\ndef add(a: int, b: int) -> int: ...\nVERSION: int"})
	if err != nil {
		t.Fatal(err)
	}
	modules := monty.ExternalModules{"tools": tools}
	pool := integrationPool(t)
	session := integrationSession(t, pool, monty.CheckoutOptions{ExternalModules: modules})
	got := run(t, session, `import tools
from tools import add, describe
from tools.sub import twice
[add(20, 22), describe(name='Ada'), tools.VERSION, await twice(21), type(tools).__name__, hasattr(tools, '_secret')]`)
	want := monty.List{int64(42), "Ada", int64(3), int64(42), "module", false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("module calls: %#v", got)
	}
	returned := run(t, session, "tools").(monty.Module)
	if returned.Name != "tools" || returned.ID != tools.ID() {
		t.Fatalf("module identity: %#v", returned)
	}
	_, err = session.FeedRun(ctx, "tools.fail()")
	var runtimeErr *monty.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Exception.Type != "ValueError" {
		t.Fatal(err)
	}
	dump, err := session.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool, monty.CheckoutOptions{ExternalModules: modules})
	if err := restored.LoadSession(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if got := run(t, restored, "tools.add(20, 22)"); got != int64(42) {
		t.Fatal(got)
	}
	if got := run(t, restored, "tools").(monty.Module); got.ID != returned.ID {
		t.Fatal("restored identity changed")
	}
	for _, code := range []string{"import missing", "from tools import missing", "tools._secret()"} {
		if _, err := restored.FeedRun(ctx, code); err == nil {
			t.Fatalf("accepted %s", code)
		}
	}
	checked := integrationSession(t, pool, monty.CheckoutOptions{ExternalModules: modules, TypeCheck: true})
	if got := run(t, checked, "from tools import add\nfrom tools.sub import twice\nadd(20, await twice(11))"); got != int64(42) {
		t.Fatal(got)
	}
	_, err = checked.FeedRun(ctx, "add('bad', 2)")
	var typing *monty.TypingError
	if !errors.As(err, &typing) {
		t.Fatalf("module stubs not enforced: %v", err)
	}
}

func TestImportSnapshotsAndOSHandler(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	session := integrationSession(t, pool)
	p, err := session.FeedStart(ctx, "import tools\ntools.VALUE")
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok || call.FunctionName != "import" || !reflect.DeepEqual(call.Args, []monty.Value{"tools"}) {
		t.Fatalf("import suspension: %#v", p)
	}
	dump, err := call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := integrationSession(t, pool)
	snap, err := restored.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	value := monty.Module{Name: "tools", ID: [16]byte{1}, Attrs: monty.Dict{{Key: "VALUE", Value: 42}}}
	p, err = snap.(*monty.FunctionSnapshot).Resume(ctx, value)
	if err != nil || p.(*monty.Complete).Output != int64(42) {
		t.Fatalf("manual import: %#v %v", p, err)
	}
	osSession := integrationSession(t, pool, monty.CheckoutOptions{TypeCheck: true})
	var calls []string
	got := run(t, osSession, "import tools\ntools.VALUE", monty.FeedOptions{OS: func(ctx context.Context, name string, args []monty.Value, kw monty.Kwargs) (monty.Value, error) {
		calls = append(calls, name)
		switch name {
		case "import_stubs":
			if !reflect.DeepEqual(args, []monty.Value{monty.List{"tools"}}) {
				t.Fatalf("stub arguments: %#v", args)
			}
			return map[string]any{"tools": "VALUE: int"}, nil
		case "import":
			return value, nil
		}
		return monty.NotHandled, nil
	}})
	if got != int64(42) || !reflect.DeepEqual(calls, []string{"import_stubs", "import"}) {
		t.Fatalf("OS import: %#v, %v", got, calls)
	}
}
