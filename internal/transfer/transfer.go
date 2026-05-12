// Package transfer copies CRIU checkpoint images to a guest VM and triggers
// a remote criu restore, using ssh and scp subprocesses.
//
// SSH/SCP subprocesses are used rather than golang.org/x/crypto/ssh to keep
// external dependencies minimal for v0.0.1. Paths supplied to these functions
// must not contain spaces or shell metacharacters.
package transfer

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

// SSHTransfer holds the SSH/SCP parameters for a guest VM connection.
type SSHTransfer struct {
	// Host is the guest's IP address or hostname.
	Host string
	// User is the SSH login username on the guest.
	User string
	// KeyPath is the path to the SSH private key file.
	KeyPath string
	// Port is the SSH port on the guest.
	Port int
}

// RunCommand executes the given args as a remote command on the guest via SSH.
// It returns an error if the command exits non-zero or the connection fails.
func (t *SSHTransfer) RunCommand(ctx context.Context, args ...string) error {
	full := append(t.sshBaseArgs(), args...)
	out, err := execSubprocess(ctx, "ssh", full...)
	if err != nil {
		return fmt.Errorf("ssh %v: %s: %w", args, out, err)
	}
	return nil
}

// CopyDump copies localDir to remoteDir on the guest using scp -r.
// remoteDir is created if it does not already exist.
func (t *SSHTransfer) CopyDump(ctx context.Context, localDir, remoteDir string) error {
	if err := t.RunCommand(ctx, "mkdir", "-p", remoteDir); err != nil {
		return fmt.Errorf("create remote dump dir %q: %w", remoteDir, err)
	}
	dest := fmt.Sprintf("%s@%s:%s", t.User, t.Host, remoteDir)
	// localDir+"/." copies the directory contents (not the directory name itself).
	args := append(t.scpBaseArgs(), "-r", localDir+"/.", dest)
	out, err := execSubprocess(ctx, "scp", args...)
	if err != nil {
		return fmt.Errorf("scp dump to %s: %s: %w", dest, out, err)
	}
	return nil
}

// RemoteRestore runs criu restore on the guest via SSH and returns once CRIU exits.
// The --detach flag causes CRIU to hand off the restored process tree before exiting,
// so this call does not block for the lifetime of the migrated process.
func (t *SSHTransfer) RemoteRestore(ctx context.Context, criuPath, remoteDir string) error {
	// Arguments are passed directly rather than via "bash -c" to avoid shell quoting.
	args := []string{criuPath, "restore", "-D", remoteDir, "--shell-job", "--detach", "-v4"}
	if err := t.RunCommand(ctx, args...); err != nil {
		return fmt.Errorf("remote criu restore: %w", err)
	}
	return nil
}

// sshBaseArgs returns the common ssh flags followed by user@host.
// All subsequent arguments passed to ssh are treated as the remote command.
func (t *SSHTransfer) sshBaseArgs() []string {
	return []string{
		"-i", t.KeyPath,
		"-p", strconv.Itoa(t.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", t.User, t.Host),
	}
}

// scpBaseArgs returns the common scp flags (before source/destination).
func (t *SSHTransfer) scpBaseArgs() []string {
	return []string{
		"-i", t.KeyPath,
		"-P", strconv.Itoa(t.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
	}
}

func execSubprocess(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
