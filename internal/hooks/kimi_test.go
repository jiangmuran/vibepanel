package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kimiPath(t *testing.T) string {
	t.Helper()
	p, err := KimiConfigPath()
	if err != nil {
		t.Fatalf("KimiConfigPath: %v", err)
	}
	return p
}

func TestInstallKimiCreatesTheConfigWhenThereIsNone(t *testing.T) {
	withFakeHome(t)
	st, err := InstallKimi(codexScript)
	if err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	if !st.KimiInstalled {
		t.Error("installed and then reported not installed")
	}
	if len(st.KimiEvents) != len(kimiEvents) {
		t.Errorf("reported %d events, want %d", len(st.KimiEvents), len(kimiEvents))
	}
	body := readFile(t, kimiPath(t))
	for _, e := range kimiEvents {
		if !strings.Contains(body, `event = "`+e.event+`"`) {
			t.Errorf("config is missing the %s block:\n%s", e.event, body)
		}
	}
	if !strings.Contains(body, codexScript) {
		t.Errorf("config does not carry the reporter:\n%s", body)
	}
}

// A second install must produce the same file: the settings page calls this
// whenever the button is pressed, and a grow-by-one-copy-each-time installer
// is how a config file ends up with five identical blocks in it.
func TestInstallKimiTwiceWritesOneCopy(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("first InstallKimi: %v", err)
	}
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("second InstallKimi: %v", err)
	}
	body := readFile(t, kimiPath(t))
	if n := strings.Count(body, "[[hooks]]"); n != len(kimiEvents) {
		t.Errorf("two installs left %d blocks, want %d:\n%s", n, len(kimiEvents), body)
	}
}

// The blocks are appended to a file that is the user's: providers, model
// tables and their own hooks must survive both directions untouched.
func TestKimiKeepsTheUsersOwnHooks(t *testing.T) {
	withFakeHome(t)
	own := `default_model = "kimi-code/k3"

[[hooks]]
event = "PreToolUse"
matcher = "Bash"
command = "node ~/.kimi-code/hooks/block-dangerous-bash.mjs"
`
	if err := os.MkdirAll(filepath.Dir(kimiPath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(kimiPath(t), []byte(own), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	if _, err := UninstallKimi(codexScript); err != nil {
		t.Fatalf("UninstallKimi: %v", err)
	}
	body := readFile(t, kimiPath(t))
	if !strings.Contains(body, "block-dangerous-bash.mjs") {
		t.Errorf("the user's own hook is gone:\n%s", body)
	}
	if strings.Contains(body, "vibepanel-report") {
		t.Errorf("ours is still there after uninstall:\n%s", body)
	}
	if !strings.Contains(body, `default_model = "kimi-code/k3"`) {
		t.Errorf("the rest of the file did not survive:\n%s", body)
	}
}

// A data directory with a space in it, which is the case tomlString exists
// for: written raw, the quote or the backslash ends the string and the file
// stops parsing.
func TestKimiSnippetQuotesThePath(t *testing.T) {
	snippet := KimiHooks(`/data/we"ird\path/vibepanel-report.sh`)
	if !strings.Contains(snippet, `we\"ird`) || !strings.Contains(snippet, `\\path`) {
		t.Errorf("the path was written into TOML unescaped: %s", snippet)
	}
}

// Removal takes our blocks and stops at the next table header -- so a comment
// the user wrote to introduce the section after ours was inside the range
// being dropped, and went with it.
func TestUninstallKimiKeepsACommentWrittenAfterOurBlocks(t *testing.T) {
	withFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(kimiPath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(kimiPath(t), []byte("default_model = \"k\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	body := readFile(t, kimiPath(t))
	body += "\n# the provider I use at work\n[providers.work]\nbase_url = \"https://example.invalid\"\n"
	if err := os.WriteFile(kimiPath(t), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := UninstallKimi(codexScript); err != nil {
		t.Fatalf("UninstallKimi: %v", err)
	}
	after := readFile(t, kimiPath(t))
	if !strings.Contains(after, "# the provider I use at work") {
		t.Errorf("the user's comment went out with our hooks:\n%s", after)
	}
	if strings.Contains(after, "vibepanel-report") {
		t.Errorf("ours is still there:\n%s", after)
	}
}

// And the blank line the installer itself writes between blocks is not
// somebody's: kept, it would leave one more empty line behind on every
// install-remove cycle.
func TestKimiLeavesNoBlankLinesBehindOverCycles(t *testing.T) {
	withFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(kimiPath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	start := "default_model = \"k\"\n"
	if err := os.WriteFile(kimiPath(t), []byte(start), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := InstallKimi(codexScript); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
		if _, err := UninstallKimi(codexScript); err != nil {
			t.Fatalf("uninstall %d: %v", i, err)
		}
	}
	if after := readFile(t, kimiPath(t)); after != start {
		t.Errorf("three cycles did not leave the file as it was found:\n%q", after)
	}
}
