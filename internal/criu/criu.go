// Package criu wraps the criu CLI for process checkpoint and restore operations.
//
// Minimum required CRIU version: 3.15 (introduced stable --shell-job and --restore-detached support).
// CRIU is invoked as a subprocess via os/exec; it is never linked as a library.
package criu

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

var (
	// ErrCRIUNotFound is returned when the criu binary cannot be located.
	ErrCRIUNotFound = fmt.Errorf("criu binary not found")
	// ErrDumpFailed is returned when criu dump exits with a non-zero status.
	ErrDumpFailed = fmt.Errorf("criu dump failed")
	// ErrRestoreFailed is returned when criu restore exits with a non-zero status.
	ErrRestoreFailed = fmt.Errorf("criu restore failed")
)

// Dumper performs CRIU checkpoint operations on a local process.
type Dumper struct {
	// CRIUPath is the absolute or PATH-relative location of the criu binary.
	CRIUPath string
	// GhostLimit, when > 0, sets --ghost-limit: the maximum size in bytes of a
	// deleted-but-still-open file that CRIU snapshots into the image (a "ghost
	// file"). CRIU's built-in cap (historically ~1 MiB) fails the whole dump the
	// moment any process in the tree holds a larger unlinked file — something
	// fork-heavy recon scripts do routinely via temp files. Raising it trades
	// image size for not aborting. Zero leaves CRIU's default in place.
	GhostLimit int64
	// FileLocks requests --file-locks, so fcntl/flock locks held anywhere in the
	// tree are checkpointed (and restored) instead of causing the dump to bail.
	FileLocks bool
	// FreezeCgroup, when non-empty, is the path of a cgroup freezer that already
	// holds the target tree frozen. Passing it as --freeze-cgroup makes CRIU
	// adopt that existing freeze rather than ptrace-seizing the tree itself, so
	// the tree is dumped at the exact instant the coherence gate inspected it.
	FreezeCgroup string
}

// Dump checkpoints process pid into dir using criu dump.
// The process is left in a stopped (frozen) state after a successful call.
// dir must already exist and be writable by the calling user.
// Returns ErrCRIUNotFound if the binary cannot be located, ErrDumpFailed on non-zero exit.
func (d *Dumper) Dump(ctx context.Context, pid int, dir string) error {
	if _, err := exec.LookPath(d.CRIUPath); err != nil {
		return fmt.Errorf("%w: %s", ErrCRIUNotFound, d.CRIUPath)
	}
	out, err := runSubprocess(ctx, d.CRIUPath, d.dumpArgs(pid, dir)...)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDumpFailed, out)
	}
	return nil
}

// dumpArgs builds the criu dump argument vector for pid into dir, appending any
// optional hardening flags configured on d. It is split out from Dump so the
// exact invocation can be unit-tested without a criu binary present.
func (d *Dumper) dumpArgs(pid int, dir string) []string {
	args := []string{
		"dump",
		"-t", strconv.Itoa(pid),
		"-D", dir,
		"--shell-job",
	}
	if d.GhostLimit > 0 {
		args = append(args, "--ghost-limit", strconv.FormatInt(d.GhostLimit, 10))
	}
	if d.FileLocks {
		args = append(args, "--file-locks")
	}
	if d.FreezeCgroup != "" {
		args = append(args, "--freeze-cgroup", d.FreezeCgroup)
	}
	return append(args, "-v4")
}

// Restorer performs CRIU restore operations from a checkpoint directory.
type Restorer struct {
	// CRIUPath is the absolute or PATH-relative location of the criu binary.
	CRIUPath string
}

// RestoreDetached resumes a dumped process from dir and returns immediately after
// CRIU hands off the process tree. The restored process runs independently.
// Returns ErrCRIUNotFound if the binary cannot be located, ErrRestoreFailed on non-zero exit.
func (r *Restorer) RestoreDetached(ctx context.Context, dir string) error {
	if _, err := exec.LookPath(r.CRIUPath); err != nil {
		return fmt.Errorf("%w: %s", ErrCRIUNotFound, r.CRIUPath)
	}
	args := []string{
		"restore",
		"-D", dir,
		"--shell-job",
		"--restore-detached",
		"-v4",
	}
	out, err := runSubprocess(ctx, r.CRIUPath, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRestoreFailed, out)
	}
	return nil
}

func runSubprocess(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
