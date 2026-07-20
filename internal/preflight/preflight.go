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
	"strings"

	"github.com/oubliette/oubliette/internal/coherence"
)

// ErrPreflightFailed is returned (wrapping the cause) when any check fails.
var ErrPreflightFailed = fmt.Errorf("preflight check failed")

// CommandRunner runs a command inside the guest, used for pre-migration checks.
type CommandRunner interface {
	RunCommand(ctx context.Context, args ...string) error
}

// Checker runs pre-migration validation for a given PID and reachable guest.
type Checker struct {
	// Transfer is used to probe paths on the guest.
	Transfer CommandRunner
	// RemoteCRIUPath is the expected location of the criu binary on the guest.
	RemoteCRIUPath string
	// VMName is the libvirt domain name to check for a configured vsock device.
	VMName string
	// VsockLookup resolves the AF_VSOCK CID for VMName, e.g. internal/vm.VsockCID.
	// Required for the vsock-device check; injected so this package doesn't
	// need to depend on internal/vm or shell out to virsh directly.
	VsockLookup func(ctx context.Context, name string) (uint32, error)
	// ProcRoot is the procfs mount used to enumerate the target's open files.
	// Empty means "/proc"; overridden in tests.
	ProcRoot string
}

// procRoot returns the procfs mount to read, defaulting to "/proc".
func (c *Checker) procRoot() string {
	if c.ProcRoot != "" {
		return c.ProcRoot
	}
	return "/proc"
}

// Run executes all preflight checks in order and returns on the first failure.
// Checks performed:
//  1. Source process exists and its /proc entry is readable.
//  2. Source process CWD exists at the same path on the guest.
//  3. Every real file the tree holds open exists at the same path on the guest.
//  4. criu binary is present and executable on the guest.
//  5. The guest has a vsock device configured, needed for the restore
//     helper's pty control channel.
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
	if err := c.checkRemoteOpenFiles(ctx, pid); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	if err := c.checkRemoteCRIU(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	if err := c.checkVsockDevice(ctx); err != nil {
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

// checkRemoteOpenFiles verifies that every real file the tree rooted at pid holds
// open also exists on the guest at the same path. CRIU reopens such files by path
// on restore, so one that is absent there (an open script, or a runtime FIFO like
// fish's universal-variables notifier under /run/user) fails the restore and
// kills the migrated session. Catching it here aborts before the source is
// touched, turning a silent session loss into a fail-loud no-op that names the
// exact path to stage.
//
// Only STAGE-class fds are checked: pipes, ttys and other internal kernel objects
// need no guest path, and host-coupled /proc,/sys fds are the coherence gate's
// concern (resolved by dump timing, not by staging).
func (c *Checker) checkRemoteOpenFiles(ctx context.Context, pid int) error {
	pids, err := coherence.SubtreePIDs(c.procRoot(), pid)
	if err != nil {
		return fmt.Errorf("enumerate open files of tree %d: %w", pid, err)
	}
	snap := coherence.Inspect(c.procRoot(), pids, nil)
	checked := make(map[string]bool)
	for _, r := range snap.Resources {
		if r.Verdict != coherence.VerdictStage || !strings.HasPrefix(r.Target, "/") || checked[r.Target] {
			continue
		}
		checked[r.Target] = true
		if err := c.Transfer.RunCommand(ctx, "test", "-e", r.Target); err != nil {
			return fmt.Errorf("tree holds open file %q (pid %d fd %s) absent on guest; stage it there, or migrate a tree that does not hold it: %w", r.Target, r.PID, r.FD, err)
		}
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

// checkVsockDevice verifies that VMName has a vsock device configured, so
// the restore helper's control channel has a transport to run on.
func (c *Checker) checkVsockDevice(ctx context.Context) error {
	if _, err := c.VsockLookup(ctx, c.VMName); err != nil {
		return fmt.Errorf("vsock device check for %q: %w", c.VMName, err)
	}
	return nil
}
