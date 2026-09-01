package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Claude Code runs the "command" field through a shell, so the installer has to
// write one a shell reads as a single word.
//
// `--data-dir "/opt/my panel"` wrote `/opt/my panel/hooks/vibepanel-report.sh
// waiting`, which exits 127 — and nothing said so. report.sh suppresses its own
// failures, Inspect matched on the marker and reported all four events
// installed, and every session fell back to the heuristic with the settings page
// saying reporting was on.
func TestAHookCommandIsRunnableFromADirectoryWithASpaceInIt(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(filepath.Join(home, "my panel", "hooks"))
	if err != nil {
		t.Fatalf("InstallScript: %v", err)
	}
	st, err := InstallClaude(script)
	if err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}
	if !st.Installed {
		t.Fatalf("nothing was installed: %+v", st)
	}

	// The command exactly as it sits in the file the agent reads.
	doc := readJSON(t, st.SettingsPath)
	hooks, _ := doc["hooks"].(map[string]any)
	list, _ := hooks["Notification"].([]any)
	if len(list) == 0 {
		t.Fatalf("no Notification entry was written: %v", doc)
	}
	entry, _ := list[0].(map[string]any)
	inner, _ := entry["hooks"].([]any)
	hook, _ := inner[0].(map[string]any)
	cmd, _ := hook["command"].(string)
	if cmd == "" {
		t.Fatalf("no command in the installed entry: %v", entry)
	}

	// Run it the way a hook runner does. report.sh exits 0 and does nothing
	// outside the panel, so anything but 0 is the shell failing to find it.
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Errorf("the installed hook is not runnable: sh -c %q: %v: %s", cmd, err, out)
	}

	// And it is still recognisably ours, so uninstalling gets it back out.
	after, err := UninstallClaude(script)
	if err != nil {
		t.Fatalf("UninstallClaude: %v", err)
	}
	if after.Installed {
		t.Errorf("a quoted command was not recognised as ours on the way out: %+v", after)
	}
}

// Quoted only when it has to be: the snippet the settings page shows is built
// separately from the command the installer writes, and the promise there is
// that what you read before pressing install is what gets merged.
func TestAnOrdinaryPathIsWrittenExactlyAsItIs(t *testing.T) {
	const script = "/home/jmr/.local/share/vibepanel/hooks/vibepanel-report.sh"
	if got, want := command(script, "waiting"), script+" waiting"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if !strings.Contains(ClaudeSettings(script), `"command": "`+command(script, "waiting")+`"`) {
		t.Errorf("the snippet and the installer disagree for an ordinary path:\n%s",
			ClaudeSettings(script))
	}
}

// Two edits of ~/.claude/settings.json at once must both survive, and must not
// meet in the middle.
//
// The settings page's hooks row and its tune row carry separate busy flags, and
// the first-run tour presses one and then the other, so these overlap on an
// ordinary first run. Unsynchronised, each wrote a document built from a read
// taken before the other's write and both staged through the same temp path: 200
// runs gave one correct file, 177 with an edit silently gone and 22 that
// json.Unmarshal refuses -- and Claude Code does not start on that last one,
// which is what writeSettings' atomic rename is there to prevent.
func TestAnInstallAndATuneAtTheSameTimeBothSurvive(t *testing.T) {
	for i := 0; i < 50; i++ {
		home := withFakeHome(t)
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(home, ".claude", "settings.json")
		if err := os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		script := filepath.Join(home, "hooks", "vibepanel-report.sh")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = InstallClaude(script) }()
		go func() { defer wg.Done(); _, _ = ApplyTune() }()
		wg.Wait()

		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("run %d left a settings.json the agent cannot read (%v):\n%s", i, err, b)
		}
		if doc["model"] != "opus" {
			t.Fatalf("run %d lost the user's own key:\n%s", i, b)
		}
		if _, ok := doc["hooks"]; !ok {
			t.Fatalf("run %d: the tune discarded the install:\n%s", i, b)
		}
		if _, ok := doc["includeCoAuthoredBy"]; !ok {
			t.Fatalf("run %d: the install discarded the tune:\n%s", i, b)
		}
	}
}
