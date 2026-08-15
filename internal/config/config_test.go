package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := "vm:\n  shared_dir_host: /home/nyro/.local/share/falltrap/shared\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"CRIUPath", cfg.CRIUPath, "criu"},
		{"LocalDumpDir", cfg.LocalDumpDir, "/tmp/oubliette-dump"},
		{"SharedDirGuest", cfg.VM.SharedDirGuest, "/mnt/falltrap-shared"},
		{"RemoteCRIUPath", cfg.VM.RemoteCRIUPath, "/usr/sbin/criu"},
		{"RemoteDumpDir", cfg.VM.RemoteDumpDir, "oubliette-dump"},
		{"GhostLimit", cfg.GhostLimit, int64(10 << 20)},
		{"GateDisabled", cfg.Gate.Disabled, false},
		{"GateCgroupRoot", cfg.Gate.CgroupRoot, "/sys/fs/cgroup"},
		{"GateDumpRetries", cfg.Gate.DumpRetries, 3},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoad_explicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `
criu_path: /usr/sbin/criu
local_dump_dir: /var/run/dump
vm:
  shared_dir_host: /home/nyro/.local/share/falltrap/shared
  shared_dir_guest: /mnt/custom-shared
  remote_dump_dir: custom-dump
  remote_criu_path: /usr/sbin/criu
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VM.SharedDirGuest != "/mnt/custom-shared" {
		t.Errorf("SharedDirGuest: got %q, want %q", cfg.VM.SharedDirGuest, "/mnt/custom-shared")
	}
	if cfg.VM.SharedDirHost != "/home/nyro/.local/share/falltrap/shared" {
		t.Errorf("SharedDirHost: got %q, want %q", cfg.VM.SharedDirHost, "/home/nyro/.local/share/falltrap/shared")
	}
	if cfg.VM.RemoteDumpDir != "custom-dump" {
		t.Errorf("RemoteDumpDir: got %q, want %q", cfg.VM.RemoteDumpDir, "custom-dump")
	}
}

func TestLoad_missingSharedDirHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("vm:\n  shared_dir_guest: /mnt/falltrap-shared\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoad_badPath(t *testing.T) {
	_, err := Load("/nonexistent/oubliette.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
}

func TestLoad_badYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":::\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}
