// Command monty-install downloads and verifies the Monty worker expected by
// the matching version of monty-go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	monty "github.com/camdenclark/monty-go"
)

func main() {
	cacheDir := flag.String("cache-dir", "", "runtime cache directory (default: MONTY_CACHE_DIR or the user cache)")
	force := flag.Bool("force", false, "download and verify the runtime even if it is already cached")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "monty-install does not accept positional arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	path, err := monty.Install(ctx, monty.InstallOptions{CacheDir: *cacheDir, Force: *force})
	if err != nil {
		fmt.Fprintf(os.Stderr, "monty-install: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(path)
}
