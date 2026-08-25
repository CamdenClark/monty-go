// Package monty runs Python snippets in crash-isolated Pydantic Monty worker
// subprocesses. It provides persistent REPL sessions, typed host calls,
// resumable snapshots, resource limits, type checking, and safe directory
// mounts without cgo or a CPython dependency.
package monty
