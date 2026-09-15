package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Codex reports its state through its own hooks: the same shape as Claude
// Code's, in ~/.codex/hooks.json, with an event per transition. It used to be
// wired through `notify`, which fires once when a turn ends -- so a Codex
// session could report "waiting" and nothing else, and once it had, nothing it
// ever did afterwards could say otherwise. See the build log entry "Codex
// states from Codex's own hooks".
//
// Verified against codex-cli 0.153.4 through its app server (`hooks/list`, on a
// throwaway CODEX_HOME): every event below is accepted, `timeout` is honoured,
// an unknown `_source` field is ignored, and each handler is keyed
// `<hooks.json path>:<snake_case event>:<group>:<handler>`.

// codexEvents maps a Codex hook event to the state it reports.
//
// PermissionRequest is the one that matters: it fires before Codex shows an
// approval prompt, which is the moment a person is needed. PostToolUse is here
// as well as PreToolUse because an approved tool call resumes work without a
// new prompt, and the report that it did has to come from somewhere.
// SessionStart is "done" because a Codex that has just opened is sitting at its
// prompt, which the panel calls finished.
var codexEvents = map[string]string{
	"SessionStart":      "done",
	"UserPromptSubmit":  "working",
	"PreToolUse":        "working",
	"PostToolUse":       "working",
	"PermissionRequest": "waiting",
	"Stop":              "done",
	"Interrupt":         "done",
}

// codexHookTimeout is the seconds Codex waits for our handler. Codex's default
// is 600, which a panel that is down would spend on every tool call; the script
// gives curl two seconds and exits.
const codexHookTimeout = 5

// codexSource is the argument that marks a report as coming from Codex's hooks.
const codexSource = "codex"

// CodexLegacySource marks a report from the old `notify` line, which the server
// treats differently: it can only ever say "waiting", so nothing else will ever
// replace it. See session.Detector.
const CodexLegacySource = "codex-notify"

// codexHome is where Codex keeps its configuration: $CODEX_HOME, as Codex
// itself reads it, or ~/.codex.
func codexHome() (string, error) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

