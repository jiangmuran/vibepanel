package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zcodePath(t *testing.T) string {
	t.Helper()
	p, err := ZcodeConfigPath()
	if err != nil {
		t.Fatalf("ZcodeConfigPath: %v", err)
	}
	return p
}

func readZcodeDoc(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(zcodePath(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("the written config does not parse: %v", err)
	}
	return doc
}

func TestInstallZcodeCreatesTheConfigWhenThereIsNone(t *testing.T) {
	withFakeHome(t)
	st, err := InstallZcode(codexScript)
	if err != nil {
		t.Fatalf("InstallZcode: %v", err)
	}
	if !st.ZcodeInstalled {
		t.Error("installed and then reported not installed")
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Error("hooks.enabled is not true; nothing would ever run")
	}
	events, _ := hooks["events"].(map[string]any)
	for _, e := range zcodeEvents {
		if _, ok := events[e.event]; !ok {
			t.Errorf("events is missing %s", e.event)
		}
	}
}

// The rest of the file is zcode's own — providers, models, permissions — and
// zcode rewrites it itself, so a lost key here is a login or a model gone.
func TestZcodeKeepsTheRestOfTheConfig(t *testing.T) {
	withFakeHome(t)
	own := `{
  "provider": {"zai": {"kind": "anthropic"}},
  "hooks": {"enabled": false, "events": {}}
}
`
	if err := os.MkdirAll(filepath.Dir(zcodePath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(zcodePath(t), []byte(own), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := InstallZcode(codexScript); err != nil {
		t.Fatalf("InstallZcode: %v", err)
	}
	doc := readZcodeDoc(t)
	provider, _ := doc["provider"].(map[string]any)
	if _, ok := provider["zai"]; !ok {
		t.Error("the provider block is gone")
	}
}

// enabled belongs to the whole "hooks" object, not to us: it goes back to
// false only when nothing of anybody's is left, and stays when the user has
// hooks of their own.
func TestUninstallZcodeLeavesEnabledWhenOthersRemain(t *testing.T) {
	withFakeHome(t)
	own := `{
  "hooks": {
    "enabled": true,
    "events": {
      "Stop": [ { "matcher": ".*", "hooks": [ { "type": "command", "command": "/opt/theirs/notify done" } ] } ]
    }
  }
}
`
	if err := os.MkdirAll(filepath.Dir(zcodePath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(zcodePath(t), []byte(own), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := InstallZcode(codexScript); err != nil {
		t.Fatalf("InstallZcode: %v", err)
	}
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode: %v", err)
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Error("enabled went false with the user's own hooks still present")
	}
	events, _ := hooks["events"].(map[string]any)
	groups, _ := events["Stop"].([]any)
	if len(groups) != 1 {
		t.Fatalf("the user's Stop groups did not survive: %v", events)
	}
	body := readFile(t, zcodePath(t))
	if strings.Contains(body, "vibepanel-report") {
		t.Errorf("ours is still there after uninstall:\n%s", body)
	}
}

func TestUninstallZcodeRevertsEnabledWhenEmpty(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallZcode(codexScript); err != nil {
		t.Fatalf("InstallZcode: %v", err)
	}
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode: %v", err)
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != false {
		t.Error("enabled stayed true with no hooks left at all")
	}
}

// A second install must produce the same file and no second backup. The
// settings page calls this on every press, and comparing the bytes on disk
// against a re-encode that has no trailing newline made every press a rewrite
// and a backup of its own.
func TestInstallZcodeTwiceWritesOneCopyAndOneBackup(t *testing.T) {
	withFakeHome(t)
	for i := 0; i < 3; i++ {
		if _, err := InstallZcode(codexScript); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	events, _ := hooks["events"].(map[string]any)
	for _, e := range zcodeEvents {
		if groups, _ := events[e.event].([]any); len(groups) != 1 {
			t.Errorf("%s has %d groups after three installs, want 1", e.event, len(groups))
		}
	}
	if n := zcodeBackups(t); n != 0 {
		t.Errorf("three installs of a config that did not exist left %d backups, want 0", n)
	}
}

// The machine that has never run zcode, which is most of them: `vibepanel hook
// remove` walks every agent, and this one turned "there is no config" into an
// empty document, wrote it back and failed for want of ~/.zcode/cli. The
// command then reported a failure and kept the reporter script it was there to
// delete.
func TestUninstallZcodeWithoutZcodeWritesNothing(t *testing.T) {
	home := withFakeHome(t)
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode on a machine with no zcode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Error("removing hooks created ~/.zcode on a machine that has no zcode")
	}
}

// A config with no "hooks" object at all is somebody who has never asked the
// panel for anything. Removing must not add one to say the panel was here.
func TestUninstallZcodeLeavesAConfigWithNoHooksAlone(t *testing.T) {
	withFakeHome(t)
	own := `{
  "provider": {"zai": {"kind": "anthropic"}}
}
`
	writeZcodeConfig(t, own)
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode: %v", err)
	}
	if body := readFile(t, zcodePath(t)); body != own {
		t.Errorf("the config was rewritten by a removal that had nothing to remove:\n%s", body)
	}
}

// enabled is not the panel's to turn off. It gates hooks that are not in this
// file -- zcode's workspace and plugin hooks -- so somebody who had it on for
// those, with no events of their own here, had them switched off by a removal
// that had installed nothing.
func TestUninstallZcodeLeavesEnabledThePanelDidNotTurnOn(t *testing.T) {
	withFakeHome(t)
	writeZcodeConfig(t, `{"hooks": {"enabled": true, "events": {}}}`+"\n")
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode: %v", err)
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Error("a removal that installed nothing switched the user's hooks off")
	}
}

// The other half of the same rule: install onto a config whose enabled was
// already true, and the uninstall leaves it true even with nothing left.
func TestUninstallZcodeKeepsEnabledThatWasOnBefore(t *testing.T) {
	withFakeHome(t)
	writeZcodeConfig(t, `{"hooks": {"enabled": true, "events": {}}}`+"\n")
	if _, err := InstallZcode(codexScript); err != nil {
		t.Fatalf("InstallZcode: %v", err)
	}
	if _, err := UninstallZcode(codexScript); err != nil {
		t.Fatalf("UninstallZcode: %v", err)
	}
	doc := readZcodeDoc(t)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Error("enabled was on before the panel arrived and went off when it left")
	}
}

func writeZcodeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(zcodePath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(zcodePath(t), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func zcodeBackups(t *testing.T) int {
	t.Helper()
	m, err := filepath.Glob(zcodePath(t) + ".vibepanel-backup-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(m)
}
