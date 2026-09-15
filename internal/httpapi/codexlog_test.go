package httpapi

import (
	"testing"
	"time"
)

// A session whose hooks report is never overruled by its log, and a shell is
// never read as a Codex.
func TestTheRolloutIsReadOnlyForAQuietCodex(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		command  string
		lastHook time.Time
		want     bool
	}{
		{"codex, never reported", "codex", time.Time{}, true},
		{"codex, reported a minute ago", "codex", now.Add(-time.Minute), false},
		{"codex, quiet for an hour", "codex", now.Add(-time.Hour), true},
		{"claude", "claude", time.Time{}, false},
		{"a shell", "bash", time.Time{}, false},
	} {
		if got := readCodexLog(tc.command, tc.lastHook, now); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
