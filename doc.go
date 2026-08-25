// Package monty runs Python snippets in crash-isolated Pydantic Monty worker
// subprocesses. It provides persistent REPL sessions, typed host calls,
// resumable snapshots, resource limits, type checking, and safe directory
// mounts without cgo or a CPython dependency. Install downloads and verifies
// the pinned worker for the current platform into a versioned user cache.
package monty
