//go:build linux

package restorehelper

import (
	"os"
	"strings"
	"testing"
)

func TestCriuRestoreCmd(t *testing.T) {
	slave := &os.File{} // never dereferenced except by identity comparison below
	cmd := criuRestoreCmd("/usr/sbin/criu", "/mnt/falltrap-shared/oubliette-dump", slave)

	if cmd.Path != "/usr/sbin/criu" {
		t.Errorf("Path: got %q, want %q", cmd.Path, "/usr/sbin/criu")
	}
	wantArgs := []string{"/usr/sbin/criu", "restore", "-D", "/mnt/falltrap-shared/oubliette-dump",
		"--shell-job", "--restore-detached", "-v4", "-o", "restore.log"}
	if strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("Args: got %v, want %v", cmd.Args, wantArgs)
	}
	if cmd.Stdin != slave || cmd.Stdout != slave || cmd.Stderr != slave {
		t.Error("expected Stdin/Stdout/Stderr all set to slave")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("expected non-nil SysProcAttr")
	}
	if !cmd.SysProcAttr.Setsid {
		t.Error("expected Setsid to be true")
	}
	if !cmd.SysProcAttr.Setctty {
		t.Error("expected Setctty to be true")
	}
}
