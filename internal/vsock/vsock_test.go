package vsock

import (
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// testPort is used for the loopback round-trip tests below. AF_VSOCK has no
// ephemeral-port allocation exposed through this package, so tests share a
// couple of fixed, unlikely-to-collide port numbers.
const (
	testPortRoundTrip = 0x1F972
	testPortNoServer  = 0x1F973
)

// skipIfUnsupported skips the test when the kernel has no AF_VSOCK support
// (e.g. the vsock/vsock_loopback modules aren't loaded), keeping `go test
// ./...` hermetic across environments that can't provide it, per project
// convention for anything needing a real kernel feature beyond stdlib syscalls.
func skipIfUnsupported(t *testing.T) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Skipf("AF_VSOCK not available in this environment: %v", err)
	}
	unix.Close(fd)
}

// TestConn_RoundTrip exercises Listen/Accept/Dial/Read/Write end to end over
// the kernel's vsock loopback transport (CID 1), which stands in here for a
// real guest connection.
func TestConn_RoundTrip(t *testing.T) {
	skipIfUnsupported(t)

	ln, err := Listen(testPortRoundTrip)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan *Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- c
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, unix.VMADDR_CID_LOCAL, testPortRoundTrip)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	var server *Conn
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Accept")
	}
	defer server.Close()

	msg := []byte("hello from host")
	if _, err := client.Write(msg); err != nil {
		t.Fatalf("client.Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(server, buf); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Errorf("server got %q, want %q", buf, msg)
	}

	reply := []byte("hello from guest")
	if _, err := server.Write(reply); err != nil {
		t.Fatalf("server.Write: %v", err)
	}
	buf2 := make([]byte, len(reply))
	if _, err := io.ReadFull(client, buf2); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf2) != string(reply) {
		t.Errorf("client got %q, want %q", buf2, reply)
	}
}

// TestDial_ContextCanceled checks that Dial does not hang past an
// already-canceled context, whether it observes cancellation directly or a
// fast connection-refused from the kernel first.
func TestDial_ContextCanceled(t *testing.T) {
	skipIfUnsupported(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := Dial(ctx, unix.VMADDR_CID_LOCAL, testPortNoServer)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error dialing with a canceled context, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dial did not return promptly with a canceled context")
	}
}

// TestDial_NoListener checks the plain failure path when nothing is
// listening on the target port.
func TestDial_NoListener(t *testing.T) {
	skipIfUnsupported(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, unix.VMADDR_CID_LOCAL, testPortNoServer)
	if err == nil {
		t.Fatal("expected error dialing a port with no listener, got nil")
	}
}
