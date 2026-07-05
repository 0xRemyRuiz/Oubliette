//go:build linux

// Package migrate orchestrates the full process migration sequence:
// preflight checks → CRIU dump (host) → transfer → restore helper launch →
// pty bridge handoff → kill source.
//
// The source process is not touched until all preflight checks pass, and it
// is only killed once the guest-side restore is confirmed (a successful
// control-channel connection to the restore helper implies criu restore
// already succeeded there, since the helper only starts listening after a
// clean restore). Migration is one-way.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/criu"
	"github.com/oubliette/oubliette/internal/preflight"
	"github.com/oubliette/oubliette/internal/ptybridge"
	"github.com/oubliette/oubliette/internal/qga"
	"github.com/oubliette/oubliette/internal/transfer"
	"github.com/oubliette/oubliette/internal/vm"
	"github.com/oubliette/oubliette/internal/vsock"
)

// restoreHelperBinaryName is the file name the restore helper is staged
// under in the shared virtiofs directory.
const restoreHelperBinaryName = "oubliette-restorehelper"

// restoreHelperVsockPort is the AF_VSOCK port the restore helper listens on
// for the host's control-channel connection. Fixed, since only one
// migration runs against a given guest at a time.
const restoreHelperVsockPort = 9999

// restoreHelperConnectTimeout bounds how long we wait for the guest's
// restore helper to finish restoring and start listening on its vsock port.
const restoreHelperConnectTimeout = 30 * time.Second

// restoreHelperDialInterval is how often we retry the control-channel
// connection while the helper is still starting up.
const restoreHelperDialInterval = 500 * time.Millisecond

// Run migrates the process identified by pid into the libvirt domain vmName.
// It reads all parameters from cfg and logs progress via slog at INFO level.
// Any failure before the CRIU dump step leaves the source process unmodified.
func Run(ctx context.Context, pid int, vmName string, cfg *config.Config) error {
	slog.InfoContext(ctx, "starting migration", "pid", pid, "vm", vmName)

	if _, err := vm.LookupDomain(ctx, vmName); err != nil {
		return fmt.Errorf("vm lookup: %w", err)
	}
	slog.InfoContext(ctx, "vm is running", "vm", vmName)

	agent := &qga.Agent{Domain: vmName}

	checker := &preflight.Checker{
		Transfer:       agent,
		RemoteCRIUPath: cfg.VM.RemoteCRIUPath,
		VMName:         vmName,
		VsockLookup:    vm.VsockCID,
	}
	if err := checker.Run(ctx, pid); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	slog.InfoContext(ctx, "preflight passed")

	if err := os.MkdirAll(cfg.LocalDumpDir, 0700); err != nil {
		return fmt.Errorf("create local dump dir: %w", err)
	}

	dumper := &criu.Dumper{CRIUPath: cfg.CRIUPath}
	if err := dumper.Dump(ctx, pid, cfg.LocalDumpDir); err != nil {
		return fmt.Errorf("criu dump: %w", err)
	}
	slog.InfoContext(ctx, "process dumped", "dir", cfg.LocalDumpDir)

	sharedHostDir := filepath.Join(cfg.VM.SharedDirHost, cfg.VM.RemoteDumpDir)
	if err := transfer.CopyDump(cfg.LocalDumpDir, sharedHostDir); err != nil {
		return fmt.Errorf("transfer dump: %w", err)
	}
	slog.InfoContext(ctx, "dump transferred", "shared_dir", sharedHostDir)

	guestHelperPath, err := stageRestoreHelper(cfg)
	if err != nil {
		return fmt.Errorf("stage restore helper: %w", err)
	}
	slog.InfoContext(ctx, "restore helper staged", "guest_path", guestHelperPath)

	sharedGuestDir := filepath.Join(cfg.VM.SharedDirGuest, cfg.VM.RemoteDumpDir)
	if err := launchRestoreHelper(ctx, agent, guestHelperPath, sharedGuestDir, cfg.VM.RemoteCRIUPath); err != nil {
		return fmt.Errorf("launch restore helper: %w", err)
	}
	slog.InfoContext(ctx, "restore helper launched in guest")

	cid, err := vm.VsockCID(ctx, vmName)
	if err != nil {
		return fmt.Errorf("resolve vsock cid: %w", err)
	}
	conn, err := dialHelperWithRetry(ctx, cid, restoreHelperVsockPort)
	if err != nil {
		return fmt.Errorf("connect to restore helper: %w", err)
	}
	defer conn.Close()
	slog.InfoContext(ctx, "connected to restore helper; guest-side restore confirmed")

	// The helper only starts listening after a successful criu restore, so a
	// connected control channel confirms the guest-side copy is live. Kill
	// the (SIGSTOP'd, since criu dump) source process now; it is safe to do
	// so regardless of how long the operator stays attached below.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.WarnContext(ctx, "failed to kill source process (already gone?)", "pid", pid, "err", err)
	} else {
		slog.InfoContext(ctx, "source process killed", "pid", pid)
	}

	term, err := newTerminalConn()
	if err != nil {
		return fmt.Errorf("attach local terminal: %w", err)
	}
	slog.InfoContext(ctx, "attached to migrated session; input is now forwarded to the guest")
	if err := ptybridge.Pump(ctx, term, conn); err != nil {
		return fmt.Errorf("pty bridge: %w", err)
	}

	slog.InfoContext(ctx, "migration complete", "pid", pid, "vm", vmName)
	return nil
}

