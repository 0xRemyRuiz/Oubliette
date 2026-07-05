//go:build linux

package restorehelper

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// openPTY allocates a fresh pty pair via /dev/ptmx and opens its slave side.
// The caller owns both returned files and must close them.
//
// os.OpenFile already puts /dev/ptmx (a pollable character device) into
// non-blocking mode and registers it with the runtime poller, so master
// supports the same concurrent Read/Close-from-another-goroutine semantics
// as internal/vsock.Conn without any extra wrapping here.
func openPTY() (master, slave *os.File, err error) {
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
	if err := rc.Control(func(fd uintptr) {
		if serr := unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); serr != nil {
			ctlErr = fmt.Errorf("unlock pty: %w", serr)
			return
		}
		n, ctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN)
	}); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("control pty master: %w", err)
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

	// A freshly allocated pty reports a 0x0 window size, which makes
	// line-editing shells (fish) miscompute wrapping and cursor positioning.
	// Seed a conventional 80x24 default so the restored shell is usable; the
	// host's actual dimensions are propagated as a later enhancement.
	ws := &unix.Winsize{Row: 24, Col: 80}
	if werr := unix.IoctlSetWinsize(int(m.Fd()), unix.TIOCSWINSZ, ws); werr != nil {
		s.Close()
		m.Close()
		return nil, nil, fmt.Errorf("set pty window size: %w", werr)
	}

	return m, s, nil
}
