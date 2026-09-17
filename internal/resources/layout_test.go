package resources

import (
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

func TestAProcessIsTracedToItsPaneThroughItsEnvironment(t *testing.T) {
	byID := map[string]string{"%3": "vp_a"}
	env := func(kv ...string) []byte {
		out := ""
		for _, s := range kv {
			out += s + "\x00"
		}
		return []byte(out)
	}
	if got := paneFromEnviron(env("TMUX=/tmp/tmux-1000/vibepanel,4242,0", "TMUX_PANE=%3"), 4242, byID); got != "vp_a" {
		t.Errorf("this server's pane: %q", got)
	}
	// Pane ids are per server: %3 of the person's own tmux is somebody else.
	if got := paneFromEnviron(env("TMUX=/tmp/tmux-1000/default,999,0", "TMUX_PANE=%3"), 4242, byID); got != "" {
		t.Errorf("another server's pane: %q", got)
	}
	if got := paneFromEnviron(env("TMUX_PANE=%3"), 4242, byID); got != "" {
		t.Errorf("no TMUX at all: %q", got)
	}
	if got := paneFromEnviron(env("TMUX=/tmp/tmux-1000/vibepanel,4242,0", "TMUX_PANE=%9"), 4242, byID); got != "" {
		t.Errorf("a pane nobody listed: %q", got)
	}
}

func TestOnlyTmuxsOwnPaneScopesBesideOursAreReclaimed(t *testing.T) {
	scope := "/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel-sessions.scope"
	app := "/user.slice/user-1000.slice/user@1000.service/app.slice/"
	cases := map[string]bool{
		app + "tmux-spawn-7d81b4c0-84b6-45db-89f2-346ef9237036.scope": true,
		app + "tmux-spawn-x.service":                                  false,
		app + "vibepanel.service":                                     false,
		"/user.slice/user-1000.slice/session-3.scope":                 false,
		scope + "/pool/s-vp_a":                                        false,
		"/system.slice/tmux-spawn-x.scope":                            false,
	}
	for rel, want := range cases {
		if got := tmuxSpawnBeside(rel, scope); got != want {
			t.Errorf("%s: %v", rel, got)
		}
	}
}

func TestAPaneJustStartedIsYoung(t *testing.T) {
	ticks := uint64(sysmon.ClockTicks)
	p := sysmon.Proc{Start: 1000 * ticks}
	if !youngerThan(p, 1005*ticks, 10*time.Second) {
		t.Error("five seconds old")
	}
	if youngerThan(p, 1011*ticks, 10*time.Second) {
		t.Error("eleven seconds old")
	}
	if youngerThan(p, 0, 10*time.Second) || youngerThan(sysmon.Proc{}, 1005*ticks, 10*time.Second) {
		t.Error("an unknown clock or start is not young")
	}
}
