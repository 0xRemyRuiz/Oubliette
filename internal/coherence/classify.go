//go:build linux

package coherence

import "strings"

// Verdict describes how a resource referenced by the tree will fare when CRIU
// restores the tree inside the shadow VM.
type Verdict int

const (
	// VerdictOK is a resource CRIU reproduces with no host-specific coupling:
	// the controlling tty (recreated by the restore helper), pipes and other
	// anonymous kernel objects internal to the tree, and character devices
	// present in any Linux guest.
	VerdictOK Verdict = iota
	// VerdictStage is a regular file that must exist at the same path in the
	// guest. It does not disqualify an instant: it is arranged once, ahead of
	// time, by staging the file into the guest.
	VerdictStage
	// VerdictGhost is a deleted-but-still-open regular file. CRIU snapshots its
	// contents into the image (bounded by --ghost-limit), so it also does not
	// disqualify an instant.
	VerdictGhost
	// VerdictExternal is a socket or comparable object whose peer may live
	// outside the tree; restoring it needs explicit CRIU --external handling and,
	// absent that, blocks the instant.
	VerdictExternal
	// VerdictBlock is host-coupled kernel state — an fd into /proc or /sys — that
	// cannot be staged, ghosted, or recreated in the guest. Its presence makes
	// the instant un-dumpable-coherently.
	VerdictBlock
)

// String returns the verdict's short upper-case name, for diagnostics.
func (v Verdict) String() string {
	switch v {
	case VerdictOK:
		return "OK"
	case VerdictStage:
		return "STAGE"
	case VerdictGhost:
		return "GHOST"
	case VerdictExternal:
		return "EXTERNAL"
	case VerdictBlock:
		return "BLOCK"
	default:
		return "UNKNOWN"
	}
}

// Blocks reports whether a resource with this verdict disqualifies an instant
// from being dumped coherently.
func (v Verdict) Blocks() bool {
	return v == VerdictBlock || v == VerdictExternal
}

// A Classifier maps an fd target (as read by readlink on /proc/<pid>/fd/<n>) to
// a Verdict. It is a function type so callers can substitute a policy refined
// against the guest's actual CRIU behavior.
type Classifier func(target string) Verdict

// DefaultClassifier is the built-in policy. It is deliberately conservative:
// where CRIU's true capability is uncertain it errs toward a blocking verdict,
// so the gate waits for a cleaner instant rather than committing a dump that
// might fail restore. It never reports a genuinely un-restorable resource as OK.
func DefaultClassifier(target string) Verdict {
	switch {
	case target == "/proc" || strings.HasPrefix(target, "/proc/"),
		target == "/sys" || strings.HasPrefix(target, "/sys/"):
		return VerdictBlock
	case strings.HasPrefix(target, "socket:"):
		return VerdictExternal
	case strings.HasPrefix(target, "pipe:"), strings.HasPrefix(target, "anon_inode:"):
		return VerdictOK
	case strings.HasPrefix(target, "/dev/pts/"), target == "/dev/tty", target == "/dev/ptmx":
		return VerdictOK
	case strings.HasSuffix(target, " (deleted)"):
		return VerdictGhost
	case strings.HasPrefix(target, "/dev/"):
		return VerdictOK
	default:
		return VerdictStage
	}
}
