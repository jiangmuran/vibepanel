package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

func codexHooksFile(t *testing.T) string {
	t.Helper()
	p, err := CodexHooksPath()
	if err != nil {
		t.Fatalf("CodexHooksPath: %v", err)
	}
	return p
}

func writeCodexConfig(t *testing.T, body string) string {
	t.Helper()
	return writeAt(t, codexPath(t), body)
}

func writeAt(t *testing.T, p, body string) string {
	t.Helper()
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

// ourCommands is event → commands of our entries in hooks.json.
func ourCommands(t *testing.T) map[string][]string {
	t.Helper()
	doc := readJSON(t, codexHooksFile(t))
	out := map[string][]string{}
	hooks, _ := doc["hooks"].(map[string]any)
	for event, v := range hooks {
		list, _ := v.([]any)
		for _, item := range list {
			if !isOurs(item, "") {
				continue
			}
			inner := item.(map[string]any)["hooks"].([]any)
			for _, h := range inner {
				out[event] = append(out[event], h.(map[string]any)["command"].(string))
			}
		}
	}
	return out
}

func TestInstallCodexWritesAHookForEveryTransition(t *testing.T) {
	withFakeHome(t)
	st, err := InstallCodex(codexScript)
	if err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if !st.CodexInstalled {
		t.Error("installed and then reported not installed")
	}
	got := ourCommands(t)
	for event, state := range codexEvents {
		want := codexScript + " " + state + " codex"
		if len(got[event]) != 1 || got[event][0] != want {
			t.Errorf("%s: %v, want exactly [%q]", event, got[event], want)
		}
	}
	// A timeout, because Codex's default is ten minutes per call and a panel
	// that is down would cost that on every tool call.
	doc := readJSON(t, codexHooksFile(t))
	entry := doc["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if entry["timeout"] != float64(codexHookTimeout) {
		t.Errorf("timeout = %v, want %d", entry["timeout"], codexHookTimeout)
	}
	if st.CodexTrust != CodexUntrusted {
		t.Errorf("trust right after installing = %q, want untrusted: Codex has not seen them yet", st.CodexTrust)
	}
}

// The four states the panel needs are all reachable now. This is the property
// the notify line could not have.
func TestCodexHooksReportEveryState(t *testing.T) {
	seen := map[string]bool{}
	for _, state := range codexEvents {
		seen[state] = true
	}
	for _, want := range []string{"working", "waiting", "done"} {
		if !seen[want] {
			t.Errorf("no Codex event reports %q", want)
		}
	}
	if codexEvents["PermissionRequest"] != "waiting" {
		t.Error("an approval prompt must report waiting: it is the moment a person is needed")
	}
}

func TestInstallCodexKeepsTheUsersOwnHooks(t *testing.T) {
	withFakeHome(t)
	const mine = `{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "/home/me/guard.sh" } ] }
    ],
    "Custom": [ { "hooks": [ { "type": "command", "command": "echo hi" } ] } ]
  },
  "other": true
}
`
	writeAt(t, codexHooksFile(t), mine)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	doc := readJSON(t, codexHooksFile(t))
	if doc["other"] != true {
		t.Error("installing dropped a key the user had")
	}
	hooks := doc["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 || !strings.Contains(mustJSON(pre[0]), "guard.sh") {
		t.Errorf("the user's PreToolUse hook moved or went: %s", mustJSON(pre))
	}
	if _, ok := hooks["Custom"]; !ok {
		t.Error("an event the panel does not use was dropped")
	}

	if _, err := UninstallCodex(codexScript); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	after := readJSON(t, codexHooksFile(t))
	var want map[string]any
	if err := json.Unmarshal([]byte(mine), &want); err != nil {
		t.Fatal(err)
	}
	if mustJSON(after) != mustJSON(want) {
		t.Errorf("install then uninstall did not give the file back:\n got %s\nwant %s", mustJSON(after), mustJSON(want))
	}
}

func TestInstallCodexTwiceWritesOnce(t *testing.T) {
	home := withFakeHome(t)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := readFile(t, codexHooksFile(t))
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatalf("second: %v", err)
	}
	if second := readFile(t, codexHooksFile(t)); second != first {
		t.Errorf("the second install changed the file:\n%s", second)
	}
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

// An upgrade moves the data directory: the old entries go, rather than two
// hooks per event each reporting through a different script.
func TestInstallCodexReplacesOurOwnOlderEntries(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallCodex("/old/path/vibepanel-report.sh"); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, codexHooksFile(t))
	if strings.Contains(body, "/old/path") {
		t.Errorf("the old entries survived:\n%s", body)
	}
	for event, cmds := range ourCommands(t) {
		if len(cmds) != 1 {
			t.Errorf("%s has %d hooks of ours", event, len(cmds))
		}
	}
}

// $CODEX_HOME is where Codex reads its configuration when it is set, so it is
// where the hooks have to go.
func TestCodexHomeIsHonoured(t *testing.T) {
	withFakeHome(t)
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks.json")); err != nil {
		t.Errorf("hooks.json is not under CODEX_HOME: %v", err)
	}
}

