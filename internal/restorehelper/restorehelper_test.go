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

func TestOpenPTY(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("pty allocation not available in this environment: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	if !strings.HasPrefix(slave.Name(), "/dev/pts/") {
		t.Errorf("slave name: got %q, want prefix /dev/pts/", slave.Name())
	}

	msg := []byte("hello\n")
	if _, err := master.Write(msg); err != nil {
		t.Fatalf("master.Write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := slave.Read(buf); err != nil {
		t.Fatalf("slave.Read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Errorf("slave got %q, want %q", buf, msg)
	}
}
