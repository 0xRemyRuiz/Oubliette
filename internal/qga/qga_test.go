package qga

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBuildExecRequest(t *testing.T) {
	body, err := buildExecRequest("/usr/bin/test", []string{"-d", "/home/falltrap/src"})
	if err != nil {
		t.Fatalf("buildExecRequest: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal built request: %v", err)
	}
	if decoded["execute"] != "guest-exec" {
		t.Errorf("execute: got %v, want guest-exec", decoded["execute"])
	}
	args := decoded["arguments"].(map[string]any)
	if args["path"] != "/usr/bin/test" {
		t.Errorf("path: got %v, want /usr/bin/test", args["path"])
	}
	if args["capture-output"] != true {
		t.Errorf("capture-output: got %v, want true", args["capture-output"])
	}
	arg := args["arg"].([]any)
	if len(arg) != 2 || arg[0] != "-d" || arg[1] != "/home/falltrap/src" {
		t.Errorf("arg: got %v, want [-d /home/falltrap/src]", arg)
	}
}

func TestBuildStatusRequest(t *testing.T) {
	body, err := buildStatusRequest(4242)
	if err != nil {
		t.Fatalf("buildStatusRequest: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal built request: %v", err)
	}
	if decoded["execute"] != "guest-exec-status" {
		t.Errorf("execute: got %v, want guest-exec-status", decoded["execute"])
	}
	args := decoded["arguments"].(map[string]any)
	if args["pid"] != float64(4242) {
		t.Errorf("pid: got %v, want 4242", args["pid"])
	}
}

func TestParseExecResponse(t *testing.T) {
	raw := []byte(`{"return":{"pid":1234}}`)
	pid, err := parseExecResponse(raw)
	if err != nil {
		t.Fatalf("parseExecResponse: %v", err)
	}
	if pid != 1234 {
		t.Errorf("pid: got %d, want 1234", pid)
	}
}

func TestParseExecResponse_malformed(t *testing.T) {
	_, err := parseExecResponse([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
}

func TestParseStatusResponse(t *testing.T) {
	outData := base64.StdEncoding.EncodeToString([]byte("hello\n"))
	raw := []byte(`{"return":{"exited":true,"exitcode":0,"out-data":"` + outData + `"}}`)
	status, err := parseStatusResponse(raw)
	if err != nil {
		t.Fatalf("parseStatusResponse: %v", err)
	}
	if !status.Exited {
		t.Error("expected Exited=true")
	}
	if status.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", status.ExitCode)
	}
	decoded, _ := base64.StdEncoding.DecodeString(status.OutData)
	if string(decoded) != "hello\n" {
		t.Errorf("OutData decoded: got %q, want %q", decoded, "hello\n")
	}
}

func TestParseStatusResponse_stillRunning(t *testing.T) {
	raw := []byte(`{"return":{"exited":false}}`)
	status, err := parseStatusResponse(raw)
	if err != nil {
		t.Fatalf("parseStatusResponse: %v", err)
	}
	if status.Exited {
		t.Error("expected Exited=false")
	}
}

func TestCommandFailedError(t *testing.T) {
	status := execStatus{
		ExitCode: 1,
		OutData:  base64.StdEncoding.EncodeToString([]byte("stdout text")),
		ErrData:  base64.StdEncoding.EncodeToString([]byte("stderr text")),
	}
	err := commandFailedError([]string{"test", "-d", "/nope"}, status)
	if !errors.Is(err, ErrGuestCommandFailed) {
		t.Fatalf("expected ErrGuestCommandFailed, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "stdout text") || !strings.Contains(msg, "stderr text") {
		t.Errorf("error message missing captured output: %q", msg)
	}
}

func TestClassifyAgentError_nilErr(t *testing.T) {
	if err := classifyAgentError("dom", []byte("anything"), nil); err != nil {
		t.Errorf("expected nil for nil input error, got %v", err)
	}
}

func TestClassifyAgentError_notConnected(t *testing.T) {
	out := []byte("error: Guest agent is not connected")
	err := classifyAgentError("falltrap-debian", out, errors.New("exit status 1"))
	if !errors.Is(err, ErrGuestAgentUnavailable) {
		t.Fatalf("expected ErrGuestAgentUnavailable, got %v", err)
	}
}

func TestClassifyAgentError_other(t *testing.T) {
	out := []byte("error: failed to get domain 'falltrap-debian'")
	err := classifyAgentError("falltrap-debian", out, errors.New("exit status 1"))
	if errors.Is(err, ErrGuestAgentUnavailable) {
		t.Error("did not expect ErrGuestAgentUnavailable for an unrelated failure")
	}
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
}
