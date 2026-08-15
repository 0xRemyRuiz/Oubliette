//go:build linux

package preflight

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeRunner is a CommandRunner whose "test -e <path>" fails for paths in missing.
type fakeRunner struct {
	missing map[string]bool
}

func (f *fakeRunner) RunCommand(_ context.Context, args ...string) error {
	if len(args) == 3 && args[0] == "test" && args[1] == "-e" && f.missing[args[2]] {
		return fmt.Errorf("exit status 1")
	}
	return nil
}

// writeFakeProc creates <root>/<pid>/stat and fd symlinks for a fabricated tree.
func writeFakeProc(t *testing.T, root string, pid, ppid int, fds map[string]string) {
	t.Helper()
	base := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(base, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (sh) S %d 1 1 0 -1 0", pid, ppid)
	if err := os.WriteFile(filepath.Join(base, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range fds {
		if err := os.Symlink(target, filepath.Join(base, "fd", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckRemoteOpenFiles_absentFileFailsLoud(t *testing.T) {
	root := t.TempDir()
	fifo := "/run/user/1000/fish_universal_variables.notifier"
	writeFakeProc(t, root, 500, 1, map[string]string{
		"0": "/dev/pts/1",      // OK class, skipped
		"3": "/tmp/linpeas.sh", // STAGE, present on guest
		"4": fifo,              // STAGE, absent on guest -> must fail loud
	})

	// All present: passes.
	ok := &Checker{ProcRoot: root, Transfer: &fakeRunner{}}
	if err := ok.checkRemoteOpenFiles(context.Background(), 500); err != nil {
		t.Errorf("all present: unexpected error: %v", err)
	}

	// FIFO absent on guest: fails, naming the path.
	bad := &Checker{ProcRoot: root, Transfer: &fakeRunner{missing: map[string]bool{fifo: true}}}
	err := bad.checkRemoteOpenFiles(context.Background(), 500)
	if err == nil {
		t.Fatal("expected error for a file absent on the guest, got nil")
	}
	if !strings.Contains(err.Error(), "fish_universal_variables.notifier") {
		t.Errorf("error should name the missing path, got: %v", err)
	}
}

func TestCheckProcess_self(t *testing.T) {
	c := &Checker{}
	if err := c.checkProcess(os.Getpid()); err != nil {
		t.Errorf("checkProcess(self): unexpected error: %v", err)
	}
}

func TestCheckProcess_nonexistent(t *testing.T) {
	c := &Checker{}
	// PID 0 is the idle process and is never a valid target for migration.
	if err := c.checkProcess(0); err == nil {
		t.Error("expected error for PID 0, got nil")
	}
}

func TestProcessCWD_self(t *testing.T) {
	c := &Checker{}
	cwd, err := c.processCWD(os.Getpid())
	if err != nil {
		t.Fatalf("processCWD(self): unexpected error: %v", err)
	}
	if cwd == "" {
		t.Error("expected non-empty cwd for self")
	}
}

func TestProcessCWD_nonexistent(t *testing.T) {
	c := &Checker{}
	_, err := c.processCWD(0)
	if err == nil {
		t.Error("expected error for PID 0, got nil")
	}
}

func TestCheckVsockDevice_ok(t *testing.T) {
	c := &Checker{
		VMName: "falltrap-debian",
		VsockLookup: func(ctx context.Context, name string) (uint32, error) {
			return 42, nil
		},
	}
	if err := c.checkVsockDevice(context.Background()); err != nil {
		t.Errorf("checkVsockDevice: unexpected error: %v", err)
	}
}

func TestCheckVsockDevice_missing(t *testing.T) {
	wantErr := errors.New("no vsock device configured")
	c := &Checker{
		VMName: "falltrap-debian",
		VsockLookup: func(ctx context.Context, name string) (uint32, error) {
			return 0, wantErr
		},
	}
	err := c.checkVsockDevice(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("checkVsockDevice: expected wrapped %v, got %v", wantErr, err)
	}
}
