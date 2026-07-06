//go:build linux

package coherence

import (
	"context"
	"fmt"
	"time"
)

// GateConfig parameterizes the search for a coherent dump instant.
type GateConfig struct {
	// MaxAttempts bounds how many freeze/inspect cycles the gate runs before
	// giving up. Zero uses defaultMaxAttempts.
	MaxAttempts int
	// Backoff is how long the tree runs thawed between attempts, letting it leave
	// a busy window before the next check. Zero uses defaultBackoff.
	Backoff time.Duration
	// Classifier overrides the fd-classification policy. Nil uses
	// DefaultClassifier.
	Classifier Classifier
}

const (
	defaultMaxAttempts = 100
	defaultBackoff     = 10 * time.Millisecond
)

// ErrNoCoherentInstant is returned when the gate exhausts its attempts without
// finding a coherent instant. It carries the last snapshot so the caller can
// report exactly which fds stood in the way — a fail-loud diagnostic rather than
// a bare timeout.
type ErrNoCoherentInstant struct {
	Attempts int
	Last     Snapshot
}

func (e *ErrNoCoherentInstant) Error() string {
	b := e.Last.Blockers()
	return fmt.Sprintf("no coherent dump instant after %d attempts: %d blocking fd(s)%s",
		e.Attempts, len(b), firstBlockerHint(b))
}

// firstBlockerHint renders a short "e.g. ..." pointer at the first blocker.
func firstBlockerHint(b []Resource) string {
	if len(b) == 0 {
		return ""
	}
	return fmt.Sprintf(", e.g. pid %d fd %s -> %s (%s)", b[0].PID, b[0].FD, b[0].Target, b[0].Verdict)
}

// freezeInspector is the tree-facing capability the gate loop needs: freeze the
// tree and inspect it as one consistent snapshot (leaving it frozen), and thaw
// it. The real implementation is *cgroupProbe; tests supply a scripted fake so
// the retry logic is exercised without a live cgroup.
type freezeInspector interface {
	freezeInspect(ctx context.Context) (Snapshot, error)
	thaw(ctx context.Context) error
}

// cgroupProbe is the production freezeInspector: it freezes a real cgroup, reads
// its members, and inspects their fds under procRoot.
type cgroupProbe struct {
	cg       *Cgroup
	procRoot string
	class    Classifier
}

func (p *cgroupProbe) freezeInspect(ctx context.Context) (Snapshot, error) {
	if err := p.cg.Freeze(ctx); err != nil {
		return Snapshot{}, err
	}
	members, err := p.cg.Members()
	if err != nil {
		return Snapshot{}, err
	}
	return Inspect(p.procRoot, members, p.class), nil
}

func (p *cgroupProbe) thaw(ctx context.Context) error { return p.cg.Thaw(ctx) }

// WaitForCoherentInstant freezes and inspects the tree in cg until it observes a
// coherent instant, which it returns with the tree LEFT FROZEN so the caller can
// checkpoint it at exactly that instant (dump with --freeze-cgroup cg.Path(), or
// Thaw then dump). Between attempts the tree runs thawed for cfg.Backoff. On
// failure the tree is left thawed and the error says why: *ErrNoCoherentInstant
// (carrying the blocking fds) when the budget is spent, or ctx's error on
// cancellation. procRoot is normally "/proc".
func WaitForCoherentInstant(ctx context.Context, cg *Cgroup, procRoot string, cfg GateConfig) (Snapshot, error) {
	return runGate(ctx, &cgroupProbe{cg: cg, procRoot: procRoot, class: cfg.Classifier}, cfg)
}

// runGate is the freeze/inspect/retry core, decoupled from real cgroups via fi.
// It leaves the tree frozen only on the coherent-success return; every failure
// path leaves it thawed.
func runGate(ctx context.Context, fi freezeInspector, cfg GateConfig) (Snapshot, error) {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	backoff := cfg.Backoff
	if backoff <= 0 {
		backoff = defaultBackoff
	}

	var last Snapshot
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		snap, err := fi.freezeInspect(ctx)
		if err != nil {
			_ = fi.thaw(ctx) // freeze state is uncertain after an error; best-effort thaw
			return Snapshot{}, fmt.Errorf("freeze/inspect: %w", err)
		}
		if snap.Coherent() {
			return snap, nil // success: tree left frozen for the caller to dump
		}
		last = snap
		if err := fi.thaw(ctx); err != nil {
			return Snapshot{}, fmt.Errorf("thaw: %w", err)
		}
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return Snapshot{}, &ErrNoCoherentInstant{Attempts: maxAttempts, Last: last}
}
