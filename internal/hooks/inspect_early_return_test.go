package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// Inspect has two returns and the early one is the fresh install.
//
// The comment on Status.Events in Inspect records this going wrong once: a
// field normalised at the end of the function, an early return above it for
// "no settings file", and a year of passing locally because this machine has a
// ~/.claude/settings.json and never took that branch. CI, with no home
// directory to speak of, took it on the first request.
//
// OpencodePath and OpencodeInstalled were added afterwards, at the end of the
// function, below the same early return. So on a machine with no
// ~/.claude/settings.json -- every fresh install, and any user who has Codex
// or opencode and not Claude Code -- the settings page drew an empty path and
// said opencode was not installed while its plugin sat in place and reporting.
//
// That is the failure red line 3 is about, running backwards: the page's claim
// about hooks comes from reading a file rather than from anything arriving, so
// a wrong claim in either direction produces no error anywhere.
//
// The test gives the process a home with no Claude settings and asks for the
// two fields that live past the early return.
func TestOpencodeIsReportedWithNoClaudeSettingsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("the fixture needs a home with no Claude settings: %v", err)
	}

	st, err := Inspect("/nowhere/vibepanel-report.sh")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	want := filepath.Join(home, ".config", "opencode", "plugin", opencodePluginName)
	if st.OpencodePath != want {
		t.Errorf("OpencodePath = %q, want %q\n(the early return for a missing settings file skipped it)",
			st.OpencodePath, want)
	}

	// And the installed flag, which is the half that can be wrong rather than
	// merely blank: write the plugin, then ask again.
	if err := InstallOpencode(); err != nil {
		t.Fatalf("InstallOpencode: %v", err)
	}
	st, err = Inspect("/nowhere/vibepanel-report.sh")
	if err != nil {
		t.Fatalf("Inspect after install: %v", err)
	}
	if !st.OpencodeInstalled {
		t.Error("OpencodeInstalled is false with the plugin in place and no ~/.claude/settings.json")
	}
}
