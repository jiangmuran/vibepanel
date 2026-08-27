package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// marker tags the hook entries this panel manages.
//
// Without it, uninstalling would have to guess which entries were ours, and a
// user's own hooks on the same events would be collateral damage.
const marker = "vibepanel-report"

// ClaudeSettingsPath is the file the panel offers to edit.
func ClaudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// Status describes what is currently installed.
type Status struct {
	SettingsPath string `json:"settingsPath"`
	ScriptPath   string `json:"scriptPath"`
	Installed    bool   `json:"installed"`
	// Events lists the hook events currently pointing at our script.
	Events []string `json:"events"`
	// Snippet is what would be merged, for the user to read before agreeing.
	Snippet string `json:"snippet"`
	// CodexSnippet is the equivalent for Codex: the one line that goes into
	// config.toml, shown before the button is pressed the same way Snippet is.
	CodexSnippet string `json:"codexSnippet"`
	// CodexPath is the file that line goes into.
	CodexPath string `json:"codexPath"`
	// CodexInstalled reports whether that file's notify is ours.
	//
	// Separate from Installed rather than folded into it. They are two agents
	// configured by two mechanisms that fail separately -- the runbook has a
	// section for exactly that -- and one flag would make a page that can only
	// say "hooks are installed" about a machine where half of them are.
	CodexInstalled bool `json:"codexInstalled"`
}

// events maps a hook event to the state it reports.
var events = map[string]string{
	"Notification":     "waiting",
	"Stop":             "done",
	"UserPromptSubmit": "working",
	"PreToolUse":       "working",
}

// Inspect reports what is installed without changing anything.
func Inspect(scriptPath string) (Status, error) {
	settingsPath, err := ClaudeSettingsPath()
	if err != nil {
		return Status{}, err
	}
	codexPath, err := CodexConfigPath()
	if err != nil {
		return Status{}, err
	}
	st := Status{
		SettingsPath:   settingsPath,
		ScriptPath:     scriptPath,
		Snippet:        ClaudeSettings(scriptPath),
		CodexSnippet:   CodexNotify(scriptPath),
		CodexPath:      codexPath,
		CodexInstalled: codexInstalled(codexPath),
		Events:         []string{},
		// Empty here rather than normalised at the end, because there are two
		// returns and the early one -- no settings file, which is the state of
		// every fresh install -- skipped the normalisation and sent `null`.
		//
		// It passed locally for a year: this machine has a settings.json, so
		// the early return was never taken and the test that exists for this
		// exact field never exercised the path it was written for. CI, on a
		// runner with no ~/.claude, took it on the first try.
	}

	doc, err := readSettings(settingsPath)
	if err != nil {
		// A missing or unreadable file simply means nothing is installed.
		return st, nil
	}
	hooks, _ := doc["hooks"].(map[string]any)
	for event := range events {
		if entriesFor(hooks, event, scriptPath) {
			st.Events = append(st.Events, event)
		}
	}
	// Sorted, because `events` is a map and Go randomises the order it walks
	// one. This list goes to the settings page, so without this it arrives in a
	// different order every time it is asked for — a list that reshuffles while
	// somebody is reading it, which this project calls hostile where it happens
	// to a tab strip.
	//
	// Pinned by TestTheEventListComesBackInTheSameOrderEveryTime, which asks
	// twenty times: one call cannot tell a sort from a lucky shuffle.
	sort.Strings(st.Events)
	st.Installed = len(st.Events) > 0
	return st, nil
}

// InstallClaude merges the panel's hooks into the user's settings.
//
// Merges rather than replaces, and copies the file beside itself before any
// edit. That file is the user's, it usually contains other things, and a tool
// that rewrites it wholesale is a tool people stop trusting — once,
// permanently.
//
// Writes nothing at all when the hooks are already exactly right, which is the
// common case: the settings page calls this whenever somebody presses the
// button, and re-encoding a file to make no change to it still reformats it,
// still moves its mtime, and still leaves a backup recording an edit that never
// happened.
func InstallClaude(scriptPath string) (Status, error) {
	settingsPath, err := ClaudeSettingsPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return Status{}, fmt.Errorf("hooks: create %s: %w", filepath.Dir(settingsPath), err)
	}

	doc, err := readSettings(settingsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	// What the file already says, put through the same encoder we are about to
	// write with, so the comparison below is about content and not indentation.
	before := encode(doc)

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, state := range events {
		hooks[event] = mergeEvent(hooks[event], scriptPath, state)
	}
	doc["hooks"] = hooks

	if unchanged(before, doc) {
		// Already exactly right. Writing anyway would reformat the user's file
		// and leave a backup beside it recording a change that was not made,
		// which is how a settings file ends up surrounded by copies of itself.
		return Inspect(scriptPath)
	}
	if err := backup(settingsPath); err != nil {
		return Status{}, err
	}
	if err := writeSettings(settingsPath, doc); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// UninstallClaude removes only the entries this panel added.
func UninstallClaude(scriptPath string) (Status, error) {
	settingsPath, err := ClaudeSettingsPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readSettings(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Inspect(scriptPath)
		}
		return Status{}, err
	}
	before := encode(doc)

	// A nil hooks map is fine to read from and to delete from; assigning would
	// panic, but removeOurs only returns non-nil when it was given a list,
	// which a nil map cannot produce.
	hooks, _ := doc["hooks"].(map[string]any)
	for event := range events {
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

	if unchanged(before, doc) {
		// Nothing of ours was in there. Removing nothing should leave no trace,
		// not a reformatted file and a backup of it.
		return Inspect(scriptPath)
	}
	if err := backup(settingsPath); err != nil {
		return Status{}, err
	}
	if err := writeSettings(settingsPath, doc); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// ─── file handling ────────────────────────────────────────────────────────

func readSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("hooks: %s is not valid JSON: %w", path, err)
	}
	return doc, nil
}

