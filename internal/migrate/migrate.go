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

	"github.com/oubliette/oubliette/internal/coherence"
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
// It acquires a fresh freezer cgroup, moves the tree into it, and (unless the
// gate is disabled) checkpoints only once the tree is at a coherent instant.
// Callers that already own the tree's cgroup — the broker, which spawns the
// shell into one at birth — should use MigrateInCgroup instead.
//
// The source tree is killed once the guest-side restore is confirmed. Any
// failure before the CRIU dump leaves the source untouched. criu dump captures
// the whole subtree of pid, so a shell plus its running children migrate
// together.
func Migrate(ctx context.Context, pid int, vmName string, cfg *config.Config) (*vsock.Conn, error) {
	return migrateTree(ctx, nil, pid, vmName, cfg)
}

// MigrateInCgroup is like Migrate but uses cg, an already-populated freezer
// cgroup holding the tree rooted at pid. The caller retains ownership of cg and
// is responsible for closing it after this returns.
func MigrateInCgroup(ctx context.Context, cg *coherence.Cgroup, pid int, vmName string, cfg *config.Config) (*vsock.Conn, error) {
	return migrateTree(ctx, cg, pid, vmName, cfg)
}

// migrateTree is the shared migration body. cg, when non-nil, is a caller-owned
// freezer cgroup already holding the tree; when nil and the gate is enabled,
// migrateTree creates and populates one, and closes it before returning.
func migrateTree(ctx context.Context, cg *coherence.Cgroup, pid int, vmName string, cfg *config.Config) (*vsock.Conn, error) {
	slog.InfoContext(ctx, "starting migration", "pid", pid, "vm", vmName, "gate", !cfg.Gate.Disabled)

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

	dumper := &criu.Dumper{CRIUPath: cfg.CRIUPath, GhostLimit: cfg.GhostLimit, FileLocks: true}

	if cfg.Gate.Disabled {
		// Legacy path: dump at whatever instant the trap fired.
		if err := resetDir(cfg.LocalDumpDir); err != nil {
			return nil, err
		}
		if err := dumper.Dump(ctx, pid, cfg.LocalDumpDir); err != nil {
			return nil, fmt.Errorf("criu dump: %w", err)
		}
	} else {
		if cg == nil {
			owned, err := acquireCgroup(ctx, pid, cfg.Gate)
			if err != nil {
				return nil, fmt.Errorf("acquire cgroup: %w", err)
			}
			defer owned.Close()
			cg = owned
		}
		if err := gatedDump(ctx, cg, pid, dumper, cfg.LocalDumpDir, cfg.Gate); err != nil {
			return nil, err
		}
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

// procRoot is the procfs mount the coherence gate reads process state from.
const procRoot = "/proc"

// defaultDumpRetries bounds how many times gatedDump re-gates after a dump that
// lost the thaw/seize race, when the config does not specify.
const defaultDumpRetries = 3

// acquireCgroup creates a fresh freezer cgroup and moves the tree rooted at pid
// into it. The name mimics a systemd session scope so a process inspecting
// /proc/self/cgroup sees nothing that identifies the trap.
func acquireCgroup(ctx context.Context, pid int, gate config.GateConfig) (*coherence.Cgroup, error) {
	if gate.CgroupRoot == "" {
		return nil, fmt.Errorf("gate cgroup_root is empty")
	}
	cg, err := coherence.NewCgroup(gate.CgroupRoot, coherence.SessionScopeName())
	if err != nil {
		return nil, err
	}
	if err := coherence.Populate(ctx, cg, procRoot, pid); err != nil {
		_ = cg.Close()
		return nil, fmt.Errorf("populate cgroup: %w", err)
	}
	return cg, nil
}

// gatedDump checkpoints the tree in cg only at an instant the coherence gate
// judges restorable, retrying if a dump nonetheless fails on the residual
// thaw/seize race. On success criu has checkpointed and killed the tree, leaving
// cg empty. On failure the tree is left running.
func gatedDump(ctx context.Context, cg *coherence.Cgroup, pid int, dumper *criu.Dumper, dumpDir string, gate config.GateConfig) error {
	retries := gate.DumpRetries
	if retries <= 0 {
		retries = defaultDumpRetries
	}
	gateCfg := coherence.GateConfig{
		MaxAttempts: gate.MaxAttempts,
		Backoff:     time.Duration(gate.BackoffMS) * time.Millisecond,
	}

	var lastErr error
	for attempt := 1; attempt <= retries+1; attempt++ {
		// WaitForCoherentInstant returns with the tree frozen at a coherent instant.
		snap, err := coherence.WaitForCoherentInstant(ctx, cg, procRoot, gateCfg)
		if err != nil {
			return fmt.Errorf("coherence gate: %w", err)
		}
		slog.InfoContext(ctx, "coherent instant found; checkpointing",
			"tree_size", len(snap.PIDs), "attempt", attempt)

		if err := resetDir(dumpDir); err != nil {
			_ = cg.Thaw(ctx)
			return err
		}
		if gate.AdoptFreeze {
			dumper.FreezeCgroup = cg.Path() // dump the still-frozen tree (no race)
		} else {
			dumper.FreezeCgroup = ""
			if err := cg.Thaw(ctx); err != nil { // let criu re-seize the thawed tree
				return fmt.Errorf("thaw before dump: %w", err)
			}
		}

		if err := dumper.Dump(ctx, pid, dumpDir); err == nil {
			return nil
		} else {
			lastErr = err
			slog.WarnContext(ctx, "dump failed at gated instant; re-gating", "attempt", attempt, "err", err)
			_ = cg.Thaw(ctx) // AdoptFreeze leaves it frozen on failure; ensure it runs before re-gating
		}
	}
	return fmt.Errorf("gated dump failed after %d attempt(s): %w", retries+1, lastErr)
}

// resetDir empties dumpDir (or creates it), so stale images from a previous
// migration cannot contaminate this one — a leftover remap-fpath.img or *.ghost
// would otherwise be read by criu restore and fail it.
func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear dump dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dump dir: %w", err)
	}
	return nil
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
