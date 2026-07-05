// Package qga executes commands inside a libvirt-managed KVM guest via the
// QEMU Guest Agent (QGA), using `virsh qemu-agent-command` as a subprocess.
//
// Unlike SSH, no credentials are required: the guest must expose the
// org.qemu.guest_agent.0 virtio-serial channel and run qemu-guest-agent.
// Trust comes from the caller's libvirt access to the domain — the same
// access already required to look up and start the VM at all.
package qga

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	// ErrGuestAgentUnavailable is returned when the QEMU guest agent channel
	// is not connected in the guest.
	ErrGuestAgentUnavailable = errors.New("qemu guest agent not connected")
	// ErrGuestCommandFailed is returned when a guest command exits non-zero.
	ErrGuestCommandFailed = errors.New("guest command failed")
)

// pollInterval is how often guest-exec-status is polled while a guest
// command is still running.
const pollInterval = 200 * time.Millisecond

// commandTimeout bounds a single virsh qemu-agent-command invocation, in
// seconds, as accepted by virsh's --timeout flag.
const commandTimeout = "10"

// Agent runs commands inside a single libvirt domain via the QEMU guest agent.
type Agent struct {
	// Domain is the libvirt domain name of the target guest.
	Domain string
}

// RunCommand executes args[0] with args[1:] as arguments inside the guest via
// guest-exec and waits for it to complete. It returns ErrGuestAgentUnavailable
// if the guest agent channel is not connected, or ErrGuestCommandFailed
// (wrapping captured stdout/stderr) if the command exits non-zero.
func (a *Agent) RunCommand(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("qga: RunCommand requires at least one argument (the path to execute)")
	}
	pid, err := a.guestExec(ctx, args[0], args[1:])
	if err != nil {
		return err
	}
	status, err := a.waitExec(ctx, pid)
	if err != nil {
		return err
	}
	if status.ExitCode != 0 {
		return commandFailedError(args, status)
	}
	return nil
}

// execStatus mirrors the "return" object of a guest-exec-status response.
type execStatus struct {
	Exited   bool   `json:"exited"`
	ExitCode int    `json:"exitcode"`
	Signal   int    `json:"signal"`
	OutData  string `json:"out-data"`
	ErrData  string `json:"err-data"`
}

// buildExecRequest marshals a guest-exec command for path with args.
func buildExecRequest(path string, args []string) ([]byte, error) {
	req := map[string]any{
		"execute": "guest-exec",
		"arguments": map[string]any{
			"path":           path,
			"arg":            args,
			"capture-output": true,
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("qga: marshal guest-exec request: %w", err)
	}
	return body, nil
}

// buildStatusRequest marshals a guest-exec-status command for pid.
func buildStatusRequest(pid int) ([]byte, error) {
	req := map[string]any{
		"execute":   "guest-exec-status",
		"arguments": map[string]any{"pid": pid},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("qga: marshal guest-exec-status request: %w", err)
	}
	return body, nil
}

// parseExecResponse extracts the guest-side PID from a guest-exec response.
func parseExecResponse(raw []byte) (int, error) {
	var resp struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, fmt.Errorf("qga: parse guest-exec response %q: %w", raw, err)
	}
	return resp.Return.PID, nil
}

// parseStatusResponse extracts the execStatus from a guest-exec-status response.
func parseStatusResponse(raw []byte) (execStatus, error) {
	var resp struct {
		Return execStatus `json:"return"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return execStatus{}, fmt.Errorf("qga: parse guest-exec-status response %q: %w", raw, err)
	}
	return resp.Return, nil
}

// commandFailedError builds ErrGuestCommandFailed with decoded stdout/stderr
// from a non-zero-exit execStatus.
func commandFailedError(args []string, status execStatus) error {
	out, _ := base64.StdEncoding.DecodeString(status.OutData)
	errOut, _ := base64.StdEncoding.DecodeString(status.ErrData)
	return fmt.Errorf("%w: %v: exit %d: stdout=%q stderr=%q",
		ErrGuestCommandFailed, args, status.ExitCode, out, errOut)
}

// classifyAgentError turns a failed virsh invocation's combined output into
// ErrGuestAgentUnavailable when the guest agent channel is not connected, or
// a generic wrapped error otherwise. It returns nil if err is nil.
func classifyAgentError(domain string, out []byte, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "not connected") {
		return fmt.Errorf("%w: domain %q: %s", ErrGuestAgentUnavailable, domain, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("qga: virsh qemu-agent-command: %s: %w", strings.TrimSpace(string(out)), err)
}

// guestExec issues guest-exec and returns the guest-side PID of the started process.
func (a *Agent) guestExec(ctx context.Context, path string, args []string) (int, error) {
	body, err := buildExecRequest(path, args)
	if err != nil {
		return 0, err
	}
	out, err := a.agentCommand(ctx, string(body))
	if err != nil {
		return 0, err
	}
	return parseExecResponse(out)
}

// waitExec polls guest-exec-status until the process has exited or ctx is done.
func (a *Agent) waitExec(ctx context.Context, pid int) (execStatus, error) {
	body, err := buildStatusRequest(pid)
	if err != nil {
		return execStatus{}, err
	}
	for {
		out, err := a.agentCommand(ctx, string(body))
		if err != nil {
			return execStatus{}, err
		}
		status, err := parseStatusResponse(out)
		if err != nil {
			return execStatus{}, err
		}
		if status.Exited {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return execStatus{}, fmt.Errorf("qga: wait for pid %d: %w", pid, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// agentCommand runs `virsh qemu-agent-command` with the given JSON payload
// and returns its stdout.
func (a *Agent) agentCommand(ctx context.Context, jsonCmd string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "virsh", "-c", "qemu:///system",
		"qemu-agent-command", a.Domain, jsonCmd, "--timeout", commandTimeout)
	out, err := cmd.CombinedOutput()
	if err := classifyAgentError(a.Domain, out, err); err != nil {
		return nil, err
	}
	return out, nil
}
