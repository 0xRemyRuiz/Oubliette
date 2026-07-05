//go:build linux

package control

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeContainer records containment calls and can be told to fail.
type fakeContainer struct {
	remoteIP   string
	remotePort int
	pid        int
	err        error
}

func (f *fakeContainer) ContainRemote(ip string, port int) error {
	f.remoteIP, f.remotePort = ip, port
	return f.err
}

func (f *fakeContainer) ContainPID(pid int) error {
	f.pid = pid
	return f.err
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantOK   bool
		checkFn  func(*testing.T, *fakeContainer)
		failWith error
	}{
		{
			name:   "contain by remote",
			line:   `{"action":"contain","match":{"remote_ip":"1.2.3.4","remote_port":44321}}`,
			wantOK: true,
			checkFn: func(t *testing.T, f *fakeContainer) {
				if f.remoteIP != "1.2.3.4" || f.remotePort != 44321 {
					t.Errorf("got %s:%d, want 1.2.3.4:44321", f.remoteIP, f.remotePort)
				}
			},
		},
		{
			name:   "contain by pid",
			line:   `{"action":"contain","pid":1234}`,
			wantOK: true,
			checkFn: func(t *testing.T, f *fakeContainer) {
				if f.pid != 1234 {
					t.Errorf("got pid %d, want 1234", f.pid)
				}
			},
		},
		{name: "unknown action", line: `{"action":"nope"}`, wantOK: false},
		{name: "bad json", line: `{not json`, wantOK: false},
		{name: "missing target", line: `{"action":"contain"}`, wantOK: false},
		{
			name:     "container error",
			line:     `{"action":"contain","pid":7}`,
			wantOK:   false,
			failWith: errFake,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeContainer{err: tt.failWith}
			rep := dispatch(context.Background(), []byte(tt.line), f)
			if rep.OK != tt.wantOK {
				t.Fatalf("OK = %v (err=%q), want %v", rep.OK, rep.Error, tt.wantOK)
			}
			if !tt.wantOK && rep.Error == "" {
				t.Error("expected a non-empty error message on failure")
			}
			if tt.checkFn != nil {
				tt.checkFn(t, f)
			}
			// Replies must marshal cleanly.
			if _, err := json.Marshal(rep); err != nil {
				t.Errorf("marshal reply: %v", err)
			}
		})
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake failure" }
