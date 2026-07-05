//go:build linux

// Command oubliette-restorehelper runs inside the guest and completes a
// migration's restore side: it allocates a pty, restores a CRIU dump onto it
// so criu has a real controlling terminal to inherit under --shell-job, and
// bridges that pty to the host over AF_VSOCK until the restored process
// exits. It is staged into the guest via the shared virtiofs directory and
// launched by the host's `oubliette` process through the QEMU guest agent;
// it is not meant to be run by hand.
//
// Usage:
//
//	oubliette-restorehelper --dump-dir <dir> --criu-path <path> --vsock-port <port> [--log-level <level>]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/oubliette/oubliette/internal/restorehelper"
)

func main() {
	dumpDir := flag.String("dump-dir", "", "directory holding the CRIU dump to restore (required)")
	criuPath := flag.String("criu-path", "", "path to the criu binary inside the guest (required)")
	vsockPort := flag.Uint("vsock-port", 0, "AF_VSOCK port to listen on for the host control channel (required)")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	if *dumpDir == "" || *criuPath == "" || *vsockPort == 0 {
		fmt.Fprintf(os.Stderr, "oubliette-restorehelper: --dump-dir, --criu-path, and --vsock-port are required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	lvl := parseLogLevel(*logLevel)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	cfg := restorehelper.Config{
		DumpDir:   *dumpDir,
		CRIUPath:  *criuPath,
		VsockPort: uint32(*vsockPort),
	}
	if err := restorehelper.Run(context.Background(), cfg); err != nil {
		slog.Error("restore helper failed", "err", err)
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
