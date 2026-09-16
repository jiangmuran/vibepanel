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

// A block of the user's that merely *mentions* the reporter -- a comment
// saying where they copied the shape from -- is not ours. The marker was
// matched against the whole block, comments included, so installing deleted
// their hook: the install strips ours before appending.
func TestInstallKimiKeepsABlockThatOnlyTalksAboutTheReporter(t *testing.T) {
	withFakeHome(t)
	own := `[[hooks]]
event = "PreToolUse"
# adapted from the vibepanel-report.sh snippet the settings page shows
command = "node ~/.kimi-code/guard.mjs"
`
	writeKimiConfig(t, own)
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	body := readFile(t, kimiPath(t))
	if !strings.Contains(body, "guard.mjs") {
		t.Errorf("the user's own hook was deleted by an install:\n%s", body)
	}
	if _, err := UninstallKimi(codexScript); err != nil {
		t.Fatalf("UninstallKimi: %v", err)
	}
	if body := readFile(t, kimiPath(t)); !strings.Contains(body, "guard.mjs") {
		t.Errorf("and the uninstall took it:\n%s", body)
	}
}

// Pressing install twice must not write twice. The block count stays right
// either way -- the installer strips and re-adds -- so what says so is the
// backup: one per write, and a config with a copy beside it for every press
// of the button is what this is about.
func TestInstallKimiTwiceLeavesOneBackup(t *testing.T) {
	withFakeHome(t)
	writeKimiConfig(t, "default_model = \"k\"\n")
	for i := 0; i < 3; i++ {
		if _, err := InstallKimi(codexScript); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	if n := kimiBackups(t); n != 1 {
		t.Errorf("three installs left %d backups, want 1", n)
	}
	for i := 0; i < 3; i++ {
		if _, err := UninstallKimi(codexScript); err != nil {
			t.Fatalf("uninstall %d: %v", i, err)
		}
	}
	if n := kimiBackups(t); n != 2 {
		t.Errorf("three more uninstalls left %d backups in total, want 2", n)
	}
}

// A machine that has never run Kimi Code should not be left with an empty
// config.toml for a tool it does not have.
func TestUninstallKimiRemovesAFileThatWasOnlyOurs(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	if _, err := UninstallKimi(codexScript); err != nil {
		t.Fatalf("UninstallKimi: %v", err)
	}
	if _, err := os.Stat(kimiPath(t)); !os.IsNotExist(err) {
		t.Errorf("an empty config.toml was left behind: %q", readFile(t, kimiPath(t)))
	}
}

// `[[hooks]]` appended after `hooks = [...]` or `[hooks]` is a redefinition,
// not another element, and TOML rejects the file -- so Kimi Code would not
// start while the settings page said the hooks were installed.
func TestInstallKimiRefusesAConfigItCannotAppendTo(t *testing.T) {
	for _, existing := range []string{"hooks = []\n", "[hooks]\nenabled = true\n"} {
		t.Run(strings.SplitN(existing, "\n", 2)[0], func(t *testing.T) {
			withFakeHome(t)
			writeKimiConfig(t, existing)
			if _, err := InstallKimi(codexScript); err == nil {
				t.Errorf("installed into a config it cannot append to:\n%s", readFile(t, kimiPath(t)))
			}
			if body := readFile(t, kimiPath(t)); body != existing {
				t.Errorf("and wrote to it anyway:\n%s", body)
			}
		})
	}
}

// A multi-line array inside a block has continuation lines that start with
// `[`. Reading those as the next table header cut the block in half, so the
// half carrying the command was not seen and the block was never removed.
func TestKimiRemovesABlockHoldingAMultiLineArray(t *testing.T) {
	withFakeHome(t)
	// A line of the user's, so the file is still there to read afterwards:
	// a config that was only ours is removed rather than left empty.
	writeKimiConfig(t, "default_model = \"k\"\n")
	if _, err := InstallKimi(codexScript); err != nil {
		t.Fatalf("InstallKimi: %v", err)
	}
	body := readFile(t, kimiPath(t))
	// As if a future version of the panel wrote a block with an array in it.
	body += "\n[[hooks]]\nevent = \"Stop\"\nargs = [\n  [1],\n  [2],\n]\ncommand = \"" + codexScript + " done\"\n"
	if err := os.WriteFile(kimiPath(t), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := UninstallKimi(codexScript); err != nil {
		t.Fatalf("UninstallKimi: %v", err)
	}
	if left := readFile(t, kimiPath(t)); strings.Contains(left, "vibepanel-report") {
		t.Errorf("a block with an array in it was not recognised as ours:\n%s", left)
	}
}

func writeKimiConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(kimiPath(t)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(kimiPath(t), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func kimiBackups(t *testing.T) int {
	t.Helper()
	m, err := filepath.Glob(kimiPath(t) + ".vibepanel-backup-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return len(m)
}
