package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const codexScript = "/data/hooks/vibepanel-report.sh"

func codexPath(t *testing.T) string {
	t.Helper()
	p, err := CodexConfigPath()
	if err != nil {
		t.Fatalf("CodexConfigPath: %v", err)
	}
	return p
}

func writeCodexConfig(t *testing.T, body string) string {
	t.Helper()
	p := codexPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestInstallCodexCreatesTheConfigWhenThereIsNone(t *testing.T) {
	withFakeHome(t)
	st, err := InstallCodex(codexScript)
	if err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if !st.CodexInstalled {
		t.Error("installed and then reported not installed")
	}
	body := readFile(t, codexPath(t))
	if !strings.Contains(body, CodexNotify(codexScript)) {
		t.Errorf("config does not carry the notify line:\n%s", body)
	}
}

// The one that made this a file editor rather than a TOML round-trip.
//
// This machine's config.toml ends with `[tui.model_availability_nux]` and
// `[notice]`. TOML keys belong to the table above them, so a notify appended to
// the end of that file defines `notice.notify` — a key Codex never reads.
// Nothing anywhere reports it: the file still parses, `codex doctor` is happy,
// reading the line back finds it, the settings page says installed, and no
// Codex session ever reports a state. Exactly the silent-in-every-direction
// failure red line 3 is about, from the other end.
func TestInstallCodexPutsNotifyAboveTheFirstTable(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `model = "gpt-5.6"

[model_providers.mine]
name = "cpa"

[notice]
hide_gpt5_1_migration_prompt = true
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))

	notify := strings.Index(body, "notify =")
	firstTable := strings.Index(body, "[model_providers.mine]")
	if notify < 0 {
		t.Fatalf("no notify line at all:\n%s", body)
	}
	if notify > firstTable {
		t.Errorf("notify landed inside a table, where Codex will never read it:\n%s", body)
	}
	// Everything the user had is still there, and still says the same thing.
	for _, keep := range []string{`model = "gpt-5.6"`, `name = "cpa"`, "[notice]",
		"hide_gpt5_1_migration_prompt = true"} {
		if !strings.Contains(body, keep) {
			t.Errorf("installing dropped %q from the user's config:\n%s", keep, body)
		}
	}
}

func TestInstallCodexLeavesEverythingElseByteIdentical(t *testing.T) {
	withFakeHome(t)
	// Comments, alignment and ordering are what a round-trip through a TOML
	// encoder loses. The file is the user's; it has to come back the way they
	// wrote it, with one line more.
	const original = `# my provider, do not touch
model     = "gpt-5.6"   # aligned on purpose
service_tier = "fast"

[projects."/home/jmr"]
trust_level = "trusted"
`
	writeCodexConfig(t, original)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))
	want := strings.Replace(original,
		"service_tier = \"fast\"\n",
		"service_tier = \"fast\"\n"+CodexNotify(codexScript)+"\n",
		1)
	if body != want {
		t.Errorf("the file came back changed in more than the one line.\n got:\n%s\nwant:\n%s", body, want)
	}
}

func TestInstallCodexTwiceWritesOnce(t *testing.T) {
	home := withFakeHome(t)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := readFile(t, codexPath(t))
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("second: %v", err)
	}
	if second := readFile(t, codexPath(t)); second != first {
		t.Errorf("the second install changed the file:\n%s", second)
	}
	// And left no backup recording an edit that did not happen.
	entries, err := os.ReadDir(filepath.Join(home, ".codex"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "vibepanel-backup") {
			t.Errorf("a second install that changed nothing still left %s", e.Name())
		}
	}
}

// An upgrade moves the data directory, and the old line has to go rather than
// sit above the new one as a second notify Codex would parse as a duplicate key.
func TestInstallCodexReplacesOurOwnOlderLine(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `notify = ["/old/path/vibepanel-report.sh", "waiting"]
model = "gpt-5.6"
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))
	if strings.Contains(body, "/old/path") {
		t.Errorf("the old line survived; the file now has two notify keys:\n%s", body)
	}
	if n := strings.Count(body, "notify ="); n != 1 {
		t.Errorf("got %d notify lines, want 1:\n%s", n, body)
	}
}

