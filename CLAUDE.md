# Project: Transparent Session Migration to Shadow VM

This file documents the durable rules and conventions for this project.
Claude Code reads it automatically at the start of every session.

## What this project is

A Linux-only system that performs transparent live migration of a
running process (typically an interactive shell session) from a host
machine into a pre-existing KVM guest, such that the process does not
observe the transition. The CLI accepts a target PID and a shadow VM
identifier, and on success the process continues executing inside the
guest with its filesystem view, environment, and open file descriptors
preserved as faithfully as possible.

The shadow VM itself is an input to this system, not an output. The
human prepares the guest by whatever means they prefer; this project
only concerns the migration mechanics.

## Architectural principles (do not violate without discussion)

- **The migrated process must not observe the transition.** A return
  from a syscall that crosses the boundary should look like a normal
  syscall return. `pwd`, `whoami`, `ps`, and similar introspection
  must produce coherent output post-migration.
- **Migration is one-way per invocation.** Once a process is moved
  into the shadow VM, this tool does not move it back. Reversal, if
  ever supported, is a separate operation with its own command.
- **Filesystem coherence is a precondition, not a feature.** The
  shadow VM must already expose paths that match what the process
  expects (same CWD, same open file paths). Verifying this coherence
  is part of the pre-migration check; constructing it is not.
- **Fail loud, fail early.** A migration that half-succeeds is worse
  than one that refuses to start. Any pre-check failure aborts before
  the source process is touched.

## Hard technical constraints

- **Language**: Go (latest stable, currently 1.22+). Standard module
  layout (`go.mod` at repo root, `cmd/<binary>/main.go` for entrypoints,
  `internal/` for non-exported packages).
- **Platform**: Linux only on both host and guest. Host kernel must
  support CRIU; guest kernel must support CRIU restore. We use
  Linux-specific syscalls and CRIU directly. No cross-platform
  abstraction.
- **CRIU**: invoked as a subprocess via `os/exec`, not linked. Wrap
  the CRIU CLI behind a typed helper in `internal/` with a clear
  error type. Document the minimum required CRIU version in the README.
- **KVM/QEMU interaction**: via libvirt's API (`libvirt-go` or the
  `virsh` CLI as subprocess — propose which and justify). Avoid raw
  QMP unless there's a concrete reason.
- **No CGO** unless absolutely necessary; if introduced, justify in a
  code comment and discuss with the human first.
- **Concurrency**: idiomatic goroutines + channels. Use `context.Context`
  for cancellation and timeouts everywhere I/O happens.

## Workflow rules

- **Agents never manage git.** No `git add`, `git commit`, `git push`,
  `git checkout`, `git merge`, branch creation, or any other git
  command. Only the human user runs git. If a commit boundary makes
  sense, *say so in the chat* — do not run the command.
- **Ask before any network request.** This includes `go get`,
  `go install`, downloading CRIU or libvirt binaries, pulling
  container images, `curl`, `wget`, anything that hits a remote.
  State the exact command and why, then wait for approval. `go build`
  / `go test` against an already-populated module cache is fine.
- **Plan before you write.** For any change touching more than one
  file or introducing a new package, propose the plan (file tree,
  package boundaries, function signatures, responsibilities) and wait
  for validation before writing code.
- **Small, atomic changes.** Prefer multiple focused diffs over one
  large rewrite.

## Code quality rules

- **Documentation in code is mandatory.** Every exported identifier
  (capitalized name) must have a Go doc comment in the standard form
  (`// Name does X. It returns Y when Z.`). Internal functions with
  non-trivial logic get comments too. Package-level doc comments
  (`// Package foo ...`) are required on every package.
- **Tests are mandatory.** Every package with logic has `_test.go`
  files. Use table-driven tests where appropriate. Aim for meaningful
  coverage of branches and error paths, not just happy paths. Use
  `t.TempDir()` for filesystem tests. Integration tests that require
  CRIU or a live VM are gated behind a build tag (e.g. `//go:build integration`)
  so the default `go test ./...` stays hermetic.
- **Error handling**: errors are values, wrapped with
  `fmt.Errorf("...: %w", err)` to preserve chains. Define sentinel
  errors or typed errors where callers need to branch on them.
  No `panic()` in library code; `log.Fatal` only in `main()`.
- **Logging**: use `log/slog` (standard library, structured). No
  `fmt.Println` in non-main code. Default level INFO, configurable
  via flag.
- **Config**: YAML or TOML, parsed via a well-known library. Config
  schema documented in the README and in the config struct via doc
  comments and struct tags.
- **Linting**: code must pass `go vet` and `gofmt`. Prefer
  `golangci-lint` configuration committed at repo root if introduced.