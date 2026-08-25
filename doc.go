// Package monty runs Python snippets in crash-isolated Pydantic Monty worker
// subprocesses. It provides persistent REPL sessions, typed host calls,
// resumable snapshots, resource limits, and type checking without cgo or a
// CPython dependency. Filesystem access is denied unless the host explicitly
// implements OS callbacks. Install downloads and verifies the pinned worker
// for the current platform into a versioned user cache.
package monty
