package vm

import (
	"errors"
	"testing"
)

func TestClassifyDomstateError_notFound(t *testing.T) {
	tests := []string{
		"error: failed to get domain 'falltrap-debian'",
		"error: Domain not found: no domain with matching name 'falltrap-debian'",
		"no domain with matching uuid",
	}
	for _, out := range tests {
		err := classifyDomstateError("falltrap-debian", out, errors.New("exit status 1"))
		if !errors.Is(err, ErrDomainNotFound) {
			t.Errorf("output %q: expected ErrDomainNotFound, got %v", out, err)
		}
	}
}

func TestClassifyDomstateError_other(t *testing.T) {
	err := classifyDomstateError("falltrap-debian", "error: internal error", errors.New("exit status 1"))
	if errors.Is(err, ErrDomainNotFound) {
		t.Error("did not expect ErrDomainNotFound for an unrelated failure")
	}
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
}

func TestParseDomstateOutput_running(t *testing.T) {
	d, err := parseDomstateOutput("falltrap-debian", "running\n")
	if err != nil {
		t.Fatalf("parseDomstateOutput: %v", err)
	}
	if d.Name != "falltrap-debian" {
		t.Errorf("Name: got %q, want %q", d.Name, "falltrap-debian")
	}
}

func TestParseDomstateOutput_notRunning(t *testing.T) {
	_, err := parseDomstateOutput("falltrap-debian", "shut off\n")
	if !errors.Is(err, ErrDomainNotRunning) {
		t.Fatalf("expected ErrDomainNotRunning, got %v", err)
	}
}

const dumpxmlWithVsock = `<domain type='kvm'>
  <name>falltrap-debian</name>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <vsock model='virtio'>
      <cid auto='no' address='42'/>
      <alias name='vsock0'/>
      <address type='pci' domain='0x0000' bus='0x00' slot='0x07' function='0x0'/>
    </vsock>
  </devices>
</domain>`

const dumpxmlWithoutVsock = `<domain type='kvm'>
  <name>falltrap-debian</name>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
  </devices>
</domain>`

func TestParseVsockCID_present(t *testing.T) {
	cid, err := parseVsockCID(dumpxmlWithVsock)
	if err != nil {
		t.Fatalf("parseVsockCID: %v", err)
	}
	if cid != 42 {
		t.Errorf("cid: got %d, want 42", cid)
	}
}

func TestParseVsockCID_missing(t *testing.T) {
	_, err := parseVsockCID(dumpxmlWithoutVsock)
	if !errors.Is(err, ErrNoVsockDevice) {
		t.Fatalf("expected ErrNoVsockDevice, got %v", err)
	}
}

func TestParseVsockCID_malformedXML(t *testing.T) {
	_, err := parseVsockCID("<not-xml")
	if err == nil {
		t.Fatal("expected error for malformed xml, got nil")
	}
}
