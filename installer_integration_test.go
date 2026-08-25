package monty_test

import (
	"context"
	"os"
	"testing"
	"time"

	monty "github.com/camdenclark/monty-go"
)

func TestIntegrationAutoInstall(t *testing.T) {
	if os.Getenv("MONTY_AUTO_INSTALL_TEST") != "1" {
		t.Skip("set MONTY_AUTO_INSTALL_TEST=1 to exercise the real runtime download")
	}
	cacheDir := os.Getenv(monty.CacheDirEnv)
	if cacheDir == "" {
		cacheDir = t.TempDir()
	}
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
}
