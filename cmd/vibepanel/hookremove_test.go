package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `vibepanel hook remove` has to walk every agent the hooks package knows.
//
// Nothing in the repository called this function. `scripts/install-check.sh`
// drives a *fake* binary that writes what the real one is supposed to write,
// so the two agents added last could have been left out of this walk entirely
// with every check green -- and the uninstaller runs this and then deletes the
// reporter script, so a missed agent is a live hook calling a file that is not
// there, silently, on every prompt.
func TestHookRemoveTakesTheHooksOutOfEveryAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := filepath.Join(home, "data")

	// A reporter line in each agent's own file, in that agent's own shape.
	reporter := filepath.Join(data, "hooks", "vibepanel-report.sh")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "hooks.json")
	kimi := filepath.Join(home, ".kimi-code", "config.toml")
	zcode := filepath.Join(home, ".zcode", "cli", "config.json")
	write(claude, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"`+reporter+` done"}]}]}}`)
	write(codex, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"`+reporter+` done codex"}]}]}}`)
	write(kimi, "[[hooks]]\nevent = \"Stop\"\ncommand = \""+reporter+" done\"\n")
	write(zcode, `{"hooks":{"enabled":true,"events":{"Stop":[{"matcher":".*","hooks":[`+
		`{"type":"command","command":"`+reporter+` done","timeout":5}]}]}}}`)

	if err := hookRemove([]string{"--data-dir", data}); err != nil {
		t.Fatalf("hook remove: %v", err)
	}
	for _, path := range []string{claude, codex, kimi, zcode} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // removed outright, which is an answer too
		}
		if strings.Contains(string(b), "vibepanel-report") {
			t.Errorf("%s still points at the reporter after `hook remove`:\n%s", path, b)
		}
	}
	// And only then the script itself, because a hook pointing at a missing
	// file is silent: the reporter suppresses its own failures on purpose.
	if _, err := os.Stat(reporter); !os.IsNotExist(err) {
		t.Errorf("the reporter script is still there: %v", err)
	}
}

// The machine that has none of them, which is most of them. Every agent is
// reported, the command succeeds, and the reporter goes: a failure here keeps
// the script and exits 1, and the uninstaller reads that as "the hooks are
// still installed".
func TestHookRemoveOnAMachineWithNoAgentsSucceeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := filepath.Join(home, "data")
	if err := hookRemove([]string{"--data-dir", data}); err != nil {
		t.Fatalf("hook remove on a machine with no agents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode")); !os.IsNotExist(err) {
		t.Error("it created ~/.zcode on a machine that has no zcode")
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code")); !os.IsNotExist(err) {
		t.Error("it created ~/.kimi-code on a machine that has no Kimi Code")
	}
}
