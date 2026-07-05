//go:build linux

// Package migrate orchestrates the process-tree migration sequence:
// preflight checks → CRIU dump (host) → transfer → restore helper launch →
// control-channel connect → kill source. The result is a connected AF_VSOCK
// channel to the restored session's pty in the guest, which the caller relays
// to its own endpoint (an operator terminal, or the broker's client socket).
//
// The source is not touched until all preflight checks pass, and it is only
// killed once the guest-side restore is confirmed (a successful control-channel
// connection implies criu restore already succeeded, since the helper only
// starts listening after a clean restore). Migration is one-way.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/criu"
	"github.com/oubliette/oubliette/internal/preflight"
	"github.com/oubliette/oubliette/internal/ptymux"
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

// Migrate moves the process tree rooted at pid into the libvirt domain vmName
// and returns a connected AF_VSOCK channel to the restored session's pty in the
// guest. The caller owns the returned connection and must close it.
//
// The source tree is killed once the guest-side restore is confirmed. Any
// failure before the CRIU dump leaves the source untouched. criu dump captures
// the whole subtree of pid, so a shell plus its running children migrate
// together.
func Migrate(ctx context.Context, pid int, vmName string, cfg *config.Config) (*vsock.Conn, error) {
	slog.InfoContext(ctx, "starting migration", "pid", pid, "vm", vmName)

	if _, err := vm.LookupDomain(ctx, vmName); err != nil {
		return nil, fmt.Errorf("vm lookup: %w", err)
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
		return nil, fmt.Errorf("preflight: %w", err)
	}
	slog.InfoContext(ctx, "preflight passed")

	// Start from an empty dump dir: images from a previous migration must not
	// contaminate this one. A stale remap-fpath.img or *.ghost that this dump
	// does not overwrite would be read by criu restore and fail it (e.g.
	// "Remap for non existing file").
	if err := os.RemoveAll(cfg.LocalDumpDir); err != nil {
		return nil, fmt.Errorf("clear local dump dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LocalDumpDir, 0700); err != nil {
		return nil, fmt.Errorf("create local dump dir: %w", err)
	}

	dumper := &criu.Dumper{CRIUPath: cfg.CRIUPath}
	if err := dumper.Dump(ctx, pid, cfg.LocalDumpDir); err != nil {
		return nil, fmt.Errorf("criu dump: %w", err)
	}
	slog.InfoContext(ctx, "process tree dumped", "dir", cfg.LocalDumpDir)

	sharedHostDir := filepath.Join(cfg.VM.SharedDirHost, cfg.VM.RemoteDumpDir)
	if err := transfer.CopyDump(cfg.LocalDumpDir, sharedHostDir); err != nil {
		return nil, fmt.Errorf("transfer dump: %w", err)
	}
	slog.InfoContext(ctx, "dump transferred", "shared_dir", sharedHostDir)

	guestHelperPath, err := stageRestoreHelper(cfg)
	if err != nil {
		return nil, fmt.Errorf("stage restore helper: %w", err)
	}
	slog.InfoContext(ctx, "restore helper staged", "guest_path", guestHelperPath)

	sharedGuestDir := filepath.Join(cfg.VM.SharedDirGuest, cfg.VM.RemoteDumpDir)
	if err := launchRestoreHelper(ctx, agent, guestHelperPath, sharedGuestDir, cfg.VM.RemoteCRIUPath); err != nil {
		return nil, fmt.Errorf("launch restore helper: %w", err)
	}
	slog.InfoContext(ctx, "restore helper launched in guest")

	cid, err := vm.VsockCID(ctx, vmName)
	if err != nil {
		return nil, fmt.Errorf("resolve vsock cid: %w", err)
	}
	conn, err := dialHelperWithRetry(ctx, cid, restoreHelperVsockPort)
	if err != nil {
		return nil, fmt.Errorf("connect to restore helper: %w", err)
	}
	slog.InfoContext(ctx, "connected to restore helper; guest-side restore confirmed")

	// A connected control channel confirms the guest-side copy is live. Kill
	// the source now; criu dump already killed the tree, so this is cleanup
	// and ESRCH is expected.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.WarnContext(ctx, "failed to kill source process (already gone?)", "pid", pid, "err", err)
	} else {
		slog.InfoContext(ctx, "source process killed", "pid", pid)
	}

	return conn, nil
}

