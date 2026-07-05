//go:build linux

// Package restorehelper implements the guest-side companion process that
// restores a CRIU-dumped shell job onto a freshly allocated pty and bridges
// that pty back to the host over the AF_VSOCK control channel.
//
// It exists because `criu restore --shell-job` needs an already-open
// controlling terminal to inherit -- something a plain QEMU guest-exec
// invocation (no pty at all) can never provide. This helper allocates that
// terminal itself, runs criu attached to it, and then relays the terminal's
// I/O to whatever is on the other end of the vsock connection (the host's
// `oubliette` process).
package restorehelper

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/oubliette/oubliette/internal/ptymux"
	"github.com/oubliette/oubliette/internal/vsock"
)

// Config holds the parameters for a single restore-and-bridge run.
type Config struct {
	// DumpDir is the directory holding the CRIU dump images to restore.
	DumpDir string
	// CRIUPath is the location of the criu binary inside the guest.
	CRIUPath string
	// VsockPort is the AF_VSOCK port to listen on for the host's control
	// channel connection.
	VsockPort uint32
}

// Run allocates a new pty, restores the dump in cfg.DumpDir onto it via
// `criu restore --shell-job`, then bridges that pty to a single incoming
// AF_VSOCK connection on cfg.VsockPort until the restored process exits or
// the connection drops.
func Run(ctx context.Context, cfg Config) error {
	master, slave, err := openPTY()
	if err != nil {
		return fmt.Errorf("open pty: %w", err)
	}
	defer master.Close()

	cmd := criuRestoreCmd(cfg.CRIUPath, cfg.DumpDir, slave)
	if err := cmd.Start(); err != nil {
		slave.Close()
		return fmt.Errorf("start criu restore: %w", err)
	}
	// Our copy of the slave; the child process keeps its own via
	// Stdin/Stdout/Stderr, so closing this one doesn't affect it.
	slave.Close()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("criu restore: %w", err)
	}
	slog.InfoContext(ctx, "criu restore handed off restored process")

	ln, err := vsock.Listen(cfg.VsockPort)
	if err != nil {
		return fmt.Errorf("listen on vsock port %d: %w", cfg.VsockPort, err)
	}
	defer ln.Close()

	slog.InfoContext(ctx, "waiting for control channel connection", "port", cfg.VsockPort)
	conn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("accept vsock connection: %w", err)
	}
	defer conn.Close()

	slog.InfoContext(ctx, "bridging pty to control channel")
	// Apply host-sent window-size updates to the pty master. Go through
	// SyscallConn's Control rather than master.Fd() so the descriptor stays
	// registered with the runtime poller that the relay's reads depend on.
	relay := ptymux.NewRelay(master, conn, func(rows, cols uint16) {
		rc, err := master.SyscallConn()
		if err != nil {
			return
		}
		_ = rc.Control(func(fd uintptr) {
			_ = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
		})
	})
	return relay.Run(ctx)
}

// criuRestoreCmd builds the criu restore invocation, attached to slave as
// its controlling terminal (Setsid + Setctty) so --shell-job's "inherit an
// already-open tty" logic has a real terminal to take over.
//
// criu's own diagnostics are directed to restore.log inside the images dir
// (via -o, resolved relative to -D) rather than to its stderr: stderr here is
// the slave pty that carries the migrated session, so verbose criu output
// would otherwise corrupt that stream and be unreadable. Because -D is the
// shared virtiofs dump dir, restore.log is visible from the host too.
func criuRestoreCmd(criuPath, dumpDir string, slave *os.File) *exec.Cmd {
	cmd := exec.Command(criuPath, "restore",
		"-D", dumpDir,
		"--shell-job",
		"--restore-detached",
		"-v4",
		"-o", "restore.log",
	)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
	return cmd
}
