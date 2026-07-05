// Package ptymux multiplexes a raw pseudo-terminal byte stream and terminal
// control messages (currently window-size updates) over a single connection.
//
// The host and guest each own one end of a pty (the operator's terminal on the
// host, the restored shell's pty master in the guest) and are joined by one
// AF_VSOCK stream. Bytes must flow both ways, but the host must also be able to
// tell the guest when the terminal is resized -- which cannot share the raw
// byte lane without a framing that distinguishes the two. This package provides
// that framing and a symmetric Relay that both ends run.
//
// Wire format, one frame:
//
//	[1 byte type][2 bytes big-endian payload length][payload]
//
// Types are frameData (payload is raw pty bytes) and frameWinsize (payload is
// rows and cols, each a big-endian uint16). Window-size frames only ever travel
// host->guest; data frames travel both ways.
package ptymux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// frameType tags a wire frame.
type frameType byte

const (
	frameData    frameType = 0
	frameWinsize frameType = 1
)

// maxPayload bounds a single frame's payload. It fits comfortably in the
// 16-bit length field and sizes the pty read buffer.
const maxPayload = 32 * 1024

// Relay copies bytes between a raw local pty stream and a framed connection,
// and delivers window-size control frames received on the connection to an
// optional handler.
//
// It is symmetric: the host constructs it with a nil handler and pushes
// resizes via SendWinsize; the guest constructs it with a handler that applies
// the size to its pty master and never sends.
type Relay struct {
	local     io.ReadWriteCloser
	conn      io.ReadWriteCloser
	onWinsize func(rows, cols uint16)

	// writeMu serializes writes to conn so a SendWinsize frame cannot be
	// interleaved into the middle of a data frame emitted by the copy loop.
	writeMu sync.Mutex
}

// NewRelay returns a Relay joining local (a raw pty stream) and conn (the
// framed transport). onWinsize, if non-nil, is called for each window-size
// frame read from conn; pass nil on the sending end.
func NewRelay(local, conn io.ReadWriteCloser, onWinsize func(rows, cols uint16)) *Relay {
	return &Relay{local: local, conn: conn, onWinsize: onWinsize}
}

// Run copies in both directions until either side reaches EOF or errors, or
// ctx is canceled, then closes both endpoints to unblock the other direction.
// A shutdown caused by a clean EOF, or by Run's own closing to unblock a peer,
// is not reported. The first other error, if any, is returned.
func (r *Relay) Run(ctx context.Context) error {
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			r.local.Close()
			r.conn.Close()
		})
	}
	defer closeBoth()

	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()

	done := make(chan error, 2)
	go func() { done <- r.localToConn() }()
	go func() { done <- r.connToLocal() }()

	var firstErr error
	for i := 0; i < 2; i++ {
		err := <-done
		closeBoth() // unblock the other direction as soon as one side stops
		if err != nil && firstErr == nil && !isBenign(err) {
			firstErr = err
		}
	}
	return firstErr
}

// SendWinsize frames and writes a window-size update to conn. It is safe to
// call concurrently with Run.
func (r *Relay) SendWinsize(rows, cols uint16) error {
	return r.writeFrame(frameWinsize, encodeWinsize(rows, cols))
}

// localToConn reads raw bytes from local and emits them as data frames.
func (r *Relay) localToConn() error {
	buf := make([]byte, maxPayload)
	for {
		n, err := r.local.Read(buf)
		if n > 0 {
			if werr := r.writeFrame(frameData, buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// connToLocal reads frames from conn, writing data payloads to local and
// dispatching window-size frames to the handler.
func (r *Relay) connToLocal() error {
	for {
		typ, payload, err := readFrame(r.conn)
		if err != nil {
			return err
		}
		switch typ {
		case frameData:
			if _, werr := r.local.Write(payload); werr != nil {
				return werr
			}
		case frameWinsize:
			rows, cols, derr := decodeWinsize(payload)
			if derr != nil {
				return derr
			}
			if r.onWinsize != nil {
				r.onWinsize(rows, cols)
			}
		default:
			return fmt.Errorf("ptymux: unknown frame type %d", typ)
		}
	}
}

// writeFrame writes a single framed message to conn under writeMu.
func (r *Relay) writeFrame(typ frameType, payload []byte) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return writeFrameTo(r.conn, typ, payload)
}

// writeFrameTo encodes one frame into a single buffer and writes it, so a
// frame is never split across writes.
func writeFrameTo(w io.Writer, typ frameType, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("ptymux: payload of %d bytes exceeds max %d", len(payload), maxPayload)
	}
	frame := make([]byte, 3+len(payload))
	frame[0] = byte(typ)
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(payload)))
	copy(frame[3:], payload)
	_, err := w.Write(frame)
	return err
}

// readFrame reads one frame from r.
func readFrame(r io.Reader) (frameType, []byte, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:3]))
	if n > maxPayload {
		return 0, nil, fmt.Errorf("ptymux: frame length %d exceeds max %d", n, maxPayload)
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return frameType(hdr[0]), payload, nil
}

// encodeWinsize packs rows and cols into a 4-byte payload.
func encodeWinsize(rows, cols uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], rows)
	binary.BigEndian.PutUint16(b[2:4], cols)
	return b
}

// decodeWinsize unpacks a window-size payload produced by encodeWinsize.
func decodeWinsize(b []byte) (rows, cols uint16, err error) {
	if len(b) != 4 {
		return 0, 0, fmt.Errorf("ptymux: winsize payload must be 4 bytes, got %d", len(b))
	}
	return binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4]), nil
}

// isBenign reports whether err is a normal end-of-session condition (a clean
// or abrupt close of one endpoint) rather than a real transport failure.
func isBenign(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe)
}
