//go:build linux

package coherence

import (
	"context"
	"errors"
	"testing"
	"time"
)

// blocker is a snapshot with one blocking fd; clean is an empty (coherent) one.
var (
	dirtySnap = Snapshot{Resources: []Resource{{PID: 1, FD: "5", Target: "/proc/9/status", Verdict: VerdictBlock}}}
	cleanSnap = Snapshot{}
)

type step struct {
	snap Snapshot
	err  error
}

// scriptedFI is a fake freezeInspector that returns a scripted sequence of
// snapshots/errors, clamping to the last step once exhausted, and records how
// often it froze and thawed and whether it is currently frozen.
type scriptedFI struct {
	steps          []step
	i              int
	freezes, thaws int
	frozen         bool
}

func (s *scriptedFI) freezeInspect(context.Context) (Snapshot, error) {
	s.freezes++
	st := s.steps[min(s.i, len(s.steps)-1)]
	s.i++
	if st.err != nil {
		return Snapshot{}, st.err
	}
	s.frozen = true
	return st.snap, nil
}

func (s *scriptedFI) thaw(context.Context) error {
	s.thaws++
	s.frozen = false
	return nil
}

func TestRunGate_coherentFirstTry(t *testing.T) {
	fi := &scriptedFI{steps: []step{{snap: cleanSnap}}}
	snap, err := runGate(context.Background(), fi, GateConfig{Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !snap.Coherent() {
		t.Errorf("expected coherent snapshot")
	}
	if fi.freezes != 1 || fi.thaws != 0 {
		t.Errorf("freezes=%d thaws=%d, want 1/0", fi.freezes, fi.thaws)
	}
	if !fi.frozen {
		t.Errorf("tree must be left FROZEN on success so the caller can dump it")
	}
}

func TestRunGate_retriesThenCoherent(t *testing.T) {
	fi := &scriptedFI{steps: []step{{snap: dirtySnap}, {snap: dirtySnap}, {snap: cleanSnap}}}
	snap, err := runGate(context.Background(), fi, GateConfig{Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !snap.Coherent() {
		t.Errorf("expected coherent snapshot after retries")
	}
	if fi.freezes != 3 || fi.thaws != 2 {
		t.Errorf("freezes=%d thaws=%d, want 3/2", fi.freezes, fi.thaws)
	}
	if !fi.frozen {
		t.Errorf("tree must be left frozen on success")
	}
}

func TestRunGate_exhausted(t *testing.T) {
	fi := &scriptedFI{steps: []step{{snap: dirtySnap}}} // always dirty
	_, err := runGate(context.Background(), fi, GateConfig{MaxAttempts: 4, Backoff: time.Millisecond})
	var noInstant *ErrNoCoherentInstant
	if !errors.As(err, &noInstant) {
		t.Fatalf("want *ErrNoCoherentInstant, got %v", err)
	}
	if noInstant.Attempts != 4 {
		t.Errorf("Attempts = %d, want 4", noInstant.Attempts)
	}
	if len(noInstant.Last.Blockers()) != 1 {
		t.Errorf("expected the carried snapshot to report the blocker")
	}
	if fi.frozen {
		t.Errorf("tree must be left THAWED on exhaustion")
	}
	if fi.freezes != 4 || fi.thaws != 4 {
		t.Errorf("freezes=%d thaws=%d, want 4/4", fi.freezes, fi.thaws)
	}
}

func TestRunGate_freezeInspectErrorThaws(t *testing.T) {
	sentinel := errors.New("freezer wedged")
	fi := &scriptedFI{steps: []step{{err: sentinel}}}
	_, err := runGate(context.Background(), fi, GateConfig{Backoff: time.Millisecond})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want wrapped sentinel, got %v", err)
	}
	if fi.thaws != 1 {
		t.Errorf("expected a best-effort thaw after the error, thaws=%d", fi.thaws)
	}
}

func TestRunGate_contextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fi := &scriptedFI{steps: []step{{snap: dirtySnap}}}
	_, err := runGate(ctx, fi, GateConfig{Backoff: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
