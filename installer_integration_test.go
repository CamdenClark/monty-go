package monty_test

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestIntegrationAutoInstall(t *testing.T) {
	if os.Getenv("MONTY_AUTO_INSTALL_TEST") != "1" {
		t.Skip("set MONTY_AUTO_INSTALL_TEST=1 to exercise the real runtime download")
	}
	cacheDir := t.TempDir()
	t.Setenv(monty.CacheDirEnv, cacheDir)
	t.Setenv("MONTY_BIN", "")
	t.Setenv("PATH", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := monty.New(ctx, monty.Options{
		AutoInstall:  true,
		CacheDir:     cacheDir,
		MinProcesses: 1,
		MaxProcesses: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	session, err := pool.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.FeedRun(ctx, "6 * 7")
	if err != nil {
		t.Fatal(err)
	}
	if result != int64(42) {
		t.Fatalf("result = %#v, want 42", result)
	}

	// These calls reproduce the README's inputs, host callbacks and typed decode.
	result, err = session.FeedRun(ctx, `describe(user, excited=True)`, monty.FeedOptions{
		Inputs: map[string]any{"user": map[string]any{"name": "Ada"}},
		ExternalLookup: monty.ExternalLookup{"describe": func(user struct {
			Name string `monty:"name"`
		}, kw monty.Kwargs) string { return user.Name + "!" }},
	})
	if err != nil || result != "Ada!" {
		t.Fatalf("host example: %#v %v", result, err)
	}
	raw, err := session.FeedRun(ctx, `{"name":"Ada","tags":["go"]}`)
	if err != nil {
		t.Fatal(err)
	}
	type User struct {
		Name string   `monty:"name"`
		Tags []string `monty:"tags"`
	}
	user, err := monty.Decode[User](raw)
	if err != nil || !reflect.DeepEqual(user, User{"Ada", []string{"go"}}) {
		t.Fatalf("decode example: %#v %v", user, err)
	}
	if _, err = session.FeedRun(ctx, "x = 21"); err != nil {
		t.Fatal(err)
	}
	dump, err := session.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := pool.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.LoadSession(ctx, dump); err != nil {
		t.Fatal(err)
	}
	if result, err = restored.FeedRun(ctx, "x * 2"); err != nil || result != int64(42) {
		t.Fatalf("idle restore: %#v %v", result, err)
	}
	p, err := restored.FeedStart(ctx, `greet("Ada") + "!"`)
	if err != nil {
		t.Fatal(err)
	}
	call, ok := p.(*monty.FunctionSnapshot)
	if !ok {
		t.Fatalf("snapshot example: %T", p)
	}
	dump, err = call.Dump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = call.Resume(ctx, "hello Ada"); err != nil {
		t.Fatal(err)
	}
	if p.(*monty.Complete).Output != "hello Ada!" {
		t.Fatal(p)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := pool.Checkout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	snap, err := fresh.LoadSnapshot(ctx, dump)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = snap.(*monty.FunctionSnapshot).Resume(ctx, "hello Ada"); err != nil || p.(*monty.Complete).Output != "hello Ada!" {
		t.Fatalf("suspended restore: %#v %v", p, err)
	}
	result, err = fresh.FeedRun(ctx, "await twice(21)", monty.FeedOptions{ExternalLookup: monty.ExternalLookup{"twice": monty.Async(func(v int) int { return v * 2 })}})
	if err != nil || result != int64(42) {
		t.Fatalf("async callback: %#v %v", result, err)
	}
	// Reinstallation uses the verified cache after a genuine clean download.
	if _, err = monty.Install(ctx, monty.InstallOptions{CacheDir: cacheDir}); err != nil {
		t.Fatal(err)
	}
}
