//go:build linux

package coherence

import "testing"

func TestDefaultClassifier(t *testing.T) {
	tests := []struct {
		target string
		want   Verdict
	}{
		{"/proc/1234/status", VerdictBlock},
		{"/proc/irq/61", VerdictBlock},
		{"/proc", VerdictBlock},
		{"/sys/class/net/eth0/statistics/rx_bytes", VerdictBlock},
		{"/sys", VerdictBlock},
		{"socket:[45678]", VerdictExternal},
		{"pipe:[12345]", VerdictOK},
		{"anon_inode:[eventpoll]", VerdictOK},
		{"anon_inode:inotify", VerdictOK},
		{"/dev/pts/3", VerdictOK},
		{"/dev/tty", VerdictOK},
		{"/dev/ptmx", VerdictOK},
		{"/dev/null", VerdictOK},
		{"/dev/urandom", VerdictOK},
		{"/root/linpeas.sh", VerdictStage},
		{"/usr/bin/bash", VerdictStage},
		{"/etc/passwd", VerdictStage},
		{"/tmp/x/ghost123 (deleted)", VerdictGhost},
		// boundary cases: a path that merely starts with the letters "proc"/"sys"
		// but is not under /proc or /sys must not be treated as host-coupled.
		{"/proclaim/notproc", VerdictStage},
		{"/system/config", VerdictStage},
	}
	for _, tt := range tests {
		if got := DefaultClassifier(tt.target); got != tt.want {
			t.Errorf("DefaultClassifier(%q) = %v, want %v", tt.target, got, tt.want)
		}
	}
}

func TestVerdictBlocks(t *testing.T) {
	blocking := map[Verdict]bool{
		VerdictOK:       false,
		VerdictStage:    false,
		VerdictGhost:    false,
		VerdictExternal: true,
		VerdictBlock:    true,
	}
	for v, want := range blocking {
		if got := v.Blocks(); got != want {
			t.Errorf("%v.Blocks() = %v, want %v", v, got, want)
		}
	}
}

func TestVerdictString(t *testing.T) {
	cases := map[Verdict]string{
		VerdictOK:       "OK",
		VerdictStage:    "STAGE",
		VerdictGhost:    "GHOST",
		VerdictExternal: "EXTERNAL",
		VerdictBlock:    "BLOCK",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(v), got, want)
		}
	}
}
