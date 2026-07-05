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

// winsizePollInterval is how often the operator's terminal is polled for size
// changes. oubliette is not in that terminal's foreground process group (it
// runs from a different terminal), so the kernel never delivers it SIGWINCH;
// polling TIOCGWINSZ is how an out-of-band bridge notices a resize.
const winsizePollInterval = 200 * time.Millisecond

// ErrNoControllingTTY is returned when the target process has no pseudo-terminal
// as its controlling terminal, so there is no session for oubliette to bridge.
var ErrNoControllingTTY = errors.New("target has no controlling terminal")

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

	// Capture the target's controlling terminal before touching the process.
	// The migrated session is bridged back onto this same terminal so it
	// "falls" into the guest in place, rather than being pulled into
	// oubliette's own terminal. Do this before the dump so a target without a
	// tty aborts before anything is mutated.
	ttyPath, err := targetControllingTTY(pid)
	if err != nil {
		return fmt.Errorf("locate target terminal: %w", err)
	}
	slog.InfoContext(ctx, "target controlling terminal captured", "tty", ttyPath)

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

	term, err := openSessionTTY(ttyPath)
	if err != nil {
		return fmt.Errorf("attach target terminal: %w", err)
	}
	defer term.Close()
	slog.InfoContext(ctx, "attached to migrated session on target terminal; it is now driven by the guest", "tty", ttyPath)

	// Bridge the target terminal to the guest pty, forwarding data both ways
	// and window-size changes host->guest.
	relay := ptymux.NewRelay(term, conn, nil)
	winCtx, winCancel := context.WithCancel(ctx)
	go forwardWinsize(winCtx, term, relay)
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

// targetControllingTTY returns the path of the pseudo-terminal that is the
// target process's terminal, read from /proc/<pid>/fd/0. It returns
// ErrNoControllingTTY (wrapped) when fd 0 is not a /dev/pts/* device, since an
// interactive session to migrate must have one.
func targetControllingTTY(pid int) (string, error) {
	link := fmt.Sprintf("/proc/%d/fd/0", pid)
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", link, err)
	}
	if !strings.HasPrefix(target, "/dev/pts/") {
		return "", fmt.Errorf("%w: pid %d fd 0 is %q, not a pseudo-terminal", ErrNoControllingTTY, pid, target)
	}
	return target, nil
}

// sessionTTY wraps the target's controlling terminal as an io.ReadWriteCloser
// for ptymux.Relay. Opening it with os.OpenFile registers the (pollable) pts
// with the Go runtime poller in non-blocking mode, so Read/Write/Close compose
// with the relay's goroutines -- the same property internal/vsock.Conn and the
// restore helper's pty master rely on.
//
// The terminal is put into raw mode so keystrokes (Ctrl-C, Tab, arrows) pass
// through verbatim to the guest shell, which owns all echo and line-editing.
// The prior mode is captured and restored on Close so the operator's terminal
// is left intact when the session ends. All ioctls go through SyscallConn's
// Control so the descriptor is never pulled out of the poller (as File.Fd
// would do).
type sessionTTY struct {
	f        *os.File
	oldState *unix.Termios
}

func openSessionTTY(path string) (*sessionTTY, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	st := &sessionTTY{f: f}

	rc, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("syscall conn: %w", err)
	}
	var setupErr error
	if cerr := rc.Control(func(fd uintptr) {
		prev, gerr := unix.IoctlGetTermios(int(fd), unix.TCGETS)
		if gerr != nil {
			setupErr = gerr
			return
		}
		raw := *prev
		raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
			unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		raw.Oflag &^= unix.OPOST
		raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		raw.Cflag &^= unix.CSIZE | unix.PARENB
		raw.Cflag |= unix.CS8
		raw.Cc[unix.VMIN] = 1
		raw.Cc[unix.VTIME] = 0
		if serr := unix.IoctlSetTermios(int(fd), unix.TCSETS, &raw); serr != nil {
			setupErr = serr
			return
		}
		st.oldState = prev
	}); cerr != nil {
		f.Close()
		return nil, fmt.Errorf("control %s: %w", path, cerr)
	}
	if setupErr != nil {
		f.Close()
		return nil, fmt.Errorf("set raw mode on %s: %w", path, setupErr)
	}
	return st, nil
}

// winsize reads the terminal's current window size.
func (s *sessionTTY) winsize() (rows, cols uint16, err error) {
	rc, err := s.f.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var ws *unix.Winsize
	var ioErr error
	if cerr := rc.Control(func(fd uintptr) {
		ws, ioErr = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	}); cerr != nil {
		return 0, 0, cerr
	}
	if ioErr != nil {
		return 0, 0, ioErr
	}
	return ws.Row, ws.Col, nil
}

// restoreTermios puts the terminal back into its pre-raw mode. Idempotent.
func (s *sessionTTY) restoreTermios() {
	if s.oldState == nil {
		return
	}
	if rc, err := s.f.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			_ = unix.IoctlSetTermios(int(fd), unix.TCSETS, s.oldState)
		})
	}
	s.oldState = nil
}

func (s *sessionTTY) Read(p []byte) (int, error)  { return s.f.Read(p) }
func (s *sessionTTY) Write(p []byte) (int, error) { return s.f.Write(p) }
func (s *sessionTTY) Close() error {
	s.restoreTermios()
	return s.f.Close()
}

// forwardWinsize sends the terminal's size to the guest once, then polls for
// changes and forwards each new size, until ctx is done or the relay's
// connection drops. Polling is used because oubliette cannot receive SIGWINCH
// for a terminal it does not have in its foreground process group.
func forwardWinsize(ctx context.Context, term *sessionTTY, relay *ptymux.Relay) {
	var lastRows, lastCols uint16
	if rows, cols, err := term.winsize(); err == nil {
		lastRows, lastCols = rows, cols
		if err := relay.SendWinsize(rows, cols); err != nil {
			return
		}
	}
	ticker := time.NewTicker(winsizePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, cols, err := term.winsize()
			if err != nil {
				return
			}
			if rows != lastRows || cols != lastCols {
				lastRows, lastCols = rows, cols
				if err := relay.SendWinsize(rows, cols); err != nil {
					return
				}
			}
		}
	}
}