// Run migrates pid into vmName and bridges the restored session to this
// process's own controlling terminal (operator-attach): the migrated session
// appears in the terminal oubliette was launched from. It blocks until the
// session ends.
func Run(ctx context.Context, pid int, vmName string, cfg *config.Config) error {
	conn, err := Migrate(ctx, pid, vmName, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	term, err := newOperatorTerminal()
	if err != nil {
		return fmt.Errorf("attach operator terminal: %w", err)
	}
	defer term.Close()
	slog.InfoContext(ctx, "attached to migrated session; this terminal now drives the guest")

	relay := ptymux.NewRelay(term, conn, nil)
	winCtx, winCancel := context.WithCancel(ctx)
	go forwardOperatorWinsize(winCtx, term, relay)
	runErr := relay.Run(ctx)
	winCancel()
	if runErr != nil {
		return fmt.Errorf("pty bridge: %w", runErr)
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

// operatorTerminal adapts this process's own stdin/stdout into an
// io.ReadWriteCloser for ptymux.Relay. stdin is put into non-blocking mode and
// re-wrapped so Close (when the guest side ends) interrupts a pending Read, and
// into raw mode so keystrokes (Ctrl-C, Tab, arrows) pass through verbatim to
// the guest shell. The prior terminal mode and blocking state are restored on
// Close so the launching shell is left intact -- crucial because O_NONBLOCK is
// a property of the open file description shared with that parent shell.
type operatorTerminal struct {
	in       *os.File
	out      *os.File
	fd       int
	oldState *unix.Termios
}

func newOperatorTerminal() (*operatorTerminal, error) {
	fd := int(os.Stdin.Fd())
	t := &operatorTerminal{out: os.Stdout, fd: fd}

	// Enter raw mode when stdin is a real terminal. If it is not (piped input),
	// TCGETS fails with ENOTTY and we skip raw setup.
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
		t.oldState = prev
	}

	if err := unix.SetNonblock(fd, true); err != nil {
		t.restore()
		return nil, fmt.Errorf("set stdin nonblocking: %w", err)
	}
	t.in = os.NewFile(uintptr(fd), "stdin")
	return t, nil
}

// winsize reads the operator terminal's current window size.
func (t *operatorTerminal) winsize() (rows, cols uint16, err error) {
	ws, err := unix.IoctlGetWinsize(t.fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return ws.Row, ws.Col, nil
}

// restore reverts the raw-mode and non-blocking changes to the shared terminal.
// It is idempotent.
func (t *operatorTerminal) restore() {
	if t.oldState != nil {
		_ = unix.IoctlSetTermios(t.fd, unix.TCSETS, t.oldState)
		t.oldState = nil
	}
	// Return the shared open file description to blocking mode so the launching
	// shell's stdin is not left non-blocking after we exit.
	_ = unix.SetNonblock(t.fd, false)
}

func (t *operatorTerminal) Read(p []byte) (int, error)  { return t.in.Read(p) }
func (t *operatorTerminal) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t *operatorTerminal) Close() error {
	t.restore()
	return t.in.Close()
}

// forwardOperatorWinsize sends the terminal's size to the guest once, then on
// each SIGWINCH. Unlike the out-of-band case, oubliette owns this terminal, so
// the kernel delivers SIGWINCH here directly -- no polling needed.
func forwardOperatorWinsize(ctx context.Context, term *operatorTerminal, relay *ptymux.Relay) {
	send := func() {
		if rows, cols, err := term.winsize(); err == nil {
			_ = relay.SendWinsize(rows, cols)
		}
	}
	send() // initial size

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGWINCH)
	defer signal.Stop(sigCh)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			send()
		}
	}
}
