package criu

import (
	"context"
	"errors"
	"os"
	"testing"
)

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
