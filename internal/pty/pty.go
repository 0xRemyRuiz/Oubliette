//go:build linux

// Package pty allocates pseudo-terminal pairs for the processes in this project
// that need a real controlling terminal: the guest-side restore helper (so
// `criu restore --shell-job` has a tty to inherit) and the broker (so a spawned
// shell has one). It talks to /dev/ptmx directly via golang.org/x/sys/unix; no
// CGO, no external pty library.
package pty

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// defaultRows and defaultCols seed a freshly allocated pty, which otherwise
// reports a 0x0 window size that makes line-editing shells miscompute wrapping
// and cursor positioning until a real size arrives.
const (
	defaultRows = 24
	defaultCols = 80
)

// Open allocates a fresh pty pair via /dev/ptmx, opens its slave side, and
// seeds a conventional 80x24 window size. The caller owns both returned files
// and must close them.
//
// os.OpenFile puts the (pollable) pty character devices into non-blocking mode
// and registers them with the runtime poller, so master supports the same
// concurrent Read/Close-from-another-goroutine semantics as internal/vsock.Conn
// without extra wrapping. All ioctls go through SyscallConn's Control rather
// than File.Fd so the descriptors are never pulled out of the poller.
func Open() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	rc, err := m.SyscallConn()
	if err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("syscall conn: %w", err)
	}

	var n int
	var ctlErr error
	if cerr := rc.Control(func(fd uintptr) {
		if serr := unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); serr != nil {
			ctlErr = fmt.Errorf("unlock pty: %w", serr)
			return
		}
		n, ctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN)
	}); cerr != nil {
		m.Close()
		return nil, nil, fmt.Errorf("control pty master: %w", cerr)
	}
	if ctlErr != nil {
		m.Close()
		return nil, nil, ctlErr
	}

	slavePath := "/dev/pts/" + strconv.Itoa(n)
	s, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	var wsErr error
	if cerr := rc.Control(func(fd uintptr) {
		wsErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: defaultRows, Col: defaultCols})
	}); cerr != nil {
		s.Close()
		m.Close()
		return nil, nil, fmt.Errorf("control pty master for window size: %w", cerr)
	}
	if wsErr != nil {
		s.Close()
		m.Close()
		return nil, nil, fmt.Errorf("set pty window size: %w", wsErr)
	}

	return m, s, nil
}
