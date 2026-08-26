package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// Installing when the hooks are already exactly right must leave the user's
// file alone: not rewritten, not reformatted, and no backup recording an edit
// that did not happen. The settings page invites this press, so it is the
// common case rather than an edge one.
func TestInstallingTwiceDoesNotTouchTheFileAgain(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	script, _ := InstallScript(t.TempDir())

	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}
	countBackups := func() int {
		t.Helper()
		entries, rerr := os.ReadDir(filepath.Dir(settings))
		if rerr != nil {
			t.Fatal(rerr)
		}
		n := 0
		for _, e := range entries {
			if strings.Contains(e.Name(), "vibepanel-backup") {
				n++
			}
		}
		return n
	}
	backupsAfterFirst := countBackups()

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(settings, old, old); err != nil {
		t.Fatal(err)
	}

	st, err := InstallClaude(script)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || len(st.Events) != 4 {
		t.Errorf("second install reported %+v", st)
	}
	after, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Error("the settings file was rewritten when nothing had changed")
	}
	if n := countBackups(); n != backupsAfterFirst {
		t.Errorf("backups went from %d to %d for an edit that did not happen",
			backupsAfterFirst, n)
	}
}

// The file belongs to the user, including its permissions. Pressing a button
// about hooks must not quietly change who can read it.
func TestInstallKeepsTheFileMode(t *testing.T) {
	home := withFakeHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v after installing, want the 0644 the user had", info.Mode().Perm())
	}
}

// Callers treat InstallScript as "tell me where the script is" and ask on
// paths that run several times a second, so a call that changes nothing must
// touch nothing. It also matters that an unchanged install does not replace
// the file: agents execute it at moments nobody controls, and a shell reads a
// script incrementally.
func TestInstallScriptDoesNotRewriteAnUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	path, err := InstallScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// A modification time far enough in the past that any rewrite is visible
	// whatever the filesystem's timestamp resolution.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallScript(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Errorf("the script was rewritten when nothing had changed: %v → %v",
			old, after.ModTime())
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("mode changed: %v → %v", before.Mode().Perm(), after.Mode().Perm())
	}
}

// The other half: when the content really is wrong, it has to be replaced —
// and replaced whole, not truncated and refilled underneath a running hook.
func TestInstallScriptReplacesWrongContent(t *testing.T) {
	dir := t.TempDir()
	path, err := InstallScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallScript(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, ReportScript) {
		t.Error("a stale script was left in place")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The 0600 written above must not survive: the panel runs this file.
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v after replacing, want 0700", info.Mode().Perm())
	}
	// Nothing left beside it. A temp file that outlives the rename is a file
	// the next reader has to wonder about.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// All four variables, or the hook is decorative.
//
// The admin CLI built its own list of two — session id and project id — so a
// session created with `vibepanel session new` had nothing to authenticate
// with and nowhere to post. The hook script suppresses its own errors by
// design, so the only symptom was state that stayed guessed, in a panel whose
// settings page said hooks were installed.
func TestSessionEnvCarriesEverythingTheScriptReads(t *testing.T) {
	env := SessionEnv("s-1", "p-1", "http://127.0.0.1:8443", "tok")
	want := map[string]string{
		"VIBEPANEL_SESSION_ID": "s-1",
		"VIBEPANEL_PROJECT_ID": "p-1",
		"VIBEPANEL_URL":        "http://127.0.0.1:8443",
		"VIBEPANEL_TOKEN":      "tok",
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed entry %q", kv)
		}
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// The three the script actually reads. If a name changes on either side
	// the hook silently stops working, and nothing else would notice — the
	// script's whole design is to fail quietly outside the panel.
	//
	// VIBEPANEL_PROJECT_ID is deliberately not in this list: it is offered to
	// whatever the person runs inside the session, and asserting that the
	// script reads it is how this test first failed, against a comment that
	// had invented a use for it.
	for _, k := range []string{"VIBEPANEL_SESSION_ID", "VIBEPANEL_TOKEN", "VIBEPANEL_URL"} {
		if !strings.Contains(string(ReportScript), k) {
			t.Errorf("the embedded hook script never mentions %s", k)
		}
	}
}

// Without a token the session is still usable; it just falls back.
func TestSessionEnvOmitsAnEmptyToken(t *testing.T) {
	for _, kv := range SessionEnv("s", "p", "http://x", "") {
		if strings.HasPrefix(kv, "VIBEPANEL_TOKEN=") {
			t.Errorf("an empty token was injected as %q, which the script cannot tell from a real one", kv)
		}
	}
}

func TestTheEventListComesBackInTheSameOrderEveryTime(t *testing.T) {
	withFakeHome(t)
	script, _ := InstallScript(t.TempDir())
	if _, err := InstallClaude(script); err != nil {
		t.Fatal(err)
	}

	// Twenty times, because the list is built by walking a map and Go
	// randomises that. One call proves nothing: with four events there is a
	// one-in-twenty-four chance of drawing sorted order by accident. Twenty
	// consecutive draws is the assertion.
	//
	// What this protects is the settings page, which renders the list as it
	// arrives. Unsorted, the events reshuffle on every poll under a reader's
	// eyes — the same thing this project refuses to do to the session strip.
	var first []string
	for i := range 20 {
		st, err := Inspect(script)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.IsSorted(st.Events) {
			t.Fatalf("call %d returned the events unsorted: %v", i, st.Events)
		}
		if first == nil {
			first = st.Events
			continue
		}
		if !slices.Equal(first, st.Events) {
			t.Fatalf("call %d returned a different order:\n first %v\n  then %v", i, first, st.Events)
		}
	}
	if len(first) != 4 {
		t.Fatalf("expected four installed events, got %v", first)
	}
}

// The event list is a JSON array, never null.
//
// `[]string(nil)` marshals to `null`, and Events is nil until a hook is
// installed -- which is every fresh panel, and the state the settings page
// first reads. The one caller guarded with `(status.events ?? []).length`, and
// wire.ts declared the field `string[] | null` to match: the symptom patched at
// the reader, and the type changed to agree with the bug. The next reader
// written without the guard throws on a page that has never had hooks.
func TestTheEventListIsAnArrayEvenWhenNothingIsInstalled(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "report.sh")

	st, err := Inspect(script)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.Installed {
		t.Fatalf("a directory with no settings file reports hooks installed; this test is "+
			"not looking at the state it means to: %+v", st)
	}
	if st.Events == nil {
		t.Error("Events is nil, which marshals to null")
	}

	blob, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(blob, []byte(`"events":[]`)) {
		t.Errorf("the settings page is sent %s; it maps over that field", blob)
	}
}
