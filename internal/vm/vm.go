// Package vm interacts with KVM guests through the virsh CLI.
//
// virsh is used as a subprocess rather than libvirt-go because libvirt-go
// requires CGO (it wraps libvirt's C library), which this project avoids.
// The trade-off is text-output parsing instead of typed structs, which is
// acceptable given the small virsh surface area used here.
package vm

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

var (
	// ErrDomainNotFound is returned when virsh reports the domain does not exist.
	ErrDomainNotFound = fmt.Errorf("domain not found")
	// ErrDomainNotRunning is returned when the domain exists but is not running.
	ErrDomainNotRunning = fmt.Errorf("domain not running")
	// ErrNoVsockDevice is returned when the domain has no virtio-vsock device
	// configured, so it has no AF_VSOCK CID to reach its restore helper on.
	ErrNoVsockDevice = fmt.Errorf("no vsock device configured")
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
		return nil, classifyDomstateError(name, out, err)
	}
	return parseDomstateOutput(name, out)
}

// classifyDomstateError turns a failed `virsh domstate` invocation's
// combined output into ErrDomainNotFound when virsh reports the domain does
// not exist, or a generic wrapped error otherwise.
func classifyDomstateError(name, out string, err error) error {
	outLower := strings.ToLower(out)
	if strings.Contains(outLower, "failed to get domain") ||
		strings.Contains(outLower, "domain not found") ||
		strings.Contains(outLower, "no domain") {
		return fmt.Errorf("%w: %q", ErrDomainNotFound, name)
	}
	return fmt.Errorf("virsh domstate %q: %s: %w", name, strings.TrimSpace(out), err)
}

// parseDomstateOutput turns successful `virsh domstate` output into a
// Domain, or ErrDomainNotRunning if the domain exists but isn't running.
func parseDomstateOutput(name, out string) (*Domain, error) {
	state := strings.TrimSpace(out)
	if state != "running" {
		return nil, fmt.Errorf("%w: %q is in state %q", ErrDomainNotRunning, name, state)
	}
	return &Domain{Name: name}, nil
}

// domainDevicesXML captures just enough of `virsh dumpxml`'s output to reach
// the vsock device; encoding/xml ignores every other element in the document.
type domainDevicesXML struct {
	Devices struct {
		Vsock struct {
			CID struct {
				Address string `xml:"address,attr"`
			} `xml:"cid"`
		} `xml:"vsock"`
	} `xml:"devices"`
}

// VsockCID returns the AF_VSOCK context ID of the named domain's virtio-vsock
// device, used to dial its restore helper's control channel. Returns
// ErrNoVsockDevice if the domain has no vsock device configured at all.
func VsockCID(ctx context.Context, name string) (uint32, error) {
	out, err := virsh(ctx, "dumpxml", name)
	if err != nil {
		return 0, fmt.Errorf("virsh dumpxml %q: %s: %w", name, strings.TrimSpace(out), err)
	}
	return parseVsockCID(out)
}

// parseVsockCID extracts the vsock CID from `virsh dumpxml` output.
func parseVsockCID(domainXML string) (uint32, error) {
	var dom domainDevicesXML
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return 0, fmt.Errorf("parse domain xml: %w", err)
	}
	addr := dom.Devices.Vsock.CID.Address
	if addr == "" {
		return 0, ErrNoVsockDevice
	}
	cid, err := strconv.ParseUint(addr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse vsock cid %q: %w", addr, err)
	}
	return uint32(cid), nil
}

// libvirtSystemURI is the connection URI for the system-wide libvirtd
// instance. Domains created by this project's dev scripts (vm/debian/*)
// are always registered here, not under the per-user qemu:///session
// instance that virsh falls back to by default for non-root callers.
const libvirtSystemURI = "qemu:///system"

func virsh(ctx context.Context, args ...string) (string, error) {
	fullArgs := append([]string{"-c", libvirtSystemURI}, args...)
	cmd := exec.CommandContext(ctx, "virsh", fullArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
