//go:build linux

// Command oubliette migrates a running process into a KVM guest VM using CRIU.
// The guest must already be running and accessible via SSH. The migration is
// one-way: on success the source process is killed and the guest holds the live copy.
//
// Usage:
//
//	oubliette --pid <pid> --vm <domain-name> [--config <path>] [--log-level <level>]
//
// Flags:
//
//	--pid        PID of the process to migrate (required)
//	--vm         libvirt domain name of the target VM (required)
//	--config     path to the YAML config file (default: oubliette.yaml)
//	--log-level  log verbosity: debug, info, warn, error (default: info)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/migrate"
)

func main() {
	pid := flag.Int("pid", 0, "PID of the process to migrate (required)")
	vmName := flag.String("vm", "", "libvirt domain name of the target VM (required)")
	cfgPath := flag.String("config", "oubliette.yaml", "path to YAML config file")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	if *pid == 0 || *vmName == "" {
		fmt.Fprintf(os.Stderr, "oubliette: --pid and --vm are required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	lvl := parseLogLevel(*logLevel)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("failed to load config", "path", *cfgPath, "err", err)
		os.Exit(1)
	}

	if err := migrate.Run(context.Background(), *pid, *vmName, cfg); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

func parseLogLevel(s string) slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return lvl
}
