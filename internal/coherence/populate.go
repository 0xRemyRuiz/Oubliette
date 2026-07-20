//go:build linux

package coherence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// populateMaxRounds bounds the scan rounds Populate runs. Because the tree is
// held frozen throughout, each round can only shrink the set of still-running
// descendants (a frozen process forks nothing), so convergence takes at most
// roughly the depth of the tree; this is generous headroom.
const populateMaxRounds = 16

// Populate moves the process tree rooted at pid into c, capturing every member
// even while the tree forks rapidly. It exists for adopting a tree that is
// already running (the `migrate --pid` path); the broker instead spawns its
// shell into the cgroup at birth, which needs no population.
//
// The capture happens under a held freeze. Populate adds the root, freezes the
// cgroup, then repeatedly scans for descendants still outside it and moves them
// in — each entrant is frozen on arrival and so forks no further children, which
// makes the outside set shrink monotonically to zero. It deliberately does NOT
// thaw between rounds: thawing lets a fork-heavy child (linpeas looping over
// find/grep) resume and outrun the scan, so a thaw-per-round approach never
// settles. On return the whole tree is captured and thawed (running).
func Populate(ctx context.Context, c *Cgroup, procRoot string, pid int) error {
	if err := c.Add(pid); err != nil && !movedRaceLost(err) {
		return fmt.Errorf("add root pid %d to cgroup: %w", pid, err)
	}
	if err := c.Freeze(ctx); err != nil {
		return err
	}
	for round := 0; round < populateMaxRounds; round++ {
		members, err := c.Members()
		if err != nil {
			_ = c.Thaw(ctx)
			return err
		}
		stragglers, err := outsideDescendants(procRoot, pid, members)
		if err != nil {
			_ = c.Thaw(ctx)
			return err
		}
		if len(stragglers) == 0 {
			return c.Thaw(ctx) // whole tree captured; leave it running
		}
		for _, s := range stragglers {
			// Moving a task into the frozen cgroup freezes it too, so it forks no
			// more; a straggler that exited first gives a benign race error.
			if err := c.Add(s); err != nil && !movedRaceLost(err) {
				_ = c.Thaw(ctx)
				return fmt.Errorf("add straggler %d to cgroup: %w", s, err)
			}
		}
	}
	_ = c.Thaw(ctx)
	return fmt.Errorf("tree of %d did not settle into cgroup after %d rounds", pid, populateMaxRounds)
}

// outsideDescendants returns descendants of root (per procRoot) that are not in
// members — stragglers still to be captured.
func outsideDescendants(procRoot string, root int, members []int) ([]int, error) {
	captured := make(map[int]bool, len(members))
	for _, m := range members {
		captured[m] = true
	}
	sub, err := SubtreePIDs(procRoot, root)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, p := range sub {
		if !captured[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// movedRaceLost reports whether err is the benign "the process exited before we
// could move it" case: writing a dead pid to cgroup.procs yields ESRCH (or the
// procfs entry has vanished, ENOENT). Those are expected while adopting a live,
// churning tree and must not fail Populate.
func movedRaceLost(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrNotExist)
}
