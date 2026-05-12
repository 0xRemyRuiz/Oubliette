// Package vm interacts with KVM guests through the virsh CLI.
//
// virsh is used as a subprocess rather than libvirt-go because libvirt-go
// requires CGO (it wraps libvirt's C library), which this project avoids.
// The trade-off is text-output parsing instead of typed structs, which is
// acceptable given the small virsh surface area used here.
package vm

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

var (
	// ErrDomainNotFound is returned when virsh reports the domain does not exist.
	ErrDomainNotFound = fmt.Errorf("domain not found")
	// ErrDomainNotRunning is returned when the domain exists but is not running.
	ErrDomainNotRunning = fmt.Errorf("domain not running")
	// ErrNoIPv4 is returned when virsh domifaddr reports no IPv4 address.
	ErrNoIPv4 = fmt.Errorf("no IPv4 address found for domain")
)

// Domain represents a libvirt-managed KVM guest identified by its domain name.
type Domain struct {
	// Name is the libvirt domain name as shown by virsh list.
	Name string
}

// LookupDomain verifies that the named domain exists and is currently in the
// running state. It returns ErrDomainNotFound or ErrDomainNotRunning on failure.
func LookupDomain(ctx context.Context, name string) (*Domain, error) {
	out, err := virsh(ctx, "domstate", name)
	if err != nil {
		outLower := strings.ToLower(out)
		if strings.Contains(outLower, "failed to get domain") ||
			strings.Contains(outLower, "domain not found") ||
			strings.Contains(outLower, "no domain") {
			return nil, fmt.Errorf("%w: %q", ErrDomainNotFound, name)
		}
		return nil, fmt.Errorf("virsh domstate %q: %s: %w", name, strings.TrimSpace(out), err)
	}
	state := strings.TrimSpace(out)
	if state != "running" {
		return nil, fmt.Errorf("%w: %q is in state %q", ErrDomainNotRunning, name, state)
	}
	return &Domain{Name: name}, nil
}

// PrimaryIPv4 returns the first IPv4 address assigned to any of the domain's
// network interfaces as reported by virsh domifaddr. It returns ErrNoIPv4 when
// no IPv4 address is present (e.g. only IPv6, or the guest network is not up yet).
func (d *Domain) PrimaryIPv4(ctx context.Context) (string, error) {
	out, err := virsh(ctx, "domifaddr", d.Name)
	if err != nil {
		return "", fmt.Errorf("virsh domifaddr %q: %s: %w", d.Name, strings.TrimSpace(out), err)
	}
	ip, err := parseFirstIPv4(out)
	if err != nil {
		return "", fmt.Errorf("parse domifaddr output for %q: %w", d.Name, err)
	}
	return ip, nil
}

// parseFirstIPv4 parses virsh domifaddr output and returns the first IPv4 address
// without its prefix length. Expected line format (space-separated):
//
//	vnet0  52:54:00:xx:xx:xx  ipv4  192.168.122.10/24
func parseFirstIPv4(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "ipv4" {
			addr, _, _ := strings.Cut(fields[3], "/")
			if addr != "" {
				return addr, nil
			}
		}
	}
	return "", ErrNoIPv4
}

func virsh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "virsh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
