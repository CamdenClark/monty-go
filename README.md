# monty-go

An idiomatic Go client for [Pydantic Monty](https://github.com/pydantic/monty), the sandboxed Python interpreter written in Rust. It drives the same versioned subprocess protocol as the official TypeScript wrapper, so interpreter crashes and hard timeouts kill a worker rather than the Go process.

The library requires Go 1.25+. Its installer downloads only the current platform's pinned Monty worker; the Go module itself contains no native executables.

## Runtime installation

Install the runtime explicitly during development, CI, or container construction:

```bash
go run github.com/camdenclark/monty-go/cmd/monty-install@v0.2.0
```

Applications can perform the same idempotent installation themselves:

```go
path, err := monty.Install(ctx)
```

The installer downloads the official Pydantic platform archive directly over HTTPS, verifies pinned SHA-256 digests for both the archive and extracted executable, and stores it in a versioned directory under `os.UserCacheDir()`. Concurrent installers coordinate through a lock and publish the executable only after complete verification. Subsequent calls use the verified cached runtime without network access.

Set `MONTY_CACHE_DIR` or pass `monty.InstallOptions{CacheDir: ...}` to choose another cache. For development tools that may download on first use, opt in through the pool:

```go
pool, err := monty.New(ctx, monty.Options{AutoInstall: true})
```

Production and air-gapped deployments should run the installer while building the image, preserve the resulting cache directory, or provide a preinstalled worker through `Options.BinaryPath` or `MONTY_BIN`.

Binary lookup follows `Options.BinaryPath`, `MONTY_BIN`, `PATH`, the versioned runtime cache, an installed `@pydantic/monty-*` platform package, then a nearby Cargo `target` directory. The supported installer targets are macOS ARM64/x64, Linux ARM64/x64 using glibc, and Windows x64.

## Basic usage

```go
package main

import (
    "context"
    "fmt"
    "log"

    monty "github.com/camdenclark/monty-go"
)

func main() {
    ctx := context.Background()
    if _, err := monty.Install(ctx); err != nil { log.Fatal(err) }

    pool, err := monty.New(ctx)
    if err != nil { log.Fatal(err) }
    defer pool.Close()

    session, err := pool.Checkout(ctx)
    if err != nil { log.Fatal(err) }
    defer session.Close()

    _, _ = session.FeedRun(ctx, "x = 21")
    result, err := session.FeedRun(ctx, "x * 2")
    if err != nil { log.Fatal(err) }
    fmt.Println(result) // 42 (int64)
}
```

## Inputs and host functions

Ordinary Go primitives, slices, maps, exported structs, `time.Time`, `time.Duration`, and `*big.Int` are converted automatically. The named types `Tuple`, `Dict`, `Set`, `FrozenSet`, `Date`, `DateTime`, `TimeDelta`, `TimeZone`, `Path`, `FileHandle`, `NamedTuple`, and `Dataclass` preserve Python distinctions exactly.

Go functions are adapted through reflection. They may accept `context.Context`, typed positional parameters, variadic parameters, and a final `monty.Kwargs`; supported returns are `T`, `error`, or `(T, error)`.

```go
result, err := session.FeedRun(ctx, `describe(user, excited=True)`, monty.FeedOptions{
    Inputs: map[string]any{"user": map[string]any{"name": "Ada"}},
    ExternalLookup: monty.ExternalLookup{
        "describe": func(user struct { Name string `monty:"name"` }, kw monty.Kwargs) string {
            return user.Name + "!"
        },
    },
})
```

Ordinary functions are synchronous from Python. Mark a callback with `monty.Async(fn)` when Python should `await` it; the Go work then runs through Monty's external-future interface. Return `&monty.HostError{Type: "ValueError", Message: "..."}` to raise a chosen catchable Python exception.

## Snapshots

`FeedStart` stops at each external call, name lookup, OS call, or unresolved future. Resume manually or call `ResumeAuto` to use `ExternalLookup`, mounts, and `OS` from the feed options. Snapshots are one-shot and can be serialized before resuming.

```go
p, _ := session.FeedStart(ctx, `greet("Ada") + "!"`)
call := p.(*monty.FunctionSnapshot)
blob, _ := call.Dump(ctx)
p, _ = call.Resume(ctx, "hello Ada")
fmt.Println(p.(*monty.Complete).Output)

fresh, _ := pool.Checkout(ctx)
restored, _ := fresh.LoadSnapshot(ctx, blob)
_ = restored
```

Use `Session.Dump` and `LoadSession` between feeds to persist an idle REPL.

## Output, errors, limits, and type checking

`FeedOptions.PrintCallback` receives line-buffered stdout/stderr. `NewCollectString` and `NewCollectStreams` provide capped host collectors.

Execution failures are typed as `RuntimeError`, `SyntaxError`, `TypingError`, `CrashedError`, or `ProtocolError`. A normal Python error and a typing error leave the session usable. A crash or watchdog timeout loses only that session; the pool replaces its worker.

```go
session, _ := pool.Checkout(ctx, monty.CheckoutOptions{
    TypeCheck: true,
    TypeCheckStubs: `def fetch(url: str) -> str: ...`,
    TypeCheckFormat: monty.TypeCheckConcise,
    Limits: monty.ResourceLimits{
        MaxDuration: 2 * time.Second,
        MaxMemory: 64 << 20,
        MaxRecursionDepth: 100,
    },
})
```

`Options.RequestTimeout` is the hard per-protocol-turn watchdog. The cumulative `MaxDuration` clock excludes time waiting on host callbacks.

## Safe directory mounts

Mounts use Go's traversal-resistant `os.Root`. Overlay mode is the default: writes live only for one feed and never touch the host. Read-write mode persists untrusted sandbox output and should only target isolated data directories.

```go
mount, _ := monty.NewMountDir(monty.MountOptions{
    HostPath: "/srv/data",
    VirtualPath: "/data",
    Mode: monty.MountReadOnly,
})
defer mount.Close()

value, err := session.FeedRun(ctx, `open("/data/input.txt").read()`, monty.FeedOptions{Mount: mount})
```

Mounts cover file reads/writes, `open`, metadata checks, `stat`, `iterdir`, mkdir, rename, and removal. Uncovered calls fall through to `FeedOptions.OS`; return `monty.NotHandled` to apply Monty's default denial.

## Testing

Unit tests always run. Integration tests run real Monty programs when a binary resolves, and otherwise skip. To provision the pinned runtime and run everything:

```bash
go run ./cmd/monty-install
go test -race ./...
```

The suite currently contains **76 named tests** (plus table-driven subtests). Twelve tests focus specifically on REPL semantics: assignments, functions, imports, input bindings, overrides, session isolation, multiline execution, unsupported syntax, closures, comprehension scope, global mutation, and state preservation after errors.

The remaining tests cover installer integrity, caching and concurrency; all boundary value families; native Go object conversion; sync and async host functions; kwargs; host exceptions and panics; lazy lookup; output collectors; errors and recovery; OS callbacks; mount modes and traversal denial; every snapshot variant; idle/suspended dumps; branched restores; pool capacity and recycling; cancellation; hard timeouts; type checking formats; assertion annotations; and resource limits.
