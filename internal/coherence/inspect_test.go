//go:build linux

package coherence

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// fakeProc writes procRoot/<pid>/stat (with the given ppid) and a set of
// procRoot/<pid>/fd/<n> symlinks whose link text is the fd's target. The comm
// field deliberately contains a space and parentheses to exercise the stat
// parser's "resume after the last ')'" logic.
func fakeProc(t *testing.T, root string, pid, ppid int, fds map[string]string) {
	t.Helper()
	base := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(base, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (weird )(comm) S %d 1 1 0 -1 4194304 0 0", pid, ppid)
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range fds {
		if err := os.Symlink(target, filepath.Join(base, "fd", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInspect_coherentAndBlocked(t *testing.T) {
	root := t.TempDir()
	// pid 100: a shell holding its script, tty, and a pipe — all fine.
	fakeProc(t, root, 100, 1, map[string]string{
		"0": "/dev/pts/2",
		"1": "/dev/pts/2",
		"3": "/root/linpeas.sh",
		"4": "pipe:[9001]",
	})
	// pid 101: a child mid-read of /proc — the blocker.
	fakeProc(t, root, 101, 100, map[string]string{
		"0": "/dev/pts/2",
		"5": "/proc/999/status",
	})

	snap := Inspect(root, []int{100, 101}, nil)
	if snap.Coherent() {
		t.Fatalf("expected snapshot to be incoherent due to /proc fd, blockers=%v", snap.Blockers())
	}
	blk := snap.Blockers()
	if len(blk) != 1 {
		t.Fatalf("want exactly 1 blocker, got %d: %v", len(blk), blk)
	}
	if blk[0].PID != 101 || blk[0].Target != "/proc/999/status" || blk[0].Verdict != VerdictBlock {
		t.Errorf("unexpected blocker: %+v", blk[0])
	}

	// Without the offending child the same tree is coherent.
	if snap := Inspect(root, []int{100}, nil); !snap.Coherent() {
		t.Errorf("expected coherent without the /proc-holding child, blockers=%v", snap.Blockers())
	}
}

func TestInspect_missingProcessSkipped(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 200, 1, map[string]string{"0": "/dev/null"})
	// pid 201 has no directory at all — must be skipped, not error.
	snap := Inspect(root, []int{200, 201}, nil)
	if !snap.Coherent() {
		t.Errorf("expected coherent, got blockers=%v", snap.Blockers())
	}
	if len(snap.Resources) != 1 {
		t.Errorf("expected 1 resource from the present process, got %d", len(snap.Resources))
	}
}

func TestSubtreePIDs(t *testing.T) {
	root := t.TempDir()
	// Tree: 10 -> {11, 12}; 12 -> {13}. Plus an unrelated process 20.
	fakeProc(t, root, 10, 1, nil)
	fakeProc(t, root, 11, 10, nil)
	fakeProc(t, root, 12, 10, nil)
	fakeProc(t, root, 13, 12, nil)
	fakeProc(t, root, 20, 1, nil)

	got, err := SubtreePIDs(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	want := []int{10, 11, 12, 13}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SubtreePIDs(10) = %v, want %v", got, want)
	}

	// A leaf returns just itself.
	if got, _ := SubtreePIDs(root, 13); !reflect.DeepEqual(got, []int{13}) {
		t.Errorf("SubtreePIDs(13) = %v, want [13]", got)
	}
}

func TestSubtreePIDs_excludesZombies(t *testing.T) {
	root := t.TempDir()
	fakeProc(t, root, 10, 1, nil)  // live parent
	fakeProc(t, root, 11, 10, nil) // live child
	// A zombie child of 10 (state "Z"): reapable only once the frozen parent
	// runs, so it must not count as a live descendant to capture.
	base := filepath.Join(root, "12")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte("12 (true) Z 10 1 1 0 -1 0"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SubtreePIDs(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	if !reflect.DeepEqual(got, []int{10, 11}) {
		t.Errorf("SubtreePIDs(10) = %v, want [10 11] (zombie 12 excluded)", got)
	}
}
