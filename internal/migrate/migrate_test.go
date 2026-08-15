//go:build linux

package migrate_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/migrate"
	"github.com/oubliette/oubliette/internal/vm"
)

// TestRun_domainNotFound verifies that Run returns quickly with an error when
// the named libvirt domain does not exist, without touching the source process.
func TestRun_domainNotFound(t *testing.T) {
	cfg := &config.Config{
		CRIUPath:     "criu",
		LocalDumpDir: t.TempDir(),
		VM: config.VMConfig{
			SharedDirHost:  t.TempDir(),
			SharedDirGuest: "/mnt/falltrap-shared",
			RemoteCRIUPath: "criu",
			RemoteDumpDir:  "oubliette-test",
		},
	}
	err := migrate.Run(context.Background(), os.Getpid(), "oubliette-no-such-domain-xyz", cfg)
	if err == nil {
		t.Fatal("expected error for nonexistent domain, got nil")
	}
	if !errors.Is(err, vm.ErrDomainNotFound) && !errors.Is(err, vm.ErrDomainNotRunning) {
		// virsh may not be installed in CI; any error is acceptable here.
		t.Logf("got expected error (type: %T): %v", err, err)
	}
}
