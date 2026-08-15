// Package coherence decides when a process tree can be checkpointed such that
// CRIU can restore it inside the shadow VM, and drives the tree to such an
// instant.
//
// A migrated tree carries open files and kernel objects. On restore in the
// guest, CRIU reopens each by path or recreates it; anything that cannot be
// reproduced there — an fd into host-specific /proc or /sys state, a socket to
// an external peer — fails the restore and kills the migrated tree. This
// package freezes the tree with the cgroup v2 freezer, inspects a consistent
// snapshot of every member's open fds, and classifies each referenced resource.
// When the snapshot holds no un-reproducible resource the instant is "coherent"
// and safe to dump; when it does, the gate thaws, waits, and retries, so a
// fork-heavy script is checkpointed in one of the brief windows where it is
// dumpable rather than at an arbitrary, doomed instant.
//
// The freeze is atomic over the whole subtree, including any child forked during
// the freeze, so nothing escapes inspection. A failed search is fully
// non-destructive: the tree is thawed and continues exactly as before, honoring
// the project rule that a migration either starts cleanly or does not start.
package coherence
