// Package ptybridge relays bytes bidirectionally between two streams, used
// to connect a local terminal (or pty) to the vsock control channel exposed
// by the guest-side restore helper.
package ptybridge

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// Pump copies bytes from a to b and from b to a concurrently. It returns
// once both directions have stopped: either side reaching EOF, an error on
// either side, or ctx being canceled all trigger a shutdown, at which point
// both a and b are closed (if not already) to unblock the other direction.
//
// A shutdown caused by a clean EOF, or by Pump's own closing of a and b to
// unblock a peer, is not reported as an error. The first other error
// encountered, if any, is returned.
func Pump(ctx context.Context, a, b io.ReadWriteCloser) error {
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			a.Close()
			b.Close()
		})
	}
	defer closeBoth()

	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()

	done := make(chan error, 2)
	go func() { _, err := io.Copy(a, b); done <- err }()
	go func() { _, err := io.Copy(b, a); done <- err }()

	var firstErr error
	for i := 0; i < 2; i++ {
		err := <-done
		closeBoth() // unblock the other direction as soon as one side stops
		if err != nil && firstErr == nil && !errors.Is(err, io.EOF) &&
			!errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			firstErr = err
		}
	}
	return firstErr
}
