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
	content := "vm:\n  ssh_user: root\n  ssh_key_path: /root/.ssh/id_rsa\n"
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
		{"SSHPort", cfg.VM.SSHPort, 22},
		{"RemoteCRIUPath", cfg.VM.RemoteCRIUPath, "criu"},
		{"RemoteDumpDir", cfg.VM.RemoteDumpDir, "/tmp/oubliette-dump"},
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
  ssh_user: migrate
  ssh_key_path: /home/migrate/.ssh/id_ed25519
  ssh_port: 2222
  remote_dump_dir: /var/run/dump
  remote_criu_path: /usr/sbin/criu
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VM.SSHPort != 2222 {
		t.Errorf("SSHPort: got %d, want 2222", cfg.VM.SSHPort)
	}
	if cfg.VM.SSHUser != "migrate" {
		t.Errorf("SSHUser: got %q, want %q", cfg.VM.SSHUser, "migrate")
	}
}

func TestLoad_missingSSHUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("vm:\n  ssh_key_path: /key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoad_missingSSHKeyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("vm:\n  ssh_user: root\n"), 0600); err != nil {
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
