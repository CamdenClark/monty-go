# monty-go

An idiomatic Go client for [Pydantic Monty](https://github.com/pydantic/monty), the sandboxed Python interpreter written in Rust. It drives the same versioned subprocess protocol as the official TypeScript wrapper, so interpreter crashes and hard timeouts kill a worker rather than the Go process.

The library targets **Monty 1.2.0** (subprocess protocol 5) and requires Go 1.25+. Its installer downloads only the current platform's pinned Monty worker; the Go module itself contains no native executables.

Monty 1.2.0 adds `match`/`case` structural patterns, list slice assignment, set ordering, more user-defined arithmetic and subscript methods, dataclass `field(default=...)`, `default_factory` and `__post_init__`, and the `hashlib`, `os.path`, `bisect`, `csv` and `tomllib` modules. File iteration, newline handling, `json.load`/`json.dump`, and captured groups in `re.split` are also available. See the [upstream Python subset](https://github.com/pydantic/monty/tree/v1.2.0/docs/limitations) for each feature's supported surface.

## Runtime installation

Install the runtime explicitly during development, CI, or container construction:

```bash
go run github.com/camdenclark/monty-go/cmd/monty-install@v0.9.0
```

Applications can perform the same idempotent installation themselves:

```go
binary, err := monty.Install(ctx)
if err != nil { log.Fatal(err) }

pool, err := monty.New(ctx, monty.Options{
    BinaryPath: binary,
})
```

The installer downloads the official Pydantic platform archive directly over HTTPS, verifies pinned SHA-256 digests for both the archive and extracted executable, and stores it in a versioned directory under `os.UserCacheDir()`. Concurrent installers coordinate through a lock and publish the executable only after complete verification. Subsequent calls use the verified cached runtime without network access.

Set `MONTY_CACHE_DIR` or pass `monty.InstallOptions{CacheDir: ...}` to choose another cache. For development tools that may download on first use, opt in through the pool:

```go
pool, err := monty.New(ctx, monty.Options{AutoInstall: true})
```

Production and air-gapped deployments should run the installer while building the image, preserve the resulting cache directory, or provide a preinstalled worker through `Options.BinaryPath` or `MONTY_BIN`.

Binary lookup follows `Options.BinaryPath`, `MONTY_BIN`, the versioned runtime cache, `PATH`, an installed `@pydantic/monty-*` platform package, then a nearby Cargo `target` directory. Snapshot stores should retain the path returned by `Install` with the dump metadata and pass it as `BinaryPath` when restoring, because Monty dumps require a compatible runtime. The supported installer targets are macOS ARM64/x64, Linux ARM64/x64 using glibc, and Windows x64.

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
    binary, err := monty.Install(ctx)
    if err != nil { log.Fatal(err) }

    pool, err := monty.New(ctx, monty.Options{
        BinaryPath: binary,
    })
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

Input containers are copied into Python; passing the same Go slice or map under multiple names creates independent Python containers. Shared containers returned by Python retain their references in the decoded Go value.

Ordinary Go primitives, slices, maps, exported structs, `time.Time`, `time.Duration`, and `*big.Int` are converted automatically. The named types `Tuple`, `Dict`, `Set`, `FrozenSet`, `Date`, `Time`, `DateTime`, `TimeDelta`, `TimeZone`, `Path`, `FileHandle`, and `NamedTuple` preserve Python distinctions. Sandbox class and dataclass results use `ClassType` and `ClassInstance`, carrying UUID identities and attributes. These class representations are output-only; `Decode` maps instance attributes to Go structs. Expose host operations through explicit functions.

Go `complex64` and `complex128` values map to Python `complex`; Python complex results decode as `complex128`. Typed callbacks and `Decode` also accept `complex64` destinations, rejecting finite components that overflow. Signed zeros, infinities, and NaNs survive the wire format.

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

Async callbacks use Monty's eager-await optimization when the worker reports that no other sandbox work can run before the await. `ResumeAuto` then waits for the callback and may return completion directly, without a `FutureSnapshot`. Manual drivers can inspect `FunctionSnapshot.AllowEagerAwait` and answer with `ResumeEager` or `ResumeEagerError`; `ResumeFuture` remains available for explicit future handling.

Callbacks that accept `context.Context` should stop when it is canceled. Pending asynchronous callbacks are canceled when their feed finishes or their session closes. Go callbacks must cooperate with cancellation to stop their work.

Go structs returned by host functions are exposed to Monty as dictionaries. Arbitrary Go host objects and method calls are intentionally not exposed; register explicit host functions for each operation the sandbox may invoke.

Python results can be decoded directly into typed Go values. Dictionaries map
to structs using `monty` or `json` field tags, with recursive conversion for
nested structs, pointers, slices, arrays, and maps:

```go
type User struct {
    Name string   `monty:"name"`
    Tags []string `monty:"tags"`
}

raw, _ := session.FeedRun(ctx, `{"name": "Ada", "tags": ["go"]}`)
user, err := monty.Decode[User](raw)
```

## Importable host modules

Expose Go functions and constants through Python imports with `CheckoutOptions.ExternalModules`:

```go
tools, err := monty.NewExternalModule(map[string]any{
    "add": func(a, b int) int { return a + b },
    "VERSION": "1.0",
}, monty.ExternalModuleOptions{
    Stubs: "def add(a: int, b: int) -> int: ...\nVERSION: str",
})
if err != nil { log.Fatal(err) }
session, err := pool.Checkout(ctx, monty.CheckoutOptions{
    ExternalModules: monty.ExternalModules{"tools": tools},
})
if err != nil { log.Fatal(err) }
defer session.Close()
result, err := session.FeedRun(ctx, "from tools import add\nadd(20, 22)")
```

Attributes are copied at construction; names beginning with `_` are excluded. A nested `*ExternalModule` attribute supplies a submodule such as `tools.sub`. Functions accept the same typed parameters, kwargs and `Async` wrappers as `ExternalLookup`. Optional `.pyi` stubs are supplied before type checking. The sandbox's builtin modules take precedence over host modules.

Module results decode as `Module{Name, ID, Attrs}` and retain their UUID identity. Supply the modules again at checkout when restoring a session or snapshot; callbacks and stubs are host state. `ExternalModuleOptions.ID` can preserve a module's identity across Go processes. For manual drivers, imports and stub requests appear as OS `FunctionSnapshot`s named `import` and `import_stubs`. An `OSHandler` can return a `Module` with explicit `Function` attributes, whose callbacks resolve through `ExternalLookup`, or return a dictionary of module paths to `.pyi` strings for stubs. Missing imports raise `ModuleNotFoundError`.

## Snapshots

`FeedStart` stops at each external call, name lookup, OS call, or unresolved future. Resume manually or call `ResumeAuto` to use `ExternalLookup` and `OS` from the feed options. Snapshots are one-shot and can be serialized before resuming.

`NameLookupSnapshot.ResumeError` raises a chosen exception at a name lookup. `FeedOptions.Cwd` sets an absolute virtual POSIX working directory before a feed; leaving it empty preserves the session's current directory, initially `/`. This directory is retained by snapshots. Filesystem access still requires explicit OS callbacks.

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

Suspension snapshots expose `Position` with the source filename and UTF-8 byte offsets (`Start`, `End`, end exclusive).

Use `Session.Dump` and `LoadSession` between feeds to persist an idle REPL. Dumps are opaque Go wrapper snapshots: restore them through this package with the same Monty runtime version.

Dumps contain Monty's VM state and the Go wrapper's execution budget, suspension count, and cumulative system-sleep usage. They do not serialize host-side
`FeedOptions` state such as callback implementations or the Go goroutines and
channels behind in-flight asynchronous functions.
Supply fresh `FeedOptions` when loading a suspended snapshot. A restored
`FutureSnapshot` whose original Go work is no longer available must be resolved
manually with `FutureSnapshot.Resume`.

## Output, errors, limits, and type checking

`FeedOptions.PrintCallback` receives line-buffered stdout/stderr by default. Set `CheckoutOptions.PrintFlushInterval` to batch output, in whole milliseconds. Output is always flushed before a host call or feed completion. `NewCollectString` and `NewCollectStreams` provide capped host collectors.

Execution failures are typed as `RuntimeError`, `SyntaxError`, `TypingError`, `CrashedError`, or `ProtocolError`. A normal Python error and a typing error leave the session usable. A crash or watchdog timeout loses only that session; the pool replaces its worker.

`RuntimeError.Exception.Data` carries `*JSONErrorData` for JSON and TOML decode errors (message, optional document, character position, line, column) or `*UnicodeErrorData` (encoding, input bytes/string, failing range, reason) when available. Other exceptions have nil data. `HostError.Data` can carry the same payload through a host callback exception.

```go
session, _ := pool.Checkout(ctx, monty.CheckoutOptions{
    TypeCheck: true,
    TypeCheckStubs: `def fetch(url: str) -> str: ...`,
    TypeCheckFormat: monty.TypeCheckConcise,
    Limits: monty.ResourceLimits{
        MaxFeedDuration: 2 * time.Second,
        MaxTurnDuration: time.Second,
        MaxTotalSleep: 5 * time.Second,
        MaxSuspensions: 1000,
        MaxMemory: 64 << 20,
        MaxRecursionDepth: 100,
    },
})
```

`Options.RequestTimeout` is the hard per-protocol-turn watchdog. `MaxFeedDuration` resets for each feed, and `MaxTurnDuration` resets for each feed or resume; both exclude host callback waits. `MaxSuspensions` bounds host round trips per feed (default 1000). `MaxDuration` optionally sets a cumulative session budget enforced by the Go host and excludes host callback waits. Dumps preserve this budget and the worker's cumulative execution time. Restoration retains the stricter snapshot or destination limits, including the Go host's duration budget. Suspension counts also survive snapshot restoration. Default `time.sleep` and `asyncio.sleep` calls are serviced by the Go host, with each wait capped by the worker at 10 seconds.

`MaxTotalSleep` bounds cumulative system sleep across the session's feeds and snapshot restores. Go charges each capped sleep before waiting, including concurrent async sleeps, and aborts an over-budget feed with an uncatchable `TimeoutError`. The session remains usable. `SleepCallHost` waits are left to the host's own policy and are not charged. Zero leaves this budget unlimited.

## Clocks, time zones, sleeps, and randomness

`CheckoutOptions.OSPolicy` configures deterministic execution and host delegation:

```go
fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
sleepCap := 100 * time.Millisecond
session, err := pool.Checkout(ctx, monty.CheckoutOptions{
    OSPolicy: monty.OSPolicy{
        FixedDateTime: &fixed,
        TimeZone: "America/Los_Angeles",
        Sleep: monty.SleepSystem,
        SleepSystemMax: &sleepCap,
        RandomSeed: int64(42),
    },
})
```

The zero policy keeps system time in UTC, OS entropy, system sleeps capped at ten seconds, and a zero process clock. `DateTimeCallHost`, `RandomCallHost`, and `SleepCallHost` delegate their operations to `FeedOptions.OS`. `FixedDateTime` freezes the clock; use `TimeZone` for an IANA name or `FixedTimeZone` for a fixed offset. `SleepZero` skips waits. `SleepSystemMax` accepts an explicit zero or `monty.UnlimitedSleep`, and only applies to system sleeps. `RandomSeed` accepts Go integers, `*big.Int`, finite floats, strings, and byte slices. `ProcessTimeElapsed` exposes the session's accumulated sandbox execution time.

`FixedDateTime` and `DateTime`, `FixedTimeZone` and `TimeZone`, and `RandomSeed` and `Random` are alternative settings and cannot be combined. Duration caps are truncated to the worker's microsecond resolution. The Go host also applies its configured per-sleep cap when restoring a snapshot.

## Host OS callbacks

This wrapper intentionally provides no built-in filesystem mounts. Filesystem
operations are denied by Monty unless the host explicitly handles the relevant
OS calls through `FeedOptions.OS`. A handler may return `monty.NotHandled` to
apply Monty's default denial. Treat any handler that accesses host paths as a
security boundary and validate it independently. Audited mount support is
tracked separately in [issue #1](https://github.com/CamdenClark/monty-go/issues/1).

`os.scandir`, `os.walk`, `Path.walk`, `Path.glob` and `Path.rglob` use the `Path.scan` OS call. Its arguments are `[path, maxDepth, followSymlinks, pattern, caseSensitive, recurseSymlinks]`: `maxDepth` is `nil` or `uint32`, `pattern` is a `List` of glob parts, and `caseSensitive` is `nil` or `bool`. Preserve the difference between absent options and explicit zero/false. Return a `List` of `Tuple{relativePath, isDir, isFile, isSymlink}` entries, including the root as `""`. Globs also accept `Dict{{Key: "matches", Value: List{...}}}` with preselected relative paths. All paths are virtual POSIX paths.

## Testing

Unit tests always run. Integration tests run real Monty programs when a binary resolves, and otherwise skip during local development. Set `MONTY_REQUIRE_INTEGRATION=1` to fail if a worker cannot be started. CI requires integration tests on Linux, macOS, and Windows. To provision the pinned runtime and run everything:

```bash
go run ./cmd/monty-install
MONTY_REQUIRE_INTEGRATION=1 go test -race ./...
```

The suite includes unit and integration tests, fourteen fuzz targets, and table-driven subtests. REPL tests cover assignments, functions, imports, input bindings, overrides, session isolation, multiline execution, unsupported syntax, closures, comprehension scope, global mutation, and state preservation after errors. Monty 1.2 coverage exercises patterns and guards, slice assignment, new standard-library features, importable host modules, file iteration and scanning, and restoration under stricter destination limits. Statement fuzzing runs real Monty programs against an independent Go oracle.

The remaining tests cover installer integrity, caching and concurrency; all boundary value families; native Go object conversion; sync and async host functions; kwargs; host exceptions and panics; lazy lookup; output collectors; errors and recovery; OS callbacks; every snapshot variant; idle/suspended dumps; branched restores; pool capacity and recycling; cancellation; hard timeouts; type checking formats; assertion annotations; and resource limits.

Run all fuzz targets locally (CI also runs these on pushes, PRs, and weekly):

```bash
for target in FuzzNumericValueConversion FuzzPrimitiveValueConversion \
  FuzzStructuredValueConversion FuzzDecodeNestedResult FuzzDecodeArbitraryResult \
  FuzzMalformedProtocolDecoders FuzzTimeValueRoundTrip \
  FuzzArenaGraph FuzzArenaRoundTrip FuzzArenaMalformed FuzzComplexValueRoundTrip \
  FuzzModuleValueRoundTrip FuzzMonty12OSCalls FuzzMonty12Statements
do
  go test -run '^$' -fuzz "^${target}$" -fuzztime=60s .
done
```