// Codex has one notify slot, so installing takes it. Taking it silently would
// delete a hook the user wrote — the backup beside the file is not where
// anybody looks.
func TestInstallCodexKeepsSomebodyElsesNotifyInSight(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `notify = ["/home/jmr/bin/my-own-notifier.sh"]
model = "gpt-5.6"
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))
	if !strings.Contains(body, "# ") || !strings.Contains(body, "my-own-notifier.sh") {
		t.Errorf("the user's own notify was deleted rather than commented out:\n%s", body)
	}
	if !strings.Contains(body, CodexNotify(codexScript)) {
		t.Errorf("ours was not installed:\n%s", body)
	}
	// The commented one must not still be a key.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "my-own-notifier.sh") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Errorf("two live notify keys; the file will not load: %q", line)
		}
	}
}

// A notify written across lines is still one key. Replacing only its first line
// leaves `"waiting"]` behind, and Codex will not start on a config that does
// not parse — the panel would have broken the agent it was wiring up.
func TestInstallCodexReplacesAMultiLineNotify(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `notify = [
  "/old/path/vibepanel-report.sh",
  "waiting",
]
model = "gpt-5.6"
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))
	if strings.Contains(body, "/old/path") || strings.Contains(body, "]") && strings.Count(body, "[") != strings.Count(body, "]") {
		t.Errorf("the multi-line array was not replaced whole:\n%s", body)
	}
	if !strings.Contains(body, `model = "gpt-5.6"`) {
		t.Errorf("the key after the array was eaten:\n%s", body)
	}
}

// `notify` under a table is a different key. Rewriting it would edit a setting
// the user meant for something else and leave the one Codex reads unset.
func TestInstallCodexIgnoresANotifyInsideATable(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `model = "gpt-5.6"

[some_plugin]
notify = ["/home/jmr/bin/plugin-thing.sh"]
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	body := readFile(t, codexPath(t))
	if !strings.Contains(body, `notify = ["/home/jmr/bin/plugin-thing.sh"]`) {
		t.Errorf("the plugin's own notify was rewritten:\n%s", body)
	}
	top := strings.Index(body, "[some_plugin]")
	if ours := strings.Index(body, "vibepanel-report.sh"); ours < 0 || ours > top {
		t.Errorf("ours did not go in above the table:\n%s", body)
	}
}

func TestUninstallCodexRemovesOnlyOurs(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `model = "gpt-5.6"
`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	st, err := UninstallCodex(codexScript)
	if err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if st.CodexInstalled {
		t.Error("still reported as installed after removal")
	}
	body := readFile(t, codexPath(t))
	if strings.Contains(body, "notify") {
		t.Errorf("our line survived removal:\n%s", body)
	}
	if !strings.Contains(body, `model = "gpt-5.6"`) {
		t.Errorf("removal took the user's key with it:\n%s", body)
	}

	// Somebody else's is left exactly where it is.
	writeCodexConfig(t, `notify = ["/home/jmr/bin/my-own-notifier.sh"]
`)
	if _, err := UninstallCodex(codexScript); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if body := readFile(t, codexPath(t)); !strings.Contains(body, "my-own-notifier.sh") {
		t.Errorf("removing our hook deleted somebody else's:\n%s", body)
	}
}

func TestUninstallCodexWithNoConfigAtAll(t *testing.T) {
	withFakeHome(t)
	st, err := UninstallCodex(codexScript)
	if err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if st.CodexInstalled {
		t.Error("reported installed with no config file at all")
	}
	if _, err := os.Stat(codexPath(t)); err == nil {
		t.Error("removing a hook that was never installed created the config file")
	}
}

// The path is built from --data-dir, which is a flag. A quote or a backslash in
// it written raw produces a config.toml that does not parse, and Codex refuses
// to start on that.
func TestCodexNotifyQuotesThePath(t *testing.T) {
	line := CodexNotify(`/data/we"ird\path/vibepanel-report.sh`)
	if !strings.Contains(line, `we\"ird`) || !strings.Contains(line, `\\path`) {
		t.Errorf("the path was written into TOML unescaped: %s", line)
	}
}

func TestCodexInstalledIsAboutOurScriptAndNotJustAnyNotify(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, `notify = ["/home/jmr/bin/my-own-notifier.sh"]
`)
	st, err := Inspect(codexScript)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.CodexInstalled {
		t.Error("somebody else's notify was reported as the panel's hook, so the " +
			"settings page would say Codex reporting is installed and nothing " +
			"would ever arrive")
	}
	if st.CodexPath == "" {
		t.Error("Inspect did not say which file it read")
	}
}
