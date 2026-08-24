package hooks

import (
	"encoding/json"
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
	path, err := Install(dir)
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
	path, err := Install(dir)
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
	first, err := Install(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	second, err := Install(dir)
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
