//go:build linux

package coherence

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// SessionScopeName returns a fresh cgroup name shaped like a systemd session
// scope, unique per call. Uniqueness keeps oubliette's freezer cgroups from
// colliding across runs (including after a crash left one behind), and the
// systemd-like shape keeps them from standing out to a process that inspects
// /proc/self/cgroup.
func SessionScopeName() string {
	return fmt.Sprintf("session-%d.scope", rand.Uint32())
}

// freezePollInterval is how often Freeze/Thaw re-read cgroup.events while waiting
// for the freezer state to settle. Freezing normally completes in well under a
// millisecond, so this keeps the wait tight without busy-spinning.
const freezePollInterval = 200 * time.Microsecond

// Cgroup is a handle to a cgroup v2 node that oubliette created to hold a
// session's process tree. It serves two roles: a birth cgroup the broker spawns
// a shell into (so the whole session tree is captured with no move race), and a
// freezer the coherence gate uses to take consistent snapshots of that tree.
type Cgroup struct {
	path  string // absolute path under the cgroup2 mount
	dirFD int    // O_DIRECTORY fd for CLONE_INTO_CGROUP birth placement; -1 once closed
}

// NewCgroup creates a fresh cgroup named name under the cgroup2 directory root
// (for example a systemd user-delegated node, or a system-level node when
// oubliette runs as root) and opens a directory fd to it. The returned handle's
// Close removes the cgroup.
func NewCgroup(root, name string) (*Cgroup, error) {
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup %s: %w", path, err)
	}
	dirFD, err := unix.Open(path, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("open cgroup dir %s: %w", path, err)
	}
	return &Cgroup{path: path, dirFD: dirFD}, nil
}

// Path returns the cgroup's absolute filesystem path. It is what CRIU takes as
// --freeze-cgroup.
func (c *Cgroup) Path() string { return c.path }

// FD returns an open O_DIRECTORY descriptor to the cgroup, suitable for
// syscall.SysProcAttr's UseCgroupFD/CgroupFD so a child is spawned directly into
// this cgroup (via clone3 CLONE_INTO_CGROUP) with no window in which it lives in
// the parent's cgroup. The Cgroup retains ownership; the fd is closed by Close.
func (c *Cgroup) FD() int { return c.dirFD }

// Add moves pid into the cgroup by writing it to cgroup.procs. In cgroup v2 this
// moves only that task; its existing children are not pulled in, but any child
// it forks afterward is born in the cgroup. Callers adopting an already-running
// tree therefore add every current member and then re-scan (see Populate).
func (c *Cgroup) Add(pid int) error {
	return c.write("cgroup.procs", strconv.Itoa(pid))
}

// Members reads cgroup.procs and returns the pids currently in the cgroup.
func (c *Cgroup) Members() ([]int, error) {
	b, err := os.ReadFile(filepath.Join(c.path, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("read cgroup.procs: %w", err)
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if p, err := strconv.Atoi(f); err == nil {
			pids = append(pids, p)
		}
	}
	return pids, nil
}

// Freeze sets cgroup.freeze and waits until cgroup.events reports the tree
// frozen, or ctx is done. The freeze is atomic over the subtree: a child forked
// during it is born frozen, so nothing escapes a subsequent inspection.
func (c *Cgroup) Freeze(ctx context.Context) error {
	if err := c.write("cgroup.freeze", "1"); err != nil {
		return err
	}
	return c.waitFrozen(ctx, true)
}

// Thaw clears cgroup.freeze and waits until the tree is running again, or ctx is
// done.
func (c *Cgroup) Thaw(ctx context.Context) error {
	if err := c.write("cgroup.freeze", "0"); err != nil {
		return err
	}
	return c.waitFrozen(ctx, false)
}

// Close closes the directory fd and removes the cgroup. It thaws first so any
// remaining member is not left suspended, then removes the (empty) cgroup — the
// normal case, since a successful migration leaves it empty (criu killed the
// tree). If the cgroup is still populated (a failed migration left the tree
// alive), Close relocates those processes to the parent cgroup WITHOUT killing
// them and retries removal, so a failure does not leak the cgroup or strand the
// session. A removal that still fails is returned rather than swallowed.
func (c *Cgroup) Close() error {
	if c.dirFD >= 0 {
		_ = unix.Close(c.dirFD)
		c.dirFD = -1
	}
	_ = c.write("cgroup.freeze", "0") // thaw so members can exit or be moved
	if err := os.Remove(c.path); err == nil {
		return nil
	}
	// Non-empty: move any survivors up to the parent cgroup (leaving them
	// running) so the cgroup can be removed.
	if members, err := c.Members(); err == nil && len(members) > 0 {
		parentProcs := filepath.Join(filepath.Dir(c.path), "cgroup.procs")
		for _, pid := range members {
			_ = os.WriteFile(parentProcs, []byte(strconv.Itoa(pid)), 0)
		}
	}
	if err := os.Remove(c.path); err != nil {
		return fmt.Errorf("remove cgroup %s: %w", c.path, err)
	}
	return nil
}

// waitFrozen polls cgroup.events until its "frozen" line matches want.
func (c *Cgroup) waitFrozen(ctx context.Context, want bool) error {
	for {
		frozen, err := c.frozen()
		if err != nil {
			return err
		}
		if frozen == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(freezePollInterval):
		}
	}
}

// frozen reads the "frozen" state from cgroup.events.
func (c *Cgroup) frozen() (bool, error) {
	b, err := os.ReadFile(filepath.Join(c.path, "cgroup.events"))
	if err != nil {
		return false, fmt.Errorf("read cgroup.events: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		switch strings.TrimSpace(line) {
		case "frozen 1":
			return true, nil
		case "frozen 0":
			return false, nil
		}
	}
	return false, fmt.Errorf("cgroup.events has no frozen state: %q", string(b))
}

// write writes val to the named cgroup control file.
func (c *Cgroup) write(name, val string) error {
	if err := os.WriteFile(filepath.Join(c.path, name), []byte(val), 0); err != nil {
		return fmt.Errorf("write cgroup %s=%q: %w", name, val, err)
	}
	return nil
}
