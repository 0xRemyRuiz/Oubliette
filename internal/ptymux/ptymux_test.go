package ptymux

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestFrameRoundTrip checks that every frame type survives an encode/decode.
func TestFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		typ     frameType
		payload []byte
	}{
		{"data", frameData, []byte("hello world")},
		{"empty data", frameData, []byte{}},
		{"winsize", frameWinsize, encodeWinsize(40, 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeFrameTo(&buf, tt.typ, tt.payload); err != nil {
				t.Fatalf("writeFrameTo: %v", err)
			}
			gotType, gotPayload, err := readFrame(&buf)
			if err != nil {
				t.Fatalf("readFrame: %v", err)
			}
			if gotType != tt.typ {
				t.Errorf("type: got %d, want %d", gotType, tt.typ)
			}
			if !bytes.Equal(gotPayload, tt.payload) {
				t.Errorf("payload: got %q, want %q", gotPayload, tt.payload)
			}
		})
	}
}

// TestWinsizeCodec checks the window-size payload packing and its length guard.
func TestWinsizeCodec(t *testing.T) {
	rows, cols, err := decodeWinsize(encodeWinsize(24, 80))
	if err != nil {
		t.Fatalf("decodeWinsize: %v", err)
	}
	if rows != 24 || cols != 80 {
		t.Errorf("got %dx%d, want 24x80", rows, cols)
	}
	if _, _, err := decodeWinsize([]byte{1, 2, 3}); err == nil {
		t.Error("expected error on short winsize payload")
	}
}

// TestRelayDataAndWinsize drives a guest-side Relay through net.Pipe pairs and
// verifies data flows both ways and a window-size frame reaches the handler.
func TestRelayDataAndWinsize(t *testing.T) {
	guestConn, hostSide := net.Pipe()     // the framed transport
	ptyRelayEnd, ptyTestEnd := net.Pipe() // stands in for the guest pty master

	winCh := make(chan [2]uint16, 1)
	relay := NewRelay(ptyRelayEnd, guestConn, func(rows, cols uint16) {
		winCh <- [2]uint16{rows, cols}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- relay.Run(ctx) }()

	// host -> guest window-size frame reaches the handler
	go func() { _ = writeFrameTo(hostSide, frameWinsize, encodeWinsize(50, 120)) }()
	select {
	case ws := <-winCh:
		if ws[0] != 50 || ws[1] != 120 {
			t.Errorf("winsize: got %dx%d, want 50x120", ws[0], ws[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for winsize handler")
	}

	// host -> guest data frame surfaces on the pty stream
	go func() { _ = writeFrameTo(hostSide, frameData, []byte("to-guest")) }()
	buf := make([]byte, len("to-guest"))
	if err := readWithTimeout(t, ptyTestEnd, buf); err != nil {
		t.Fatalf("read guest pty: %v", err)
	}
	if string(buf) != "to-guest" {
		t.Errorf("guest pty got %q, want %q", buf, "to-guest")
	}

	// guest pty -> host is framed as data
	go func() { _, _ = ptyTestEnd.Write([]byte("to-host")) }()
	gotType, payload, err := readFrameWithTimeout(t, hostSide)
	if err != nil {
		t.Fatalf("readFrame from host side: %v", err)
	}
	if gotType != frameData || string(payload) != "to-host" {
		t.Errorf("host got type=%d payload=%q, want data %q", gotType, payload, "to-host")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func readWithTimeout(t *testing.T, r io.Reader, buf []byte) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(r, buf); done <- err }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		return io.ErrNoProgress
	}
}

func readFrameWithTimeout(t *testing.T, r io.Reader) (frameType, []byte, error) {
	t.Helper()
	type res struct {
		typ     frameType
		payload []byte
		err     error
	}
	done := make(chan res, 1)
	go func() {
		typ, payload, err := readFrame(r)
		done <- res{typ, payload, err}
	}()
	select {
	case v := <-done:
		return v.typ, v.payload, v.err
	case <-time.After(2 * time.Second):
		return 0, nil, io.ErrNoProgress
	}
}
