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

// What the settings page draws before anybody presses a button: the file it
// will edit, and the text it will write into it. Both were unreported for
// opencode once -- TestOpencodeIsReportedWithNoClaudeSettingsFile is that bug
// -- and the two agents added since had nothing checking either field, so a
// path and a snippet could both go empty with every test green.
func TestInspectNamesEveryAgentsFileAndSnippet(t *testing.T) {
	withFakeHome(t)
	st, err := Inspect(codexScript)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, tc := range []struct{ agent, path, snippet string }{
		{"claude", st.SettingsPath, st.Snippet},
		{"codex", st.CodexPath, st.CodexSnippet},
		{"kimi", st.KimiPath, st.KimiSnippet},
		{"zcode", st.ZcodePath, st.ZcodeSnippet},
		{"opencode", st.OpencodePath, "n/a"},
	} {
		if tc.path == "" {
			t.Errorf("%s has no file to name on the settings page", tc.agent)
		}
		if tc.snippet == "" {
			t.Errorf("%s shows an empty snippet where what it will write should be", tc.agent)
		}
		if tc.snippet != "n/a" && !strings.Contains(tc.snippet, codexScript) {
			t.Errorf("%s's snippet does not carry the reporter it would install:\n%s", tc.agent, tc.snippet)
		}
	}
}

// This machine's settings.json had four of the panel's five events -- an
// install from before PermissionRequest -- and the settings page said
// installed, so nothing was ever going to add the fifth.
func TestUpgradeAddsTheEventsAnOlderInstallIsMissing(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + command(script, "done") + `"}]}],` +
		`"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"their-own-guard.sh"}]}]}}`
	if err := os.WriteFile(settings, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	added, err := UpgradeClaude(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != len(events)-1 {
		t.Fatalf("added %v, want every event but Stop", added)
	}
	st, _ := Inspect(script)
	if len(st.Events) != len(events) {
		t.Fatalf("after the upgrade %d of %d events are installed", len(st.Events), len(events))
	}
	doc := readJSON(t, settings)
	if doc["model"] != "opus" {
		t.Errorf("the rest of the file was not kept: %v", doc)
	}
	if !strings.Contains(string(mustRead(t, settings)), "their-own-guard.sh") {
		t.Error("the person's own PreToolUse hook was dropped")
	}

	// And a second start changes nothing.
	again, err := UpgradeClaude(script)
	if err != nil || len(again) != 0 {
		t.Fatalf("second upgrade added %v, %v", again, err)
	}
}

// A file the panel was never installed into is not the panel's to edit.
func TestUpgradeLeavesAFileWithoutThePanelsHooksAlone(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	const theirs = `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`
	if err := os.WriteFile(settings, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := UpgradeClaude(script)
	if err != nil || len(added) != 0 {
		t.Fatalf("added %v, %v to a file with no install", added, err)
	}
	if got := string(mustRead(t, settings)); got != theirs {
		t.Fatalf("the file was rewritten:\n%s", got)
	}

	// Nor is a machine with no settings file at all.
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if added, err := UpgradeClaude(script); err != nil || len(added) != 0 {
		t.Fatalf("added %v, %v with no file", added, err)
	}
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("a settings file was created")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A second panel -- a test run from a scratch data directory -- finds the
// production panel's entries by name. They are not its install to upgrade: the
// first version of UpgradeClaude re-pointed all of them at the scratch script.
func TestUpgradeLeavesAnotherPanelsInstallAlone(t *testing.T) {
	home := withFakeHome(t)
	production, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + command(production, "done") + `"}]}]}}`
	if err := os.WriteFile(settings, []byte(theirs), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := Inspect(scratch); !st.Installed {
		t.Fatal("setup: Inspect no longer finds entries by name, so this test is not testing the case it is about")
	}
	added, err := UpgradeClaude(scratch)
	if err != nil || len(added) != 0 {
		t.Fatalf("added %v, %v to another panel's install", added, err)
	}
	if got := string(mustRead(t, settings)); got != theirs {
		t.Fatalf("another panel's install was rewritten:\n%s", got)
	}
}

// The person's own hook in the same entry group as one of the panel's, with a
// matcher. Re-merging drops the whole group; an upgrade on start must not.
func TestUpgradeKeepsAHookSharingAGroupWithOurs(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"their-guard.sh"},{"type":"command","command":"` + command(script, "working") + `"}]}]}}`
	if err := os.WriteFile(settings, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := UpgradeClaude(script)
	if err != nil || len(added) != len(events)-1 {
		t.Fatalf("added %v, %v", added, err)
	}
	doc := readJSON(t, settings)
	groups := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
	first, _ := groups[0].(map[string]any)
	if first["matcher"] != "Bash" || !strings.Contains(string(mustRead(t, settings)), "their-guard.sh") {
		t.Fatalf("the shared group was rewritten: %v", groups)
	}
}

// A settings.json kept in a dotfiles repository and linked into place. Writing
// renames a file over the link.
func TestUpgradeDoesNotReplaceASymlink(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(t.TempDir(), "settings.json")
	partial := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + command(script, "done") + `"}]}]}}`
	if err := os.WriteFile(real, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, settings); err != nil {
		t.Fatal(err)
	}
	if added, err := UpgradeClaude(script); err == nil || len(added) != 0 {
		t.Fatalf("added %v, err %v; want a refusal that says why", added, err)
	}
	if info, err := os.Lstat(settings); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	if string(mustRead(t, real)) != partial {
		t.Fatal("the linked file was changed")
	}
}
