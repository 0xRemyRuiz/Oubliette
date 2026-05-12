//go:build linux

// Package preflight performs pre-migration validation checks.
// All checks must pass before the source process is touched.
// A preflight failure leaves the source process completely unmodified.
package preflight

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/oubliette/oubliette/internal/transfer"
)

// ErrPreflightFailed is returned (wrapping the cause) when any check fails.
var ErrPreflightFailed = fmt.Errorf("preflight check failed")

// Checker runs pre-migration validation for a given PID and SSH-reachable guest.
type Checker struct {
	// Transfer is used to probe paths on the guest over SSH.
	Transfer *transfer.SSHTransfer
	// RemoteCRIUPath is the expected location of the criu binary on the guest.
	RemoteCRIUPath string
}

// Run executes all preflight checks in order and returns on the first failure.
// Checks performed:
//  1. Source process exists and its /proc entry is readable.
//  2. Source process CWD exists at the same path on the guest.
//  3. criu binary is present and executable on the guest.
func (c *Checker) Run(ctx context.Context, pid int) error {
	if err := c.checkProcess(pid); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	cwd, err := c.processCWD(pid)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	if err := c.checkRemoteCWD(ctx, cwd); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	if err := c.checkRemoteCRIU(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	return nil
}

// checkProcess verifies the process exists and its /proc/<pid>/status is readable.
func (c *Checker) checkProcess(pid int) error {
	path := "/proc/" + strconv.Itoa(pid) + "/status"
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("process %d not found or not readable: %w", pid, err)
	}
	return nil
}

// processCWD reads the working directory of pid from /proc/<pid>/cwd.
func (c *Checker) processCWD(pid int) (string, error) {
	link := "/proc/" + strconv.Itoa(pid) + "/cwd"
	cwd, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read cwd for pid %d: %w", pid, err)
	}
	return cwd, nil
}

// checkRemoteCWD verifies that cwd exists as a directory on the guest.
func (c *Checker) checkRemoteCWD(ctx context.Context, cwd string) error {
	if err := c.Transfer.RunCommand(ctx, "test", "-d", cwd); err != nil {
		return fmt.Errorf("cwd %q does not exist on guest: %w", cwd, err)
	}
	return nil
}

// checkRemoteCRIU verifies that the criu binary exists and is executable on the guest.
func (c *Checker) checkRemoteCRIU(ctx context.Context) error {
	if err := c.Transfer.RunCommand(ctx, "test", "-x", c.RemoteCRIUPath); err != nil {
		return fmt.Errorf("criu not found or not executable at %q on guest: %w", c.RemoteCRIUPath, err)
	}
	return nil
}
