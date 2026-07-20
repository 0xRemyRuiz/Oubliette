//go:build linux

package coherence

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// writableCgroupRoot returns a cgroup v2 directory the test may create child
// cgroups under, discovered by probing our own cgroup and its ancestors for one
// we can mkdir in (systemd user delegation). It skips the test when the host is
// not cgroup v2 or nothing is writable, so `go test ./...` stays green on CI
// without delegation.
func writableCgroupRoot(t *testing.T) string {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs("/sys/fs/cgroup", &st); err != nil || st.Type != unix.CGROUP2_SUPER_MAGIC {
		t.Skip("not a cgroup v2 unified hierarchy; skipping live-cgroup tests")
	}
	rel, err := selfUnifiedCgroup()
	if err != nil {
		t.Skipf("cannot determine self cgroup: %v", err)
	}
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	base := "/sys/fs/cgroup"
	for i := len(segs); i >= 0; i-- {
		dir := filepath.Join(append([]string{base}, segs[:i]...)...)
		probe := filepath.Join(dir, fmt.Sprintf("oubliette-probe-%d", os.Getpid()))
		if err := os.Mkdir(probe, 0o755); err == nil {
			_ = os.Remove(probe)
			return dir
		}
	}
	t.Skip("no writable cgroup v2 delegation; skipping live-cgroup tests")
	return ""
}

func selfUnifiedCgroup() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", fmt.Errorf("no unified (0::) line in /proc/self/cgroup")
}

// newTestCgroup creates a uniquely named cgroup and registers cleanup that kills
// any remaining members (cgroup.kill) and removes it.
func newTestCgroup(t *testing.T) *Cgroup {
	t.Helper()
	root := writableCgroupRoot(t)
	name := fmt.Sprintf("oubliette-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	cg, err := NewCgroup(root, name)
	if err != nil {
		t.Fatalf("NewCgroup: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(cg.Path(), "cgroup.kill"), []byte("1"), 0)
		waitCgroupEmpty(cg, time.Second)
		if err := cg.Close(); err != nil {
			t.Logf("cgroup close: %v", err)
		}
	})
	return cg
}

func waitCgroupEmpty(cg *Cgroup, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if m, err := cg.Members(); err == nil && len(m) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// startNull runs name/args with stdio pointed at /dev/null (so fds 0,1,2 are
// classified OK) and reaps it on cleanup.
func startNull(t *testing.T, attr *syscall.SysProcAttr, name string, args ...string) *exec.Cmd {
	t.Helper()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { null.Close() })
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestCgroup_AddFreezeThaw(t *testing.T) {
	cg := newTestCgroup(t)
	cmd := startNull(t, nil, "sleep", "30")

	if err := cg.Add(cmd.Process.Pid); err != nil {
		t.Fatalf("Add: %v", err)
	}
	members, err := cg.Members()
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt(members, cmd.Process.Pid) {
		t.Fatalf("member pid %d not in %v", cmd.Process.Pid, members)
	}

	ctx := context.Background()
	if err := cg.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if frozen, err := cg.frozen(); err != nil || !frozen {
		t.Fatalf("expected frozen, got frozen=%v err=%v", frozen, err)
	}
	if err := cg.Thaw(ctx); err != nil {
		t.Fatalf("Thaw: %v", err)
	}
	if frozen, _ := cg.frozen(); frozen {
		t.Fatal("expected thawed after Thaw")
	}
}

func TestCgroup_BirthPlacementFD(t *testing.T) {
	cg := newTestCgroup(t)
	cmd := startNull(t, &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cg.FD()}, "sleep", "30")

	members, err := cg.Members()
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt(members, cmd.Process.Pid) {
		t.Fatalf("birth-placed pid %d not in cgroup members %v", cmd.Process.Pid, members)
	}
}

func TestPopulate_capturesTree(t *testing.T) {
	cg := newTestCgroup(t)
	// A 2-node tree: sh parent with a background sleep child, kept alive by wait.
	cmd := startNull(t, &syscall.SysProcAttr{Setpgid: true}, "sh", "-c", "sleep 30 & wait")

	// Wait for the child to appear so Populate has a real tree to capture.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sub, _ := SubtreePIDs("/proc", cmd.Process.Pid); len(sub) >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := Populate(context.Background(), cg, "/proc", cmd.Process.Pid); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	members, err := cg.Members()
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := SubtreePIDs("/proc", cmd.Process.Pid)
	if len(sub) < 2 {
		t.Skip("child did not spawn in time; nothing to assert")
	}
	for _, p := range sub {
		if !containsInt(members, p) {
			t.Errorf("subtree pid %d not captured; members=%v", p, members)
		}
	}
}

func TestPopulate_convergesUnderForkStorm(t *testing.T) {
	cg := newTestCgroup(t)
	// A shell that forks a fresh child in a tight loop — the churn that made the
	// thaw-per-round approach never settle. Populate must still capture it.
	cmd := startNull(t, &syscall.SysProcAttr{Setpgid: true}, "sh", "-c", "while true; do /bin/true; done")
	time.Sleep(50 * time.Millisecond) // let the loop get going

	if err := Populate(context.Background(), cg, "/proc", cmd.Process.Pid); err != nil {
		t.Fatalf("Populate under fork storm: %v", err)
	}
	members, err := cg.Members()
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt(members, cmd.Process.Pid) {
		t.Fatalf("root shell %d not captured; members=%v", cmd.Process.Pid, members)
	}
}

func TestWaitForCoherentInstant_real(t *testing.T) {
	cg := newTestCgroup(t)
	// sleep holds only /dev/null on 0,1,2 — a coherent tree the gate accepts at once.
	startNull(t, &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cg.FD()}, "sleep", "30")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := WaitForCoherentInstant(ctx, cg, "/proc", GateConfig{Backoff: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitForCoherentInstant: %v", err)
	}
	if !snap.Coherent() {
		t.Fatalf("expected coherent, blockers=%v", snap.Blockers())
	}
	if frozen, _ := cg.frozen(); !frozen {
		t.Error("gate must leave the tree frozen on success")
	}
	if err := cg.Thaw(ctx); err != nil {
		t.Fatalf("Thaw after success: %v", err)
	}
}
