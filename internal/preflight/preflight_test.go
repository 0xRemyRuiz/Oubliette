//go:build linux

package preflight

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCheckProcess_self(t *testing.T) {
	c := &Checker{}
	if err := c.checkProcess(os.Getpid()); err != nil {
		t.Errorf("checkProcess(self): unexpected error: %v", err)
	}
}

func TestCheckProcess_nonexistent(t *testing.T) {
	c := &Checker{}
	// PID 0 is the idle process and is never a valid target for migration.
	if err := c.checkProcess(0); err == nil {
		t.Error("expected error for PID 0, got nil")
	}
}

func TestProcessCWD_self(t *testing.T) {
	c := &Checker{}
	cwd, err := c.processCWD(os.Getpid())
	if err != nil {
		t.Fatalf("processCWD(self): unexpected error: %v", err)
	}
	if cwd == "" {
		t.Error("expected non-empty cwd for self")
	}
}

func TestProcessCWD_nonexistent(t *testing.T) {
	c := &Checker{}
	_, err := c.processCWD(0)
	if err == nil {
		t.Error("expected error for PID 0, got nil")
	}
}

func TestCheckVsockDevice_ok(t *testing.T) {
	c := &Checker{
		VMName: "falltrap-debian",
		VsockLookup: func(ctx context.Context, name string) (uint32, error) {
			return 42, nil
		},
	}
	if err := c.checkVsockDevice(context.Background()); err != nil {
		t.Errorf("checkVsockDevice: unexpected error: %v", err)
	}
}

func TestCheckVsockDevice_missing(t *testing.T) {
	wantErr := errors.New("no vsock device configured")
	c := &Checker{
		VMName: "falltrap-debian",
		VsockLookup: func(ctx context.Context, name string) (uint32, error) {
			return 0, wantErr
		},
	}
	err := c.checkVsockDevice(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("checkVsockDevice: expected wrapped %v, got %v", wantErr, err)
	}
}
