# Changelog

## v0.8.0

This release expands the Go API while retaining Monty 1.1.0 and subprocess protocol 5.

- Add `CheckoutOptions.OSPolicy` for frozen or delegated clocks, time zones, seeded randomness, sleep policy, and process clocks.
- Add `ResourceLimits.MaxTotalSleep`, enforced across feeds in Go, with sleep usage preserved in idle and suspended snapshots.
- Add `FeedOptions.Cwd` and configurable print batching through `CheckoutOptions.PrintFlushInterval`.
- Preserve structured JSON and Unicode exception data in runtime and host errors.
- Expose eager-await eligibility and manual eager result/error methods; automatically use the optimization for eligible async callbacks and system sleeps.
- Add `NameLookupSnapshot.ResumeError`.

Eligible async calls can now complete during `ResumeAuto` without returning a `FutureSnapshot`. Manual drivers can continue to use `ResumeFuture` for explicit future handling. Sleep usage survives snapshot restoration, and a restoring checkout's stricter sleep budget remains a ceiling.

## v0.7.0

This release targets Monty 1.1.0 and subprocess protocol 5.

- Pin and verify the Monty 1.1.0 runtime archives and executables for all five supported platforms.
- Encode Go `complex64` and `complex128` values as Python complex numbers and decode complex results as `complex128`, preserving signed zeros and non-finite components.
- Support complex values in typed host callbacks and `Decode`, including overflow checks when narrowing to `complex64`.
- Test complex inputs, nested results, synchronous and asynchronous callbacks, snapshot restoration, malformed wire values, and arbitrary floating-point bit patterns.

Snapshots from Monty 1.0.0 must still be restored with the earlier runtime and wrapper; this version requires Monty 1.1.0 snapshots.

## v0.6.0

This release targets Monty 1.0.0 and subprocess protocol 5.

- Pin and verify the Monty 1.0.0 runtime archives and executables for all supported platforms.
- Encode and decode Monty's indexed value arenas, including shared output references, `datetime.time`, and class/dataclass results.
- Expose suspension source positions and preserve ordered stdout/stderr print segments.
- Add per-feed and per-turn execution limits and a per-feed suspension limit.
- Handle synchronous and asynchronous system sleep calls with cancellation.
- Preserve suspension counts and the cumulative execution budget in opaque Go snapshots. Restoring a suspension does not count it a second time.
- Resolve asynchronous callbacks as results become ready. Cancel pending cooperative callbacks when their feed ends or session closes.
- Add arena graph, round-trip, and malformed-input fuzzing, plus release-readiness tests.
- Require real-worker integration tests on Linux, macOS, and Windows in CI, including a clean-cache installation and documentation-example smoke test.

Inputs are copied into Python; shared Python output containers retain their references in Go. Class and dataclass results use output-only `ClassType` and `ClassInstance` values. Snapshots must be restored through this package with the same Monty runtime version.