// CodexConfigPath is the file Codex reads its settings from.
func CodexConfigPath() (string, error) {
	dir, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// CodexHooksPath is the file the panel writes Codex's hooks into.
func CodexHooksPath() (string, error) {
	dir, err := codexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks.json"), nil
}

// CodexHooks renders the hooks.json the panel merges, for the settings page to
// show before anybody presses install. Built from the same entries the
// installer writes, so what is read is what is merged.
func CodexHooks(script string) string {
	doc := map[string]any{"hooks": map[string]any{}}
	hooks := doc["hooks"].(map[string]any)
	for event, state := range codexEvents {
		hooks[event] = mergeCodexEvent(nil, script, state)
	}
	return string(encode(doc))
}

// InstallCodex merges the panel's hooks into ~/.codex/hooks.json, and takes an
// older install's `notify` line back out of config.toml.
//
// Merged exactly like Claude's settings: the user's own hooks on the same events
// stay where they are, the file is backed up before an edit, and nothing is
// written when it is already right. The notify line comes out because leaving
// it would report "waiting" at the end of every turn next to the hook's "done",
// and whichever arrived second would win.
func InstallCodex(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	hooksPath, err := CodexHooksPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readSettings(hooksPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	before := encode(doc)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, state := range codexEvents {
		hooks[event] = mergeCodexEvent(hooks[event], scriptPath, state)
	}
	doc["hooks"] = hooks
	if !unchanged(before, doc) {
		if err := backup(hooksPath); err != nil {
			return Status{}, err
		}
		if err := writeSettings(hooksPath, doc); err != nil {
			return Status{}, err
		}
	}
	if err := removeCodexNotify(); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// UninstallCodex removes only what this panel wrote: its hooks.json entries,
// and a notify line an older install left.
func UninstallCodex(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	hooksPath, err := CodexHooksPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readSettings(hooksPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Status{}, err
	default:
		before := encode(doc)
		hooks, _ := doc["hooks"].(map[string]any)
		for event := range codexEvents {
			cleaned := removeOurs(hooks[event], scriptPath)
			if cleaned == nil {
				delete(hooks, event)
			} else {
				hooks[event] = cleaned
			}
		}
		if len(hooks) == 0 {
			delete(doc, "hooks")
		} else {
			doc["hooks"] = hooks
		}
		if !unchanged(before, doc) {
			if err := backup(hooksPath); err != nil {
				return Status{}, err
			}
			if err := writeSettings(hooksPath, doc); err != nil {
				return Status{}, err
			}
		}
	}
	if err := removeCodexNotify(); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// mergeCodexEvent is mergeEvent for Codex: the same entry, with the source
// argument that tells the server which agent's hook this is, and a timeout.
func mergeCodexEvent(existing any, scriptPath, state string) any {
	list, _ := existing.([]any)
	out := make([]any, 0, len(list)+1)
	for _, item := range list {
		if !isOurs(item, "") {
			out = append(out, item)
		}
	}
	return append(out, map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command(scriptPath, state) + " " + codexSource,
				"timeout": codexHookTimeout,
				"_source": marker,
			},
		},
	})
}

// CodexHooksInstalled reports whether every Codex event has the panel's hook.
func CodexHooksInstalled() bool {
	path, err := CodexHooksPath()
	if err != nil {
		return false
	}
	return len(codexHookEvents(path)) == len(codexEvents)
}

// codexHookEvents lists the events whose hooks.json entries are ours, sorted.
func codexHookEvents(path string) []string {
	out := []string{}
	doc, err := readSettings(path)
	if err != nil {
		return out
	}
	hooks, _ := doc["hooks"].(map[string]any)
	for event := range codexEvents {
		if entriesFor(hooks, event, "") {
			out = append(out, event)
		}
	}
	sort.Strings(out)
	return out
}

// CodexTrust is whether Codex will run the hooks the panel installed.
//
// Codex runs a hook from a user's hooks.json only after the user has reviewed
// and trusted that exact definition with `/hooks`, and it records the decision
// in config.toml as `[hooks.state."<key>"]` with a `trusted_hash`. The panel
// never writes that: trusting a command on the user's behalf is the one step
// Codex put a person in front of, and a panel that skipped it would be the
// thing that review exists to catch.
//
// What it can read is whether a decision exists for each of its handlers.
// "trusted" means every one has a recorded hash; the hash itself is Codex's to
// compare, so a definition changed after it was trusted still reads trusted
// here and Codex reports it as modified -- the settings page's "reports
// arriving" line is what catches that. "untrusted" means none has one,
// "partial" some, and "" that nothing is installed.
const (
	CodexTrusted   = "trusted"
	CodexUntrusted = "untrusted"
	CodexPartial   = "partial"
)

func codexTrust(hooksPath, configPath string) string {
	doc, err := readSettings(hooksPath)
	if err != nil {
		return ""
	}
	hooks, _ := doc["hooks"].(map[string]any)
	var keys []string
	for event := range codexEvents {
		list, _ := hooks[event].([]any)
		for g, item := range list {
			entry, _ := item.(map[string]any)
			inner, _ := entry["hooks"].([]any)
			for h, hook := range inner {
				m, _ := hook.(map[string]any)
				cmd, _ := m["command"].(string)
				if m["_source"] == marker || containsMarker(cmd) {
					keys = append(keys, fmt.Sprintf("%s:%s:%d:%d", hooksPath, snakeCase(event), g, h))
				}
			}
		}
	}
	if len(keys) == 0 {
		return ""
	}
	trusted := trustedHookKeys(configPath)
	n := 0
	for _, k := range keys {
		if trusted[k] {
			n++
		}
	}
	switch n {
	case len(keys):
		return CodexTrusted
	case 0:
		return CodexUntrusted
	}
	return CodexPartial
}

// trustedHookKeys reads the `[hooks.state."<key>"]` tables in config.toml that
// carry a trusted_hash. Line-based, like everything else this package does to
// that file, and only reading.
func trustedHookKeys(configPath string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return out
	}
	current := ""
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			current = ""
			if key, ok := strings.CutPrefix(trimmed, `[hooks.state."`); ok {
				if key, ok = strings.CutSuffix(key, `"]`); ok {
					current = strings.ReplaceAll(key, `\\`, `\`)
				}
			}
			continue
		}
		if current == "" {
			continue
		}
		if name, value, found := strings.Cut(trimmed, "="); found &&
			strings.TrimSpace(name) == "trusted_hash" && strings.Trim(strings.TrimSpace(value), `"`) != "" {
			out[current] = true
		}
	}
	return out
}

// snakeCase is how Codex spells an event inside a hook's key: PreToolUse is
// pre_tool_use.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// removeCodexNotify takes a notify line an older install wrote out of
// config.toml, giving the slot back to whatever it replaced.
func removeCodexNotify() error {
	path, err := CodexConfigPath()
	if err != nil {
		return err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hooks: read %s: %w", path, err)
	}
	after := withoutCodexNotify(string(before))
	if after == string(before) {
		return nil
	}
	if err := backup(path); err != nil {
		return err
	}
	return writeFileLike(path, []byte(after))
}

// codexNotifyInstalled reports whether config.toml still has an older install's
// notify line in it.
func codexNotifyInstalled(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	start, end, ok := notifySpan(lines)
	if !ok {
		return false
	}
	return containsMarker(strings.Join(lines[start:end], "\n"))
}

// ─── editing an older install's notify line ───────────────────────────────

// codexReplacedBanner marks the notify an older install took the slot from.
//
// Read by the uninstall to give the line back. Older installs wrote it; this
// build only ever reads it, and the constant is kept so the two cannot drift.
const codexReplacedBanner = "# replaced by vibepanel:"

// withoutCodexNotify returns the document with an older install's notify line
// removed.
func withoutCodexNotify(doc string) string {
	lines := strings.Split(doc, "\n")
	start, end, ok := notifySpan(lines)
	if !ok || !containsMarker(strings.Join(lines[start:end], "\n")) {
		return doc
	}
	// Give the slot back to whoever had it before we took it.
	//
	// Removing only our own line left the commented original under the banner
	// for good: `codexInstalled` says false, the settings page says not
	// installed, the file reads as though it were the user's again -- and their
	// notifier never runs again, with nothing anywhere saying why. An install
	// that cannot be undone is worse than one that refuses the slot.
	restored, drop := replacedNotify(lines, start, end)
	if drop > 0 {
		start-- // the banner, immediately above our line
		end += drop
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, restored...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// replacedNotify finds the notify withCodexNotify parked under its banner and
// returns it uncommented, with the number of commented lines it occupies.
//
// The shape has to be exactly the one the install writes -- banner immediately
// above our span, a commented `notify` assignment immediately below it -- and
// the assignment is read with notifySpan's own rules, so a multi-line array
// comes back whole. Anything else is somebody's own comment and is left where
// it is: this runs on a file the panel does not own, and uncommenting a line a
// person wrote is the same class of surprise as deleting one.
func replacedNotify(lines []string, start, end int) ([]string, int) {
	if start == 0 || strings.TrimSpace(lines[start-1]) != codexReplacedBanner {
		return nil, 0
	}
	var out []string
	depth := 0
	for i := end; i < len(lines); i++ {
		body, ok := strings.CutPrefix(lines[i], "# ")
		if !ok {
			break
		}
		if len(out) == 0 {
			key, value, found := strings.Cut(strings.TrimSpace(body), "=")
			if !found || strings.TrimSpace(key) != "notify" {
				return nil, 0
			}
			depth = bracketDepth(value)
		} else {
			depth += bracketDepth(body)
		}
		out = append(out, body)
		if depth <= 0 {
			return out, len(out)
		}
	}
	return nil, 0
}

// notifySpan finds the top-level `notify` assignment, as a [start, end) range.
//
// Top-level only: a `notify` under `[tui]` or inside one of the `[projects...]`
// tables is a different key, and rewriting it would edit a setting the user
// meant for something else while leaving the one Codex reads untouched.
//
// The range is a range because a TOML array may be written across lines, and
// replacing only the first of them leaves `"waiting"]` behind as a syntax error
// in a file the agent will not start without.
func notifySpan(lines []string) (start, end int, ok bool) {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			return 0, 0, false // a table starts here; the top level is over
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found || strings.TrimSpace(key) != "notify" {
			continue
		}
		depth := bracketDepth(value)
		j := i
		for depth > 0 && j+1 < len(lines) {
			j++
			depth += bracketDepth(lines[j])
		}
		return i, j + 1, true
	}
	return 0, 0, false
}

// bracketDepth counts unclosed square brackets outside of quoted strings.
//
// Quote-aware because a script path is allowed to contain a bracket, and a
// panel installed under /opt/vp[1] would otherwise swallow the rest of the
// file into an array that never closes.
func bracketDepth(s string) int {
	depth := 0
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case quote != 0 && r == '\\' && quote == '"':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return depth // a comment; brackets in it are prose
		case r == '[':
			depth++
		case r == ']':
			depth--
		}
	}
	return depth
}

