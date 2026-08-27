package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/hooks"
)

// Installing for Codex has to edit Codex's file and nothing else.
//
// The two agents are configured by different mechanisms in different files, and
// the request that says which is a query parameter — which is exactly the kind
// of thing that gets dropped by a caller and silently resolves to the default.
// Then the button labelled Codex writes Claude's settings.json, the page reads
// Codex's config.toml back, and it truthfully reports nothing installed.
func TestInstallingForCodexEditsCodexAndOnlyCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts, _ := newTestServer(t)

	res, err := ts.Client().Post(ts.URL+"/api/settings/hooks?agent=codex", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %s", res.Status)
	}

	body, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("codex config: %v", err)
	}
	if !strings.Contains(string(body), "vibepanel-report.sh") {
		t.Errorf("the notify line is not in the file:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Error("installing for Codex also wrote Claude's settings file")
	}

	// And removing it again, through the same parameter.
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/hooks?agent=codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %s", res.Status)
	}
	body, err = os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("codex config after removal: %v", err)
	}
	if strings.Contains(string(body), "vibepanel-report.sh") {
		t.Errorf("the notify line survived removal:\n%s", body)
	}
}

// An agent nobody recognises is refused rather than resolved to the default.
// The value decides which file in somebody's home directory is edited, so a
// typo has to be an error: installing for "codx" and being told it worked, with
// Claude's file quietly rewritten, is worse than any 400.
func TestAnUnknownAgentIsRefusedRatherThanGuessed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts, _ := newTestServer(t)

	res, err := ts.Client().Post(ts.URL+"/api/settings/hooks?agent=codx", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST with a bad agent: %s, want 400", res.Status)
	}
	for _, p := range []string{".claude/settings.json", ".codex/config.toml"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Errorf("a refused request still wrote %s", p)
		}
	}
}

// The "states are being guessed" notice reads this, and it read Claude's flag
// alone: a machine wired up through Codex was told to install hooks it already
// had, by the notice whose whole purpose is to say the panel is guessing.
func TestEitherAgentCountsAsHooksInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{Cfg: config.Config{DataDir: t.TempDir()}}

	if s.hooksAreInstalled() {
		t.Fatal("reported installed with no configuration at all")
	}
	script, err := s.scriptPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.InstallCodex(script); err != nil {
		t.Fatal(err)
	}
	s.forgetHookStatus()
	if !s.hooksAreInstalled() {
		t.Error("Codex's hook is installed and the panel still says states are guessed")
	}

	if _, err := hooks.UninstallCodex(script); err != nil {
		t.Fatal(err)
	}
	s.forgetHookStatus()
	if s.hooksAreInstalled() {
		t.Error("still reported installed after removing Codex's hook")
	}
}
