package ptybridge

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// pair returns two connected in-memory net.Conn endpoints. In production a
// and b are a local terminal/pty and a vsock Conn; net.Pipe stands in for
// both here since both are just io.ReadWriteCloser.
func pair(t *testing.T) (near, far net.Conn) {
	t.Helper()
	near, far = net.Pipe()
	return near, far
}

func TestPump_RelaysBothDirections(t *testing.T) {
	aNear, aFar := pair(t)
	bNear, bFar := pair(t)
	defer aNear.Close()
	defer bNear.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Pump(ctx, aFar, bFar) }()

	if _, err := aNear.Write([]byte("to-b")); err != nil {
		t.Fatalf("aNear.Write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(bNear, buf); err != nil {
		t.Fatalf("bNear read: %v", err)
	}
	if string(buf) != "to-b" {
		t.Errorf("got %q, want %q", buf, "to-b")
	}

	if _, err := bNear.Write([]byte("to-a")); err != nil {
		t.Fatalf("bNear.Write: %v", err)
	}
	buf2 := make([]byte, 4)
	if _, err := io.ReadFull(aNear, buf2); err != nil {
		t.Fatalf("aNear read: %v", err)
	}
	if string(buf2) != "to-a" {
		t.Errorf("got %q, want %q", buf2, "to-a")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Pump returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pump did not return after context cancel")
	}
}

func TestPump_PeerCloseStopsBothDirections(t *testing.T) {
	aNear, aFar := pair(t)
	bNear, bFar := pair(t)
	defer bNear.Close()

	done := make(chan error, 1)
	go func() { done <- Pump(context.Background(), aFar, bFar) }()

	// Closing one outside peer should cause Pump to shut down cleanly and
	// close the other side too.
	aNear.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Pump returned error on peer close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pump did not return after peer close")
	}

	// bNear's peer (bFar) should now be closed by Pump.
	bNear.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := bNear.Read(make([]byte, 1)); err == nil {
		t.Error("expected error reading from bNear after Pump shutdown, got nil")
	}
}

func TestPump_ContextCancelStopsPump(t *testing.T) {
	aNear, aFar := pair(t)
	bNear, bFar := pair(t)
	defer aNear.Close()
	defer bNear.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Pump(ctx, aFar, bFar) }()

	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Pump returned unexpected error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pump did not return after context cancel")
	}
}
