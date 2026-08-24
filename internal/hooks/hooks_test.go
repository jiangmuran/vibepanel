package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSettingsIsValidJSON(t *testing.T) {
	// This is printed for a human to paste into their own settings file. A
	// snippet that does not parse is worse than no snippet: they will paste it,
	// their agent will fail to start, and nothing will say why.
	out := ClaudeSettings("/tmp/vibepanel-report.sh")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered settings do not parse: %v\n%s", err, out)
	}
	hooksBlock, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks block: %s", out)
	}
	for _, event := range []string{"Notification", "Stop", "UserPromptSubmit", "PreToolUse"} {
		if _, ok := hooksBlock[event]; !ok {
			t.Errorf("missing %s hook", event)
		}
	}
}

func TestScriptNoOpsOutsideThePanel(t *testing.T) {
	// Installed globally, this runs on every agent the user starts anywhere.
	// It must do nothing, quickly and silently, when it is not ours.
	dir := t.TempDir()
	path, err := InstallScript(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	cmd := exec.Command(path, "waiting")
	// Deliberately no VIBEPANEL_* variables, and no network reachable.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("script failed outside the panel: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("script printed something outside the panel: %q", out)
	}
}

func TestScriptRejectsAnUnknownState(t *testing.T) {
	dir := t.TempDir()
	path, err := InstallScript(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd := exec.Command(path, "banana")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"VIBEPANEL_SESSION_ID=x",
		"VIBEPANEL_TOKEN=y",
		// Unroutable: if the state check fails to stop it, the curl attempt is
		// what this would hang on.
		"VIBEPANEL_URL=http://127.0.0.1:1",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("script failed on an unknown state: %v\n%s", err, out)
	}
}

func TestInstallIsIdempotentAndExecutable(t *testing.T) {
	dir := t.TempDir()
	first, err := InstallScript(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	second, err := InstallScript(dir)
	if err != nil {
		t.Fatalf("Install twice: %v", err)
	}
	if first != second {
		t.Errorf("paths differ: %q vs %q", first, second)
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("script is not executable: %v", info.Mode())
	}
	// Owner only: the data directory holds credentials, and this script is
	// handed the hook token by the panel.
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("script is group/world accessible: %v", info.Mode())
	}
	if filepath.Dir(first) != dir {
		t.Errorf("installed outside the requested directory: %q", first)
	}
}

func TestCodexNotifyMentionsTheScript(t *testing.T) {
	out := CodexNotify("/tmp/vibepanel-report.sh")
	if !strings.Contains(out, "/tmp/vibepanel-report.sh") || !strings.Contains(out, "notify") {
		t.Errorf("unexpected codex snippet: %q", out)
	}
}

// withFakeHome points HOME at a temporary directory so the tests never touch
// the developer's real ~/.claude/settings.json.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
	return doc
}

func TestInstallCreatesSettingsWhenThereAreNone(t *testing.T) {
	home := withFakeHome(t)
	script, err := InstallScript(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	st, err := InstallClaude(script)
	if err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}
	if !st.Installed || len(st.Events) != 4 {
		t.Fatalf("status = %+v, want four events installed", st)
	}
	doc := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	if _, ok := doc["hooks"]; !ok {
		t.Errorf("no hooks block: %+v", doc)
	}
}

func TestInstallKeepsEverythingElseInTheFile(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real settings file has other things in it, and one of them is another
	// person's hook on the same event.
	existing := `{
	  "model": "opus",
	  "theme": "dark",
	  "hooks": {
	    "Stop": [
	      { "hooks": [ { "type": "command", "command": "/usr/local/bin/my-own-thing" } ] }
	    ]
	  }
	}`
	if err := os.WriteFile(settings, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatalf("InstallClaude: %v", err)
	}

	doc := readJSON(t, settings)
	if doc["model"] != "opus" || doc["theme"] != "dark" {
		t.Errorf("unrelated settings were lost: %+v", doc)
	}
	stop := doc["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop has %d entries, want the user's plus ours", len(stop))
	}
	if !strings.Contains(fmt.Sprint(stop[0]), "my-own-thing") {
		t.Errorf("the user's own hook is not first: %+v", stop)
	}
}

func TestInstallBacksUpBeforeEditing(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}

	// This file is the user's. Editing it without a copy beside it is not a
	// thing to do to somebody's configuration.
	entries, err := os.ReadDir(filepath.Dir(settings))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "vibepanel-backup") {
			found = true
		}
	}
	if !found {
		t.Errorf("no backup was written: %v", entries)
	}
}

func TestUninstallLeavesTheUsersOwnHooksAlone(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/bin/my-own-thing"}]}]}}`
	if err := os.WriteFile(settings, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}

	st, err := UninstallClaude(script)
	if err != nil {
		t.Fatalf("UninstallClaude: %v", err)
	}
	if st.Installed {
		t.Errorf("still installed: %+v", st)
	}
	doc := readJSON(t, settings)
	stop := doc["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 || !strings.Contains(fmt.Sprint(stop[0]), "my-own-thing") {
		t.Errorf("uninstalling took the user's hook with it: %+v", stop)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	withFakeHome(t)
	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}
	st, err := InstallClaude(script)
	if err != nil {
		t.Fatal(err)
	}
	// Installing twice must not stack two copies that both fire.
	settings, _ := ClaudeSettingsPath()
	doc := readJSON(t, settings)
	stop := doc["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 {
		t.Errorf("Stop has %d entries after installing twice, want 1", len(stop))
	}
	if len(st.Events) != 4 {
		t.Errorf("events = %v", st.Events)
	}
}

func TestInspectReportsNothingWhenTheFileIsMissing(t *testing.T) {
	withFakeHome(t)
	script, _ := InstallScript(t.TempDir())
	st, err := Inspect(script)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.Installed {
		t.Error("reported installed with no settings file")
	}
	if st.Snippet == "" || st.SettingsPath == "" {
		t.Errorf("status is missing guidance: %+v", st)
	}
}

func TestBackupsDoNotOverwriteEachOther(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script, _ := InstallScript(t.TempDir())

	// Install then remove takes well under a second. At second resolution the
	// second backup overwrote the first — which is precisely when the copy of
	// the user's original file was lost.
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallClaude(script); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(settings))
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, e := range entries {
		if strings.Contains(e.Name(), "vibepanel-backup") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) < 2 {
		t.Fatalf("got %d backups from two edits: %v", len(backups), backups)
	}
	// One of them has to be the original, or the point of backing up is lost.
	var sawOriginal bool
	for _, name := range backups {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(settings), name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "hooks") && strings.Contains(string(b), "opus") {
			sawOriginal = true
		}
	}
	if !sawOriginal {
		t.Errorf("no backup holds the file as it was before any edit: %v", backups)
	}
}