// writeFileLike writes bytes to path, keeping the mode the file already had.
//
// Same rules as writeSettings, for the same reasons: an agent's config file
// truncated by a crash halfway through is an agent that will not start, and
// silently tightening the permissions on somebody's dotfile is its own
// surprise.
func writeFileLike(path string, b []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	// A name nobody else can be holding, rather than the fixed
	// `<path>.vibepanel.tmp` this used. Two writers of one file were staging
	// through the same temp path, so what got renamed over the user's settings
	// was a splice of two encodings — a file the agent will not start on. editMu
	// makes that impossible inside this process; the unique name is what covers
	// the admin CLI running at the same time, and a leftover from a crash that
	// nothing will rename over the real file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vibepanel-*.tmp")
	if err != nil {
		return fmt.Errorf("hooks: stage %s: %w", path, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()     //nolint:errcheck
		os.Remove(name) //nolint:errcheck
		return fmt.Errorf("hooks: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name) //nolint:errcheck
		return fmt.Errorf("hooks: write %s: %w", name, err)
	}
	// CreateTemp makes the file 0600 whatever the target had, so the mode is put
	// back here — otherwise a rename would quietly tighten the permissions on
	// somebody's dotfile.
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name) //nolint:errcheck
		return fmt.Errorf("hooks: set mode on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name) //nolint:errcheck
		return fmt.Errorf("hooks: replace %s: %w", path, err)
	}
	return nil
}