// encode renders a settings document exactly as writeSettings would.
//
// Separate so that "would writing this change anything?" can be answered
// without touching the disk. Map keys are sorted by encoding/json, so the
// output is stable for the same content.
func encode(doc map[string]any) []byte {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// unchanged reports whether writing doc would produce the bytes in before.
//
// A nil before means the earlier encode failed, which is not a licence to skip
// the write: when in doubt, do the work rather than silently doing nothing.
func unchanged(before []byte, doc map[string]any) bool {
	if before == nil {
		return false
	}
	after := encode(doc)
	return after != nil && bytes.Equal(before, after)
}

func writeSettings(path string, doc map[string]any) error {
	b := encode(doc)
	if b == nil {
		return fmt.Errorf("hooks: encode settings for %s", path)
	}

	// Keep the mode the user's file already had. Forcing 0600 would tighten it
	// silently, and quietly changing the permissions on somebody's dotfile is
	// the same kind of surprise as rewriting its contents. New files start
	// private, because this one ends up holding paths and tokens.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	// Write beside the target and rename: a crash halfway through must not
	// leave the user with a truncated settings file and an agent that will not
	// start.
	tmp := path + ".vibepanel.tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return fmt.Errorf("hooks: write %s: %w", tmp, err)
	}
	// WriteFile only applies the mode when it creates the file, so a leftover
	// temp file from an earlier crash would keep its own — and get renamed over
	// the real one, taking its permissions with it.
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("hooks: set mode on %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("hooks: replace %s: %w", path, err)
	}
	return nil
}

// backup keeps a copy beside the original before any edit.
func backup(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing to lose
	}
	if err != nil {
		return fmt.Errorf("hooks: read %s: %w", path, err)
	}
	// Millisecond resolution, not seconds. Installing and then removing takes
	// well under a second, and at second resolution the second backup
	// overwrites the first — which is exactly when the original is lost.
	stamp := time.Now().UTC().Format("20060102-150405.000")
	dest := fmt.Sprintf("%s.vibepanel-backup-%s", path, stamp)
	if _, err := os.Stat(dest); err == nil {
		// Still taken: never overwrite a backup, whatever the clock says.
		for i := 1; i < 100; i++ {
			candidate := fmt.Sprintf("%s.%d", dest, i)
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				dest = candidate
				break
			}
		}
	}
	if err := os.WriteFile(dest, b, 0o600); err != nil {
		return fmt.Errorf("hooks: back up %s: %w", path, err)
	}
	return nil
}

// ─── merging ──────────────────────────────────────────────────────────────

// command is what an installed entry runs.
func command(scriptPath, state string) string {
	return scriptPath + " " + state
}

// mergeEvent adds our entry to whatever is already configured for an event,
// replacing an older one of ours if the script path changed.
func mergeEvent(existing any, scriptPath, state string) any {
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
				"command": command(scriptPath, state),
				// The tag is how uninstalling knows which entries are ours and
				// leaves the user's own alone.
				"_source": marker,
			},
		},
	})
}

func removeOurs(existing any, scriptPath string) any {
	list, _ := existing.([]any)
	var out []any
	for _, item := range list {
		if !isOurs(item, scriptPath) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isOurs reports whether an entry was installed by this panel.
//
// The tag is authoritative; the script path is a fallback for entries written
// by an older version, or pasted by hand from the CLI output.
func isOurs(item any, scriptPath string) bool {
	entry, ok := item.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := entry["hooks"].([]any)
	for _, h := range inner {
		hook, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if hook["_source"] == marker {
			return true
		}
		if cmd, ok := hook["command"].(string); ok {
			if scriptPath != "" && len(cmd) >= len(scriptPath) && cmd[:len(scriptPath)] == scriptPath {
				return true
			}
			if containsMarker(cmd) {
				return true
			}
		}
	}
	return false
}

func containsMarker(s string) bool {
	const name = "vibepanel-report.sh"
	for i := 0; i+len(name) <= len(s); i++ {
		if s[i:i+len(name)] == name {
			return true
		}
	}
	return false
}

func entriesFor(hooks map[string]any, event, scriptPath string) bool {
	if hooks == nil {
		return false
	}
	list, _ := hooks[event].([]any)
	for _, item := range list {
		if isOurs(item, scriptPath) {
			return true
		}
	}
	return false
}
