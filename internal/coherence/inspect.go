//go:build linux

package coherence

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Resource is one open file descriptor held by a process in the tree, together
// with how it was classified. Only fds are inspected: they are the surface that
// decides coherence. File-backed memory mappings (shared libraries, the shell
// binary) are always stageable and never block an instant, so they are a
// separate, one-time staging concern rather than part of the per-instant check.
type Resource struct {
	// PID is the process holding the fd.
	PID int
	// FD is the descriptor number under /proc/<pid>/fd.
	FD string
	// Target is the readlink target of the fd (a path, or a "pipe:"/"socket:"/
	// "anon_inode:" pseudo-target, possibly suffixed " (deleted)").
	Target string
	// Verdict is the classification of Target.
	Verdict Verdict
}

// Snapshot is the classified fd state of a (frozen) process tree at one instant.
type Snapshot struct {
	// PIDs are the tree members observed, in inspection order.
	PIDs []int
	// Resources are all classified fds across those members.
	Resources []Resource
}

// Blockers returns the resources whose verdict disqualifies the instant from a
// coherent dump.
func (s Snapshot) Blockers() []Resource {
	var b []Resource
	for _, r := range s.Resources {
		if r.Verdict.Blocks() {
			b = append(b, r)
		}
	}
	return b
}

// Coherent reports whether the snapshot can be dumped and restored in the guest
// without a blocking resource.
func (s Snapshot) Coherent() bool {
	for _, r := range s.Resources {
		if r.Verdict.Blocks() {
			return false
		}
	}
	return true
}

// Inspect reads and classifies every open fd of every pid, resolving procfs
// paths under procRoot (normally "/proc"; injectable for tests). The caller must
// hold the tree frozen so the snapshot is consistent. A process or fd that
// disappears mid-read is skipped rather than erroring, so a racing exit does not
// fail the whole inspection. A nil class uses DefaultClassifier.
func Inspect(procRoot string, pids []int, class Classifier) Snapshot {
	if class == nil {
		class = DefaultClassifier
	}
	snap := Snapshot{PIDs: pids}
	for _, pid := range pids {
		dir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // process exited between membership read and inspection
		}
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join(dir, e.Name()))
			if err != nil {
				continue // fd closed mid-read
			}
			snap.Resources = append(snap.Resources, Resource{
				PID:     pid,
				FD:      e.Name(),
				Target:  target,
				Verdict: class(target),
			})
		}
	}
	return snap
}

// SubtreePIDs returns root and all its descendant pids, discovered by reading
// parent links under procRoot. Order is root-first, depth-first. It is used to
// gather an already-running tree (one not born into a cgroup) so every member
// can be moved into the freezer.
func SubtreePIDs(procRoot string, root int) ([]int, error) {
	children, err := childMap(procRoot)
	if err != nil {
		return nil, err
	}
	var out []int
	seen := map[int]bool{}
	var walk func(int)
	walk = func(p int) {
		if seen[p] {
			return // guard against a malformed/cyclic parent map
		}
		seen[p] = true
		out = append(out, p)
		for _, c := range children[p] {
			walk(c)
		}
	}
	walk(root)
	return out, nil
}

// childMap builds a parent->children index from every process under procRoot by
// reading each one's PPid. Zombie and dead tasks are excluded: a zombie whose
// parent is frozen cannot be reaped and cannot be moved into a cgroup, so if it
// were treated as a live descendant it would block the capture loop forever. It
// holds no resources, and criu captures it via its own tree walk regardless.
func childMap(procRoot string) (map[int][]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	m := map[int][]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // non-pid entry under /proc
		}
		ppid, state, ok := parentAndState(procRoot, pid)
		if !ok || ppid <= 0 || isDeadState(state) {
			continue
		}
		m[ppid] = append(m[ppid], pid)
	}
	return m, nil
}

// isDeadState reports whether a /proc stat state code is a zombie ("Z") or a
// dead/exiting task ("X"/"x").
func isDeadState(state string) bool {
	return state == "Z" || state == "X" || state == "x"
}

// parentAndState reads the parent pid and state code from procRoot/<pid>/stat.
// The comm field (field 2) may itself contain spaces and parentheses, so the
// parse resumes after the final ')': the remaining fields are state, ppid, ....
func parentAndState(procRoot string, pid int) (ppid int, state string, ok bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", false
	}
	s := string(b)
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 || rp+2 >= len(s) {
		return 0, "", false
	}
	fields := strings.Fields(s[rp+2:])
	if len(fields) < 2 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, fields[0], true
}