// stageRestoreHelper copies the oubliette-restorehelper binary (expected
// alongside the running oubliette executable) into the shared virtiofs
// directory and returns its path as seen from inside the guest.
func stageRestoreHelper(cfg *config.Config) (string, error) {
	localExe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate local executable: %w", err)
	}
	localHelperPath := filepath.Join(filepath.Dir(localExe), restoreHelperBinaryName)
	if err := transfer.StageHelper(localHelperPath, cfg.VM.SharedDirHost); err != nil {
		return "", err
	}
	return filepath.Join(cfg.VM.SharedDirGuest, restoreHelperBinaryName), nil
}

// launchRestoreHelper starts the restore helper inside the guest via the
// guest agent, backgrounded so the guest-exec call returns as soon as the
// wrapping shell has forked it off, rather than blocking for the lifetime of
// the migrated session.
func launchRestoreHelper(ctx context.Context, agent *qga.Agent, guestHelperPath, guestDumpDir, remoteCRIUPath string) error {
	launchCmd := fmt.Sprintf(
		"setsid nohup %s --dump-dir %s --criu-path %s --vsock-port %d >/tmp/oubliette-restorehelper.log 2>&1 </dev/null &",
		shellQuote(guestHelperPath), shellQuote(guestDumpDir), shellQuote(remoteCRIUPath), restoreHelperVsockPort,
	)
	return agent.RunCommand(ctx, "/bin/sh", "-c", launchCmd)
}

// shellQuote wraps s in single quotes for safe interpolation into a POSIX
// shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dialHelperWithRetry connects to the restore helper's vsock control
// channel, retrying while it is still starting up, up to
// restoreHelperConnectTimeout.
func dialHelperWithRetry(ctx context.Context, cid, port uint32) (*vsock.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, restoreHelperConnectTimeout)
	defer cancel()

	var lastErr error
	for {
		conn, err := vsock.Dial(ctx, cid, port)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for restore helper control channel: %w", lastErr)
		case <-time.After(restoreHelperDialInterval):
		}
	}
}

// terminalConn adapts the operator's own stdin/stdout into a single
// io.ReadWriteCloser for ptybridge.Pump. Stdin is switched to non-blocking
// mode and re-wrapped so that Close (triggered when the guest side ends)
// reliably interrupts a pending Read here too, the same way internal/vsock
// and internal/restorehelper's pty master already do -- a plain os.Stdin
// Read cannot otherwise be woken up from another goroutine.
//
// While attached, the controlling terminal is put into raw mode so keystrokes
// (including Ctrl-C, Tab, arrows) pass through verbatim to the guest shell,
// which owns all echo and line-editing semantics. The prior terminal state is
// captured and restored on Close so the operator's shell is left intact when
// the session ends.
type terminalConn struct {
	in       *os.File
	out      *os.File
	fd       int
	oldState *unix.Termios
}

func newTerminalConn() (*terminalConn, error) {
	fd := int(os.Stdin.Fd())
	tc := &terminalConn{out: os.Stdout, fd: fd}

	// Enter raw mode when stdin is a real terminal. If it is not (piped input,
	// tests), TCGETS fails with ENOTTY and we simply skip raw setup.
	if prev, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		raw := *prev
		raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
			unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		raw.Oflag &^= unix.OPOST
		raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		raw.Cflag &^= unix.CSIZE | unix.PARENB
		raw.Cflag |= unix.CS8
		raw.Cc[unix.VMIN] = 1
		raw.Cc[unix.VTIME] = 0
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
			return nil, fmt.Errorf("set terminal raw mode: %w", err)
		}
		tc.oldState = prev
	}

	if err := unix.SetNonblock(fd, true); err != nil {
		tc.restoreTermios()
		return nil, fmt.Errorf("set stdin nonblocking: %w", err)
	}
	tc.in = os.NewFile(uintptr(fd), "stdin")
	return tc, nil
}

// restoreTermios puts the controlling terminal back into the mode it had
// before newTerminalConn switched it to raw. It is idempotent.
func (t *terminalConn) restoreTermios() {
	if t.oldState != nil {
		_ = unix.IoctlSetTermios(t.fd, unix.TCSETS, t.oldState)
		t.oldState = nil
	}
}

func (t *terminalConn) Read(p []byte) (int, error)  { return t.in.Read(p) }
func (t *terminalConn) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t *terminalConn) Close() error {
	t.restoreTermios()
	return t.in.Close()
}
