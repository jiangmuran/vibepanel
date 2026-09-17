package claudeaccount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (dir string, main Main) {
	t.Helper()
	root := t.TempDir()
	main = Main{Home: filepath.Join(root, "home")}
	if err := os.MkdirAll(filepath.Join(main.Home, ".claude", "projects", "-p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Home, ".claude", "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Home, ".claude", ".credentials.json"), []byte(`{"secret":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "data", DirName, "abc123"), main
}

func state(links []Link, name string) LinkState {
	for _, l := range links {
		if l.Name == name {
			return l.State
		}
	}
	return ""
}

func TestReconcileLinksEverySharedEntryAndNothingElse(t *testing.T) {
	dir, main := setup(t)
	links, err := Reconcile(dir, main, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != len(Shared) {
		t.Fatalf("got %d links, want %d", len(links), len(Shared))
	}
	for _, e := range Shared {
		if got := state(links, e.Name); got != Linked {
			t.Errorf("%s: %s, want linked", e.Name, got)
		}
		dest, err := os.Readlink(filepath.Join(dir, e.Name))
		if err != nil || dest != filepath.Join(main.Home, ".claude", e.Name) {
			t.Errorf("%s -> %q (%v)", e.Name, dest, err)
		}
	}
	// The login is the one thing an account exists not to share.
	if _, err := os.Lstat(filepath.Join(dir, ".credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".credentials.json is in the account directory: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("account directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
	}
	// A shared directory missing from ~/.claude is created there, because a
	// dangling link to a directory is not followed by mkdir.
	if fi, err := os.Stat(filepath.Join(main.Home, ".claude", "skills")); err != nil || !fi.IsDir() {
		t.Errorf("skills was not created in ~/.claude: %v", err)
	}
	// A missing file is left as a dangling link: Claude Code creates the
	// target through it.
	if _, err := os.Stat(filepath.Join(main.Home, ".claude", "CLAUDE.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("CLAUDE.md was created in ~/.claude: %v", err)
	}
}

// The list is what one login can see of another's. These must never be on it.
func TestSharedNeverIncludesWhatBelongsToOneLogin(t *testing.T) {
	for _, e := range Shared {
		switch e.Name {
		case ".credentials.json", ".claude.json", "sessions", "daemon", "jobs",
			"remote-settings.json", "backups", "statsig", "telemetry":
			t.Errorf("%s is shared", e.Name)
		}
	}
}

func TestReconcileIsIdempotentAndLeavesSomebodysDataAlone(t *testing.T) {
	dir, main := setup(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Written by Claude Code before the account was reconciled, or by a
	// person: a real file where a link would go, and a link elsewhere.
	if err := os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		links, err := Reconcile(dir, main, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := state(links, "history.jsonl"); got != Blocked {
			t.Errorf("history.jsonl: %s, want blocked", got)
		}
		if got := state(links, "skills"); got != Blocked {
			t.Errorf("skills: %s, want blocked", got)
		}
		if got := state(links, "projects"); got != Linked {
			t.Errorf("projects: %s, want linked", got)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	if string(b) != "mine\n" {
		t.Errorf("history.jsonl was replaced: %q", b)
	}
	if dest, _ := os.Readlink(filepath.Join(dir, "skills")); dest != "/nowhere" {
		t.Errorf("skills link was changed to %q", dest)
	}
}

func TestIsolatedAccountSharesConfigurationButNotHistory(t *testing.T) {
	dir, main := setup(t)
	// Linked once as a shared account, as if the rule had been different.
	if _, err := Reconcile(dir, main, false); err != nil {
		t.Fatal(err)
	}
	links, err := Reconcile(dir, main, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range Shared {
		_, lerr := os.Lstat(filepath.Join(dir, e.Name))
		if e.History {
			if got := state(links, e.Name); got != Private {
				t.Errorf("%s: %s, want private", e.Name, got)
			}
			if !errors.Is(lerr, os.ErrNotExist) {
				t.Errorf("%s is still linked in an isolated account", e.Name)
			}
		} else if got := state(links, e.Name); got != Linked {
			t.Errorf("%s: %s, want linked", e.Name, got)
		}
	}
	// Unlinking removed links, not transcripts.
	if _, err := os.Stat(filepath.Join(main.Home, ".claude", "projects", "-p")); err != nil {
		t.Errorf("the shared projects directory was touched: %v", err)
	}
}

func TestRemoveLeavesTheSharedFilesAlone(t *testing.T) {
	dir, main := setup(t)
	if _, err := Reconcile(dir, main, false); err != nil {
		t.Fatal(err)
	}
	var loggedOut []string
	run := func(_ context.Context, env []string, args ...string) ([]byte, error) {
		loggedOut = append(loggedOut, strings.Join(env, " ")+" | "+strings.Join(args, " "))
		return nil, errors.New("claude: not found")
	}
	logoutErr, err := Remove(context.Background(), run, dir)
	if err != nil {
		t.Fatal(err)
	}
	if logoutErr == nil {
		t.Error("a failed logout was not reported")
	}
	if len(loggedOut) != 1 || !strings.Contains(loggedOut[0], "CLAUDE_CONFIG_DIR="+dir) ||
		!strings.HasSuffix(loggedOut[0], "auth logout") {
		t.Errorf("logout ran as %q", loggedOut)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("account directory still there: %v", err)
	}
	for _, p := range []string{".claude/settings.json", ".claude/.credentials.json", ".claude/projects/-p"} {
		if _, err := os.Stat(filepath.Join(main.Home, p)); err != nil {
			t.Errorf("~/%s went with the account: %v", p, err)
		}
	}
}

func TestDirRefusesAnIDThatIsAPath(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", "../x", "a.b"} {
		if _, err := Dir("/data", id); err == nil {
			t.Errorf("Dir accepted %q", id)
		}
	}
	got, err := Dir("/data", "0123abcd")
	if err != nil || got != "/data/claude-accounts/0123abcd" {
		t.Errorf("Dir = %q, %v", got, err)
	}
}

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSyncJSONCopiesHabitsAndNeverTheAccount(t *testing.T) {
	dir, main := setup(t)
	writeJSON(t, filepath.Join(main.Home, ".claude.json"), `{
	  "oauthAccount": {"emailAddress": "main@example.com"},
	  "userID": "main-user",
	  "customApiKeyResponses": {"approved": ["x"]},
	  "hasCompletedOnboarding": true,
	  "theme": "dark",
	  "mcpServers": {"linear": {"type": "http"}, "mine": {"type": "stdio", "command": "main"}},
	  "projects": {
	    "/repo": {"hasTrustDialogAccepted": true, "allowedTools": ["Bash(ls)", "Read"],
	              "enabledMcpjsonServers": ["db"], "lastCost": 12}
	  }
	}`)
	writeJSON(t, filepath.Join(dir, ".claude.json"), `{
	  "oauthAccount": {"emailAddress": "other@example.com"},
	  "userID": "other-user",
	  "theme": "light",
	  "mcpServers": {"mine": {"type": "stdio", "command": "account"}},
	  "projects": {"/repo": {"allowedTools": ["Read", "Edit"], "enabledMcpjsonServers": []}}
	}`)

	if err := SyncJSON(dir, main); err != nil {
		t.Fatal(err)
	}
	got := readJSON(t, filepath.Join(dir, ".claude.json"))

	if got["oauthAccount"].(map[string]any)["emailAddress"] != "other@example.com" || got["userID"] != "other-user" {
		t.Errorf("the account's identity changed: %v %v", got["oauthAccount"], got["userID"])
	}
	if _, ok := got["customApiKeyResponses"]; ok {
		t.Error("API key approvals were copied")
	}
	if got["theme"] != "light" {
		t.Errorf("theme overwritten: %v", got["theme"])
	}
	if got["hasCompletedOnboarding"] != true {
		t.Error("onboarding not carried over")
	}
	mcp := got["mcpServers"].(map[string]any)
	if _, ok := mcp["linear"]; !ok {
		t.Error("missing MCP server not added")
	}
	if mcp["mine"].(map[string]any)["command"] != "account" {
		t.Error("the account's own MCP server was overwritten")
	}
	p := got["projects"].(map[string]any)["/repo"].(map[string]any)
	if p["hasTrustDialogAccepted"] != true {
		t.Error("trust not carried over")
	}
	tools, _ := json.Marshal(p["allowedTools"])
	if string(tools) != `["Read","Edit","Bash(ls)"]` {
		t.Errorf("allowedTools = %s", tools)
	}
	if enabled, _ := json.Marshal(p["enabledMcpjsonServers"]); string(enabled) != `[]` {
		t.Errorf("a decision the account made was overwritten: %s", enabled)
	}
	if _, ok := p["lastCost"]; ok {
		t.Error("per-project statistics were copied")
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".claude.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	// Nothing to add is no write at all.
	before, _ := os.Stat(filepath.Join(dir, ".claude.json"))
	if err := SyncJSON(dir, main); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(dir, ".claude.json"))
	if !before.ModTime().Equal(after.ModTime()) || os.SameFile(before, after) == false {
		t.Error("a sync with nothing to add rewrote the file")
	}
}

func TestSyncJSONRefusesToOverwriteAFileItCannotRead(t *testing.T) {
	dir, main := setup(t)
	writeJSON(t, filepath.Join(main.Home, ".claude.json"), `{"mcpServers":{"a":{}}}`)
	writeJSON(t, filepath.Join(dir, ".claude.json"), `{"oauthAccount": {"emailAd`)
	if err := SyncJSON(dir, main); err == nil {
		t.Fatal("merged into a file that does not parse")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if string(b) != `{"oauthAccount": {"emailAd` {
		t.Errorf("file was replaced: %q", b)
	}
}

func TestReadStatusReadsTheDocumentAndHidesThePanelsKeys(t *testing.T) {
	var gotEnv []string
	run := func(_ context.Context, env []string, args ...string) ([]byte, error) {
		gotEnv = env
		if strings.Join(args, " ") != "auth status --json" {
			t.Errorf("args %v", args)
		}
		return []byte(`{"loggedIn":false,"authMethod":"none","configDirectory":"/d"}`), errors.New("exit status 1")
	}
	st, err := ReadStatus(context.Background(), run, "/d")
	if err != nil || st.LoggedIn || st.AuthMethod != "none" || st.ConfigDirectory != "/d" {
		t.Fatalf("%+v %v", st, err)
	}
	joined := strings.Join(gotEnv, " ")
	for _, want := range []string{"CLAUDE_CONFIG_DIR=/d", "ANTHROPIC_API_KEY=", "CLAUDE_CODE_OAUTH_TOKEN="} {
		if !strings.Contains(joined, want) {
			t.Errorf("env %q lacks %q", joined, want)
		}
	}

	_, err = ReadStatus(context.Background(), func(context.Context, []string, ...string) ([]byte, error) {
		return nil, errors.New("exec: claude: not found")
	}, "/d")
	if err == nil {
		t.Error("a missing claude read as a status")
	}
}

// Claude Code 2.1.274 moves display preferences into settings.json, which is
// shared. Copied into .claude.json they were deleted by the first session and
// copied back by the next launch: a write per launch for nothing.
func TestSyncJSONLeavesDisplayPreferencesToSettings(t *testing.T) {
	dir, main := setup(t)
	writeJSON(t, filepath.Join(main.Home, ".claude.json"), `{"theme":"dark","editorMode":"vim"}`)
	if err := SyncJSON(dir, main); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a display preference was copied into an account")
	}
}
