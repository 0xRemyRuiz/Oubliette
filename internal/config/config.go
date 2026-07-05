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

// VMConfig holds the virtiofs share layout and guest-side CRIU parameters
// used to reach a guest through the QEMU guest agent instead of SSH.
type VMConfig struct {
	// SharedDirHost is the host-side path to the virtiofs share root that is
	// mounted inside the guest (see vm/debian/create.sh). Required.
	SharedDirHost string `yaml:"shared_dir_host"`
	// SharedDirGuest is the mount point of that same share inside the guest.
	// Defaults to /mnt/falltrap-shared.
	SharedDirGuest string `yaml:"shared_dir_guest"`
	// RemoteDumpDir is the subdirectory name, relative to the shared dir,
	// where the dump is placed. Defaults to "oubliette-dump".
	RemoteDumpDir string `yaml:"remote_dump_dir"`
	// RemoteCRIUPath is the path to the criu binary inside the guest. Must be
	// absolute: guest-exec's "test -x" check does not perform a $PATH search
	// on its argument. Defaults to "/usr/sbin/criu" (the Debian package's
	// install location).
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
	if c.VM.SharedDirGuest == "" {
		c.VM.SharedDirGuest = "/mnt/falltrap-shared"
	}
	if c.VM.RemoteCRIUPath == "" {
		c.VM.RemoteCRIUPath = "/usr/sbin/criu"
	}
	if c.VM.RemoteDumpDir == "" {
		c.VM.RemoteDumpDir = "oubliette-dump"
	}
}

func (c *Config) validate() error {
	if c.VM.SharedDirHost == "" {
		return fmt.Errorf("%w: vm.shared_dir_host is required", ErrInvalidConfig)
	}
	return nil
}
