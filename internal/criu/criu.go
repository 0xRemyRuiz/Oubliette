// Package criu wraps the criu CLI for process checkpoint and restore operations.
//
// Minimum required CRIU version: 3.15 (introduced stable --shell-job and --detach support).
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
}

// Dump checkpoints process pid into dir using criu dump.
// The process is left in a stopped (frozen) state after a successful call.
// dir must already exist and be writable by the calling user.
// Returns ErrCRIUNotFound if the binary cannot be located, ErrDumpFailed on non-zero exit.
func (d *Dumper) Dump(ctx context.Context, pid int, dir string) error {
	if _, err := exec.LookPath(d.CRIUPath); err != nil {
		return fmt.Errorf("%w: %s", ErrCRIUNotFound, d.CRIUPath)
	}
	args := []string{
		"dump",
		"-t", strconv.Itoa(pid),
		"-D", dir,
		"--shell-job",
		"-v4",
	}
	out, err := runSubprocess(ctx, d.CRIUPath, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDumpFailed, out)
	}
	return nil
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
		"--detach",
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
