//go:build linux

// Command oubliette migrates a running process tree into a KVM guest VM using
// CRIU. The guest must already be running with the QEMU guest agent connected
// and a virtiofs share configured (see vm/debian/create.sh). Migration is
// one-way: the source is killed and the guest holds the live copy.
//
// It has two subcommands:
//
//	oubliette migrate --pid <pid> [--vm <domain>] [--config <path>] [--log-level <level>]
//	    Migrate an existing process into the guest and bridge it to this
//	    terminal (operator-attach).
//
//	oubliette serve --listen <addr> [--shell <path>] [--trigger <str>] [--vm <domain>] [--config <path>] [--log-level <level>]
//	    Front shell sessions: give each client a shell, and when the client's
//	    input contains the trigger, migrate that shell's tree into the guest and
//	    hand the client over to the migrated copy.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/oubliette/oubliette/internal/broker"
	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/migrate"
)

// defaultVMDomain is the libvirt domain name used when --vm is not given.
// It matches FT_DOMAIN in vm/debian/falltrap-env.sh.
const defaultVMDomain = "falltrap-debian"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "migrate":
		runMigrate(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "oubliette: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `oubliette - transparent process migration into a shadow VM

Usage:
  oubliette migrate --pid <pid> [flags]   migrate an existing process, attach to this terminal
  oubliette serve   --listen <addr> [flags]  front shells and trap them into the shadow VM

Run "oubliette <command> -h" for command-specific flags.
`)
}

// runMigrate handles the "migrate" subcommand: operator-attach migration of an
// existing PID.
func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	pid := fs.Int("pid", 0, "PID of the process to migrate (required)")
	vmName := fs.String("vm", defaultVMDomain, "libvirt domain name of the target VM")
	cfgPath := fs.String("config", "oubliette.yaml", "path to YAML config file")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	_ = fs.Parse(args)

	if *pid == 0 || *vmName == "" {
		fmt.Fprintf(os.Stderr, "oubliette migrate: --pid is required\n\n")
		fs.Usage()
		os.Exit(1)
	}

	setupLogging(*logLevel)
	cfg := loadConfig(*cfgPath)

	if err := migrate.Run(context.Background(), *pid, *vmName, cfg); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

// runServe handles the "serve" subcommand: the broker that fronts shells and
// traps them into the shadow VM.
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":2222", "TCP address to accept sessions on")
	shell := fs.String("shell", "/bin/bash", "shell to spawn for each session")
	trigger := fs.String("trigger", "linpeas", "substring in client input that springs the trap")
	vmName := fs.String("vm", defaultVMDomain, "libvirt domain name of the shadow VM")
	controlSock := fs.String("control", "", "path to a Unix control socket for external triggers (empty = disabled)")
	cfgPath := fs.String("config", "oubliette.yaml", "path to YAML config file")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	_ = fs.Parse(args)

	if *vmName == "" || *trigger == "" {
		fmt.Fprintf(os.Stderr, "oubliette serve: --vm and --trigger are required\n\n")
		fs.Usage()
		os.Exit(1)
	}

	setupLogging(*logLevel)
	cfg := loadConfig(*cfgPath)

	brk := broker.Config{
		Listen:        *listen,
		Shell:         *shell,
		Trigger:       *trigger,
		VMName:        *vmName,
		Migration:     cfg,
		ControlSocket: *controlSock,
	}
	if err := broker.Serve(context.Background(), brk); err != nil {
		slog.Error("broker exited", "err", err)
		os.Exit(1)
	}
}

func setupLogging(level string) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(level)})))
}

func loadConfig(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		slog.Error("failed to load config", "path", path, "err", err)
		os.Exit(1)
	}
	return cfg
}

func parseLogLevel(s string) slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return lvl
}
