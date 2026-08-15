//go:build linux

package broker

import "testing"

func TestTriggerScanner(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		chunks  []string
		want    bool // whether the trigger fires across the chunks
	}{
		{"single chunk match", "linpeas", []string{"./linpeas.sh\n"}, true},
		{"no match", "linpeas", []string{"ls -la\n", "whoami\n"}, false},
		{"split across chunks", "linpeas", []string{"./lin", "peas.sh\n"}, true},
		{"split tight boundary", "trap", []string{"t", "r", "a", "p"}, true},
		{"match then more", "trap", []string{"trap", "extra"}, true},
		{"substring only", "linpeas", []string{"linpea"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &triggerScanner{pat: []byte(tt.pattern)}
			var fired bool
			for _, c := range tt.chunks {
				if sc.feed([]byte(c)) {
					fired = true
					break
				}
			}
			if fired != tt.want {
				t.Errorf("fired = %v, want %v", fired, tt.want)
			}
		})
	}
}

func TestTriggerScannerEmptyPattern(t *testing.T) {
	sc := &triggerScanner{pat: []byte("")}
	if sc.feed([]byte("anything")) {
		t.Error("empty pattern should never fire")
	}
}
