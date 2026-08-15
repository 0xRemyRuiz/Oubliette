// Package vsock provides minimal AF_VSOCK client and server sockets for the
// host<->guest control channel used to bridge a restored process's pty back
// to the operator. It talks directly to the kernel via golang.org/x/sys/unix;
// no CGO, no libvirt link, no external vsock library.
package vsock

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// connectPollTimeout bounds each poll(2) wait while a non-blocking connect is
// completing. It caps how long Dial can take to notice a canceled or expired
// context, since poll (unlike the runtime poller) is not woken by context.
const connectPollTimeout = 200 * time.Millisecond

// Conn is a connected AF_VSOCK stream socket. It implements io.ReadWriteCloser.
type Conn struct {
	f *os.File
}

// Read implements io.Reader.
func (c *Conn) Read(p []byte) (int, error) { return c.f.Read(p) }

// Write implements io.Writer.
func (c *Conn) Write(p []byte) (int, error) { return c.f.Write(p) }

// Close implements io.Closer. Closing unblocks any Read or Write already in
// progress on this Conn, including one blocked inside Dial's connect wait.
func (c *Conn) Close() error { return c.f.Close() }

// newConn wraps a connected, non-blocking socket fd as a Conn. The fd is
// registered with the Go runtime poller (via os.NewFile on a non-blocking
// descriptor) so Read/Write/Close compose correctly with goroutines blocked
// on them.
func newConn(fd int, name string) *Conn {
	return &Conn{f: os.NewFile(uintptr(fd), name)}
}

// Dial connects to port on the guest identified by cid, blocking until the
// connection completes, ctx is done, or the connect fails. cid is typically
// the value returned by internal/vm.VsockCID for the target domain.
func Dial(ctx context.Context, cid, port uint32) (*Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock: socket: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock: set nonblocking: %w", err)
	}

	name := fmt.Sprintf("vsock-dial-cid%d-port%d", cid, port)
	sa := &unix.SockaddrVM{CID: cid, Port: port}
	switch err := unix.Connect(fd, sa); {
	case err == nil:
		// Connected synchronously -- common over the loopback transport,
		// where there is no completion edge to wait for.
	case err == unix.EINPROGRESS:
		// Connect is pending: wait for it to resolve before handing back a
		// socket, otherwise the first read would see ENOTCONN.
		if err := waitConnect(ctx, fd, cid, port); err != nil {
			unix.Close(fd)
			return nil, err
		}
	default:
		unix.Close(fd)
		return nil, fmt.Errorf("vsock: connect to cid %d port %d: %w", cid, port, err)
	}

	return &Conn{f: os.NewFile(uintptr(fd), name)}, nil
}

// waitConnect blocks until the in-progress non-blocking connect on fd
// resolves, then reports its outcome via SO_ERROR. It uses poll(2) rather than
// the Go runtime poller: connect completion is signalled by the socket
// becoming writable, and the runtime poller is edge-triggered, so a connect
// that completes before the wait begins would leave nothing to wake it. poll
// is level-triggered and reports current writability regardless. The wait is
// sliced by connectPollTimeout so a canceled ctx is still observed promptly.
func waitConnect(ctx context.Context, fd int, cid, port uint32) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		n, err := unix.Poll(fds, int(connectPollTimeout/time.Millisecond))
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return fmt.Errorf("vsock: poll for connect: %w", err)
		}
		if n == 0 {
			continue // timed out this slice; re-check ctx and poll again
		}
		// Writable (or errored): SO_ERROR holds the authoritative verdict.
		errno, gerr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if gerr != nil {
			return fmt.Errorf("vsock: getsockopt SO_ERROR: %w", gerr)
		}
		if errno != 0 {
			return fmt.Errorf("vsock: connect to cid %d port %d: %w", cid, port, unix.Errno(errno))
		}
		return nil
	}
}
