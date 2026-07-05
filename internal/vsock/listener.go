package vsock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// listenBacklog is the pending-connection queue length passed to listen(2).
// The guest-side restore helper only ever expects a single control
// connection from the host, so a small backlog is sufficient.
const listenBacklog = 1

// Listener accepts a single incoming AF_VSOCK connection on a well-known
// port. It is used guest-side by the restore helper.
type Listener struct {
	f *os.File
}

// Listen binds and listens on port across all CIDs (VMADDR_CID_ANY), as seen
// from inside the guest.
func Listen(port uint32) (*Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock: socket: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock: set nonblocking: %w", err)
	}
	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock: bind port %d: %w", port, err)
	}
	if err := unix.Listen(fd, listenBacklog); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock: listen port %d: %w", port, err)
	}
	return &Listener{f: os.NewFile(uintptr(fd), fmt.Sprintf("vsock-listen-port%d", port))}, nil
}

// Accept blocks until a connection arrives and returns it. Only one call is
// expected for the lifetime of a Listener in the current control-channel
// protocol, but Accept may be called repeatedly.
func (l *Listener) Accept() (*Conn, error) {
	rc, err := l.f.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("vsock: syscall conn: %w", err)
	}

	var connFd int
	var acceptErr error
	rerr := rc.Read(func(rawFd uintptr) bool {
		nfd, _, err := unix.Accept(int(rawFd))
		if err == unix.EAGAIN {
			return false // not ready yet, keep waiting
		}
		if err != nil {
			acceptErr = fmt.Errorf("vsock: accept: %w", err)
			return true
		}
		connFd = nfd
		return true
	})
	if rerr != nil {
		return nil, fmt.Errorf("vsock: wait for accept: %w", rerr)
	}
	if acceptErr != nil {
		return nil, acceptErr
	}
	if err := unix.SetNonblock(connFd, true); err != nil {
		unix.Close(connFd)
		return nil, fmt.Errorf("vsock: set nonblocking on accepted conn: %w", err)
	}
	return newConn(connFd, "vsock-accepted"), nil
}

// Close stops accepting new connections.
func (l *Listener) Close() error { return l.f.Close() }
