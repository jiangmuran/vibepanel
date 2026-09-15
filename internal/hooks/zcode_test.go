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
