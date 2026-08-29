package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// read is the file as a map, or a fatal.
func read(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return d
}

func settingsIn(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// A machine with no ~/.claude at all, which is every fresh install.
func TestTuneCreatesTheFileWhenThereIsNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st, err := InspectTune()
	if err != nil {
		t.Fatalf("InspectTune: %v", err)
	}
	if st.Exists {
		t.Error("reported an existing file in an empty home")
	}
	if st.Changes != len(Tweaks()) {
		t.Errorf("Changes = %d, want all %d", st.Changes, len(Tweaks()))
	}
	if _, err := os.Stat(settingsIn(home)); !os.IsNotExist(err) {
		t.Fatal("InspectTune wrote something; it must not touch the disk")
	}

	if _, err := ApplyTune(); err != nil {
		t.Fatalf("ApplyTune: %v", err)
	}
	doc := read(t, settingsIn(home))
	if doc["autoUploadSessions"] != false {
		t.Errorf("autoUploadSessions = %v", doc["autoUploadSessions"])
	}
	env, _ := doc["env"].(map[string]any)
	if env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("env = %v", doc["env"])
	}
}

// The file belongs to somebody else and mostly holds their things.
//
// This is the whole risk of the feature. A merge that drops a key, flattens
// `hooks`, or rewrites `env` wholesale would take out the panel's own state
// reporting along with whatever else was in there, and the installer runs this
// unattended on a machine the user has been using for months.
func TestTuneLeavesEverythingElseAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{
  "model": "opus",
  "theme": "dark",
  "env": {"SOMETHING_ELSE": "keep me"},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/x/report.sh done"}]}]}
}`
	path := settingsIn(home)
	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyTune(); err != nil {
		t.Fatalf("ApplyTune: %v", err)
	}
	doc := read(t, path)

	if doc["model"] != "opus" || doc["theme"] != "dark" {
		t.Errorf("unrelated keys changed: model=%v theme=%v", doc["model"], doc["theme"])
	}
	env, _ := doc["env"].(map[string]any)
	if env["SOMETHING_ELSE"] != "keep me" {
		t.Errorf("env was replaced rather than merged: %v", doc["env"])
	}
	if env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("the tweak did not land in env: %v", doc["env"])
	}
	if _, ok := doc["hooks"]; !ok {
		t.Error("hooks is gone; state reporting would stop with no error anywhere")
	}

	// The mode the user's file had, not one we prefer. Tightening somebody's
	// dotfile silently is the same surprise as rewriting it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640", info.Mode().Perm())
	}

	// And a copy of what was there before.
	found := false
	entries, _ := os.ReadDir(filepath.Join(home, ".claude"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "backup") {
			b, _ := os.ReadFile(filepath.Join(home, ".claude", e.Name()))
			if string(b) != before {
				t.Errorf("the backup is not what was there:\n%s", b)
			}
			found = true
		}
	}
	if !found {
		t.Error("no backup was written")
	}
}

// Running it twice writes once.
func TestTuneIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := ApplyTune(); err != nil {
		t.Fatalf("first: %v", err)
	}
	first, err := os.ReadFile(settingsIn(home))
	if err != nil {
		t.Fatal(err)
	}

	st, err := ApplyTune()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if st.Changes != 0 {
		t.Errorf("the second run wanted to change %d things", st.Changes)
	}
	second, err := os.ReadFile(settingsIn(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("the second run rewrote the file")
	}
}

// Invalid JSON stops everything.
//
// The merge starts from what was parsed, so a file that will not parse would
// be replaced by seven keys and nothing else -- silently destroying a
// configuration whose only problem was a trailing comma.
func TestTuneRefusesAFileItCannotParse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := "{\"model\": \"opus\",}\n"
	path := settingsIn(home)
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectTune(); err == nil {
		t.Error("InspectTune accepted a file that is not JSON")
	}
	if _, err := ApplyTune(); err == nil {
		t.Error("ApplyTune accepted a file that is not JSON")
	}
	b, _ := os.ReadFile(path)
	if string(b) != broken {
		t.Errorf("the unparseable file was written to anyway:\n%s", b)
	}
}

// A scalar where an object has to go is refused, not replaced.
func TestTuneWillNotOverwriteAScalarWithAnObject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := settingsIn(home)
	if err := os.WriteFile(path, []byte(`{"env": "not an object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyTune(); err == nil {
		t.Fatal("replaced a string with an object without saying anything")
	}
	doc := read(t, path)
	if doc["env"] != "not an object" {
		t.Errorf("env = %v; the file was changed after the refusal", doc["env"])
	}
}

// Every tweak names a key that the installed Claude Code actually reads.
//
// Not a guess about behaviour -- that cannot be tested here -- but the thing
// that makes this feature worth having at all: a plausible key that does
// nothing is worse than no feature, because the summary reports it as applied
// and the person believes it.
//
// Two of these (autoUploadSessions, env.CLAUDE_CODE_ATTRIBUTION_HEADER) are
// absent from json.schemastore.org's copy of the settings schema and present
// in the binary, so this list was checked against the binary rather than the
// schema. Skipped when there is no claude on PATH, which is most CI.
func TestEveryTweakIsAKeyClaudeCodeKnows(t *testing.T) {
	bin, err := exeOnPath("claude")
	if err != nil {
		t.Skip("no claude on PATH")
	}
	blob, err := os.ReadFile(bin)
	if err != nil {
		t.Skipf("cannot read %s: %v", bin, err)
	}
	hay := string(blob)
	for _, tw := range Tweaks() {
		// The leaf, which is the name the CLI reads. `attribution` and `env`
		// are containers and their own names prove nothing about the key
		// inside them.
		leaf := tw.Path[len(tw.Path)-1]
		if !strings.Contains(hay, leaf) {
			t.Errorf("%s: %q does not appear in %s", keyOf(tw.Path), leaf, bin)
		}
	}
}

func exeOnPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Both languages, or neither.
//
// deploy/install.sh's string table has this rule and scripts/install-check.sh
// enforces it there; these sentences are read in exactly the same place, by
// somebody deciding whether to say yes, and they live here instead only because
// they describe a specific key and would drift if they lived apart from it. A
// tweak added with the English side filled in prints a Chinese screen with one
// English line in the middle of it, and nothing else would say so.
func TestEveryTweakSpeaksBothLanguages(t *testing.T) {
	for _, tw := range Tweaks() {
		key := keyOf(tw.Path)
		if strings.TrimSpace(tw.What) == "" {
			t.Errorf("%s: no English description", key)
		}
		if strings.TrimSpace(tw.WhatZH) == "" {
			t.Errorf("%s: no Chinese description", key)
		}
		if tw.Say("zh") == tw.Say("en") && tw.WhatZH != "" {
			t.Errorf("%s: both languages are the same string", key)
		}
		// A description that is only the key again says nothing that the key
		// did not already say.
		if strings.EqualFold(strings.TrimSpace(tw.What), key) {
			t.Errorf("%s: the description just repeats the key", key)
		}
	}
}
