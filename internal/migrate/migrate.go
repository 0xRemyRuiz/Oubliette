//go:build linux

// Package migrate orchestrates the full process migration sequence:
// preflight checks → CRIU dump (host) → transfer → CRIU restore (guest) → kill source.
//
// The source process is not touched until all preflight checks pass.
// After a successful restore in the guest the source is killed; migration is one-way.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/criu"
	"github.com/oubliette/oubliette/internal/preflight"
	"github.com/oubliette/oubliette/internal/transfer"
	"github.com/oubliette/oubliette/internal/vm"
)

// Run migrates the process identified by pid into the libvirt domain vmName.
// It reads all parameters from cfg and logs progress via slog at INFO level.
// Any failure before the CRIU dump step leaves the source process unmodified.
func Run(ctx context.Context, pid int, vmName string, cfg *config.Config) error {
	slog.InfoContext(ctx, "starting migration", "pid", pid, "vm", vmName)

	domain, err := vm.LookupDomain(ctx, vmName)
	if err != nil {
		return fmt.Errorf("vm lookup: %w", err)
	}
	slog.InfoContext(ctx, "vm is running", "vm", vmName)

	guestIP, err := domain.PrimaryIPv4(ctx)
	if err != nil {
		return fmt.Errorf("resolve guest IP: %w", err)
	}
	slog.InfoContext(ctx, "resolved guest IP", "ip", guestIP)

	xfr := &transfer.SSHTransfer{
		Host:    guestIP,
		User:    cfg.VM.SSHUser,
		KeyPath: cfg.VM.SSHKeyPath,
		Port:    cfg.VM.SSHPort,
	}

	checker := &preflight.Checker{
		Transfer:       xfr,
		RemoteCRIUPath: cfg.VM.RemoteCRIUPath,
	}
	if err := checker.Run(ctx, pid); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	slog.InfoContext(ctx, "preflight passed")

	if err := os.MkdirAll(cfg.LocalDumpDir, 0700); err != nil {
		return fmt.Errorf("create local dump dir: %w", err)
	}

	dumper := &criu.Dumper{CRIUPath: cfg.CRIUPath}
	if err := dumper.Dump(ctx, pid, cfg.LocalDumpDir); err != nil {
		return fmt.Errorf("criu dump: %w", err)
	}
	slog.InfoContext(ctx, "process dumped", "dir", cfg.LocalDumpDir)

	if err := xfr.CopyDump(ctx, cfg.LocalDumpDir, cfg.VM.RemoteDumpDir); err != nil {
		return fmt.Errorf("transfer dump: %w", err)
	}
	slog.InfoContext(ctx, "dump transferred", "remote_dir", cfg.VM.RemoteDumpDir)

	if err := xfr.RemoteRestore(ctx, cfg.VM.RemoteCRIUPath, cfg.VM.RemoteDumpDir); err != nil {
		return fmt.Errorf("remote restore: %w", err)
	}
	slog.InfoContext(ctx, "process restored in guest")

	// Kill the source process. After criu dump it is stopped (SIGSTOP). We only
	// reach this point after confirmed restore in the guest, so it is safe to do so.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.WarnContext(ctx, "failed to kill source process (already gone?)", "pid", pid, "err", err)
	} else {
		slog.InfoContext(ctx, "source process killed", "pid", pid)
	}

	slog.InfoContext(ctx, "migration complete", "pid", pid, "vm", vmName)
	return nil
}
