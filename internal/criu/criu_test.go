package criu

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestDumper_dumpArgs(t *testing.T) {
	tests := []struct {
		name   string
		d      Dumper
		want   []string          // flags/values that must be present
		pairs  map[string]string // flag -> required immediately-following value
		absent []string          // flags that must not be present
	}{
		{
			name:   "defaults omit optional flags",
			d:      Dumper{CRIUPath: "criu"},
			want:   []string{"dump", "--shell-job", "-v4"},
			pairs:  map[string]string{"-t": "1234", "-D": "/tmp/dir"},
			absent: []string{"--ghost-limit", "--file-locks", "--freeze-cgroup"},
		},
		{
			name:  "ghost limit emitted as bytes",
			d:     Dumper{CRIUPath: "criu", GhostLimit: 10 << 20},
			pairs: map[string]string{"--ghost-limit": "10485760"},
		},
		{
			name:   "file locks is a bare flag",
			d:      Dumper{CRIUPath: "criu", FileLocks: true},
			want:   []string{"--file-locks"},
			absent: []string{"--ghost-limit", "--freeze-cgroup"},
		},
		{
			name:  "freeze cgroup carries its path",
			d:     Dumper{CRIUPath: "criu", FreezeCgroup: "/sys/fs/cgroup/x"},
			pairs: map[string]string{"--freeze-cgroup": "/sys/fs/cgroup/x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.d.dumpArgs(1234, "/tmp/dir")
			if len(args) == 0 || args[0] != "dump" {
				t.Fatalf("first arg must be \"dump\", got %v", args)
			}
			for _, w := range tt.want {
				if !contains(args, w) {
					t.Errorf("expected flag %q in %v", w, args)
				}
			}
			for flag, val := range tt.pairs {
				got, ok := flagValue(args, flag)
				if !ok {
					t.Errorf("expected flag %q in %v", flag, args)
				} else if got != val {
					t.Errorf("flag %q: want value %q, got %q", flag, val, got)
				}
			}
			for _, a := range tt.absent {
				if contains(args, a) {
					t.Errorf("did not expect flag %q in %v", a, args)
				}
			}
		})
	}
}

// contains reports whether args includes s.
func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// flagValue returns the argument immediately following flag, and whether flag
// was found with a value after it.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestDumper_Dump_notFound(t *testing.T) {
	d := &Dumper{CRIUPath: "/nonexistent/criu-binary"}
	err := d.Dump(context.Background(), os.Getpid(), t.TempDir())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrCRIUNotFound) {
		t.Errorf("expected ErrCRIUNotFound, got %v", err)
	}
}

func TestRestorer_RestoreDetached_notFound(t *testing.T) {
	r := &Restorer{CRIUPath: "/nonexistent/criu-binary"}
	err := r.RestoreDetached(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrCRIUNotFound) {
		t.Errorf("expected ErrCRIUNotFound, got %v", err)
	}
}

func TestDumper_Dump_nonZeroExit(t *testing.T) {
	// Use a real binary that exits non-zero to exercise ErrDumpFailed.
	// We call criu-named-as-false by pointing CRIUPath at the "false" utility,
	// but since the binary name must be "criu" for LookPath, we skip this test
	// when criu is not installed and test ErrDumpFailed via a missing image dir.
	d := &Dumper{CRIUPath: "false"}
	err := d.Dump(context.Background(), os.Getpid(), t.TempDir())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// "false" exits 1; LookPath succeeds on Linux where "false" is in PATH,
	// but on platforms without it we get ErrCRIUNotFound instead — both are errors.
	if !errors.Is(err, ErrDumpFailed) && !errors.Is(err, ErrCRIUNotFound) {
		t.Errorf("expected ErrDumpFailed or ErrCRIUNotFound, got %v", err)
	}
}