// An install from before hooks left `notify` in config.toml. Installing hooks
// takes it out -- it would report "waiting" at the end of every turn next to
// the Stop hook's "done" -- and gives back a notify it had replaced.
func TestInstallingHooksRetiresTheOldNotifyLine(t *testing.T) {
	for _, tc := range []struct{ name, legacy, want string }{
		{"ours alone",
			"model = \"gpt-5.6\"\nnotify = [\"/data/hooks/vibepanel-report.sh\", \"waiting\"]\n\n[tui]\ntheme = \"dark\"\n",
			"model = \"gpt-5.6\"\n\n[tui]\ntheme = \"dark\"\n"},
		{"ours over somebody else's",
			"model = \"gpt-5.6\"\n# replaced by vibepanel:\nnotify = [\"/data/hooks/vibepanel-report.sh\", \"waiting\"]\n# notify = [\"/home/jmr/bin/my-own-notifier.sh\"]\n\n[tui]\ntheme = \"dark\"\n",
			"model = \"gpt-5.6\"\nnotify = [\"/home/jmr/bin/my-own-notifier.sh\"]\n\n[tui]\ntheme = \"dark\"\n"},
		{"somebody else's, untouched",
			"notify = [\"/home/jmr/bin/my-own-notifier.sh\"]\nmodel = \"gpt-5.6\"\n",
			"notify = [\"/home/jmr/bin/my-own-notifier.sh\"]\nmodel = \"gpt-5.6\"\n"},
		{"a multi-line array of ours",
			"notify = [\n  \"/old/vibepanel-report.sh\",\n  \"waiting\",\n]\nmodel = \"gpt-5.6\"\n",
			"model = \"gpt-5.6\"\n"},
		{"a notify inside a table is a different key",
			"model = \"gpt-5.6\"\n\n[some_plugin]\nnotify = [\"/x/vibepanel-report.sh\"]\n",
			"model = \"gpt-5.6\"\n\n[some_plugin]\nnotify = [\"/x/vibepanel-report.sh\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFakeHome(t)
			writeCodexConfig(t, tc.legacy)
			st, err := InstallCodex(codexScript)
			if err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, codexPath(t)); got != tc.want {
				t.Errorf("config.toml after install:\n%s\nwant:\n%s", got, tc.want)
			}
			if st.CodexLegacyNotify {
				t.Error("still reported the old notify line after it was removed")
			}
		})
	}
}

func TestInspectSeesALegacyNotifyInstall(t *testing.T) {
	withFakeHome(t)
	writeCodexConfig(t, "notify = [\"/data/hooks/vibepanel-report.sh\", \"waiting\"]\n")
	st, err := Inspect(codexScript)
	if err != nil {
		t.Fatal(err)
	}
	if !st.CodexLegacyNotify || st.CodexInstalled {
		t.Errorf("legacy=%v installed=%v; an old notify install is not the hooks", st.CodexLegacyNotify, st.CodexInstalled)
	}
}

func TestUninstallCodexWithNothingInstalled(t *testing.T) {
	withFakeHome(t)
	st, err := UninstallCodex(codexScript)
	if err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}
	if st.CodexInstalled {
		t.Error("reported installed with nothing there")
	}
	for _, p := range []string{codexPath(t), codexHooksFile(t)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("removing a hook that was never installed created %s", p)
		}
	}
}

func TestCodexInstalledIsAboutOurHooksAndNotJustAnyHooks(t *testing.T) {
	withFakeHome(t)
	writeAt(t, codexHooksFile(t), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/home/me/notify.sh"}]}]}}`)
	st, err := Inspect(codexScript)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.CodexInstalled || len(st.CodexEvents) != 0 || st.CodexTrust != "" {
		t.Errorf("somebody else's hooks read as the panel's: %+v", st)
	}
	if st.CodexPath == "" {
		t.Error("Inspect did not say which file it read")
	}
}

// Codex records a trust decision per handler in config.toml. The keys are the
// ones Codex's own app server listed for this layout (hooks/list, 0.153.4):
// `<hooks.json>:<snake_case event>:<group>:<handler>`, with the group counted
// after the user's own entries.
func TestCodexTrustIsReadFromConfigToml(t *testing.T) {
	withFakeHome(t)
	writeAt(t, codexHooksFile(t), `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatal(err)
	}
	hooksPath := codexHooksFile(t)
	var keys []string
	for event := range codexEvents {
		g := 0
		if event == "PreToolUse" {
			g = 1 // after the user's own
		}
		keys = append(keys, hooksPath+":"+snakeCase(event)+":"+itoa(g)+":0")
	}
	sort.Strings(keys)

	trust := func(n int) string {
		var b strings.Builder
		b.WriteString("model = \"gpt-5.6\"\n")
		for _, k := range keys[:n] {
			b.WriteString("\n[hooks.state.\"" + k + "\"]\ntrusted_hash = \"sha256:abc\"\n")
		}
		writeCodexConfig(t, b.String())
		st, err := Inspect(codexScript)
		if err != nil {
			t.Fatal(err)
		}
		return st.CodexTrust
	}
	if got := trust(0); got != CodexUntrusted {
		t.Errorf("no decisions = %q", got)
	}
	if got := trust(3); got != CodexPartial {
		t.Errorf("some decisions = %q", got)
	}
	if got := trust(len(keys)); got != CodexTrusted {
		t.Errorf("every handler trusted = %q", got)
	}
	// A decision recorded against the user's own hook is not one for ours.
	writeCodexConfig(t, "[hooks.state.\""+hooksPath+":pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:abc\"\n")
	if st, _ := Inspect(codexScript); st.CodexTrust != CodexUntrusted {
		t.Errorf("trusting the user's own hook made ours %q", st.CodexTrust)
	}
}

func TestSnakeCaseMatchesCodexKeys(t *testing.T) {
	for in, want := range map[string]string{
		"PreToolUse": "pre_tool_use", "UserPromptSubmit": "user_prompt_submit",
		"Stop": "stop", "PermissionRequest": "permission_request", "SessionStart": "session_start",
	} {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%s) = %s, want %s", in, got, want)
		}
	}
}

// The snippet the settings page shows is what the installer writes.
func TestTheCodexSnippetIsWhatGetsMerged(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallCodex(codexScript); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, codexHooksFile(t)), CodexHooks(codexScript); got != want {
		t.Errorf("installed file differs from the snippet:\n got %s\nwant %s", got, want)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
