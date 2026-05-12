// Package config loads and validates the oubliette YAML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ErrInvalidConfig is returned when required configuration fields are absent or malformed.
var ErrInvalidConfig = errors.New("invalid configuration")

// Config holds all parameters for a single migration run.
type Config struct {
	// CRIUPath is the path to the criu binary on the host. Defaults to "criu".
	CRIUPath string `yaml:"criu_path"`
	// LocalDumpDir is the host directory where CRIU writes checkpoint images.
	// Must be writable by the user running oubliette. Defaults to /tmp/oubliette-dump.
	LocalDumpDir string `yaml:"local_dump_dir"`
	// VM holds parameters for connecting to the target guest.
	VM VMConfig `yaml:"vm"`
}

// VMConfig holds SSH credentials and guest-side CRIU parameters.
type VMConfig struct {
	// SSHUser is the username for SSH connections to the guest. Required.
	SSHUser string `yaml:"ssh_user"`
	// SSHKeyPath is the path to the SSH private key. Required.
	SSHKeyPath string `yaml:"ssh_key_path"`
	// SSHPort is the guest SSH port. Defaults to 22.
	SSHPort int `yaml:"ssh_port"`
	// RemoteDumpDir is the path inside the guest where the dump will be placed.
	// Defaults to /tmp/oubliette-dump.
	RemoteDumpDir string `yaml:"remote_dump_dir"`
	// RemoteCRIUPath is the path to the criu binary inside the guest. Defaults to "criu".
	RemoteCRIUPath string `yaml:"remote_criu_path"`
}

// Load reads and parses the YAML config file at path, applies defaults, and
// validates required fields. It returns ErrInvalidConfig (wrapped) on validation failure.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.CRIUPath == "" {
		c.CRIUPath = "criu"
	}
	if c.LocalDumpDir == "" {
		c.LocalDumpDir = "/tmp/oubliette-dump"
	}
	if c.VM.SSHPort == 0 {
		c.VM.SSHPort = 22
	}
	if c.VM.RemoteCRIUPath == "" {
		c.VM.RemoteCRIUPath = "criu"
	}
	if c.VM.RemoteDumpDir == "" {
		c.VM.RemoteDumpDir = "/tmp/oubliette-dump"
	}
}

func (c *Config) validate() error {
	if c.VM.SSHUser == "" {
		return fmt.Errorf("%w: vm.ssh_user is required", ErrInvalidConfig)
	}
	if c.VM.SSHKeyPath == "" {
		return fmt.Errorf("%w: vm.ssh_key_path is required", ErrInvalidConfig)
	}
	return nil
}
