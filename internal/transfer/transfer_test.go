package transfer

import (
	"context"
	"testing"
)

// TestRunCommand_connectionRefused verifies that RunCommand surfaces an error
// when SSH cannot connect. Port 1 is reserved and will be refused on loopback.
func TestRunCommand_connectionRefused(t *testing.T) {
	xfr := &SSHTransfer{
		Host:    "127.0.0.1",
		User:    "nobody",
		KeyPath: "/nonexistent/key",
		Port:    1,
	}
	err := xfr.RunCommand(context.Background(), "true")
	if err == nil {
		t.Fatal("expected error for refused connection, got nil")
	}
}

func TestSSHBaseArgs_structure(t *testing.T) {
	xfr := &SSHTransfer{
		Host:    "10.0.0.1",
		User:    "root",
		KeyPath: "/root/.ssh/id_ed25519",
		Port:    2222,
	}
	args := xfr.sshBaseArgs()

	wantDest := "root@10.0.0.1"
	if len(args) == 0 || args[len(args)-1] != wantDest {
		t.Errorf("last arg: got %q, want %q", args[len(args)-1], wantDest)
	}

	portIdx := -1
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			portIdx = i + 1
			break
		}
	}
	if portIdx == -1 || args[portIdx] != "2222" {
		t.Errorf("-p value: want %q in args %v", "2222", args)
	}
}

func TestSCPBaseArgs_usesUpperP(t *testing.T) {
	xfr := &SSHTransfer{Port: 2222}
	args := xfr.scpBaseArgs()
	for i, a := range args {
		if a == "-P" && i+1 < len(args) && args[i+1] == "2222" {
			return
		}
	}
	t.Errorf("expected -P 2222 in scp args: %v", args)
}
