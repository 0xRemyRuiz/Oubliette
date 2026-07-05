//go:build linux

package pty

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpen(t *testing.T) {
	master, slave, err := Open()
	if err != nil {
		t.Skipf("pty allocation not available in this environment: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	if !strings.HasPrefix(slave.Name(), "/dev/pts/") {
		t.Errorf("slave name: got %q, want prefix /dev/pts/", slave.Name())
	}

	// The default window size should have been seeded.
	rc, err := master.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var ws *unix.Winsize
	var ioErr error
	if cerr := rc.Control(func(fd uintptr) {
		ws, ioErr = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	}); cerr != nil {
		t.Fatalf("control: %v", cerr)
	}
	if ioErr != nil {
		t.Fatalf("TIOCGWINSZ: %v", ioErr)
	}
	if ws.Row != defaultRows || ws.Col != defaultCols {
		t.Errorf("window size: got %dx%d, want %dx%d", ws.Row, ws.Col, defaultRows, defaultCols)
	}

	// Bytes written to the master appear on the slave.
	msg := []byte("hello\n")
	if _, err := master.Write(msg); err != nil {
		t.Fatalf("master.Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := slave.Read(buf); err != nil {
		t.Fatalf("slave.Read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Errorf("slave got %q, want %q", buf, msg)
	}
}
