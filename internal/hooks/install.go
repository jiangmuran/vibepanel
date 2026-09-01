package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// editMu serialises every read-modify-write of an agent's configuration file.
//
// Held by InstallClaude, UninstallClaude, ApplyTune, InstallCodex and
// UninstallCodex — everything that reads one of those files, changes what it
// read and writes it back. One lock for all of them rather than one per file:
// these are button presses on a settings page, so there is no contention worth
// measuring, and a single lock cannot be taken in two orders.
//
// Without it there were three unsynchronised cycles over
// ~/.claude/settings.json with nothing between them: the settings page's hooks
// row and its tune row carry separate busy flags, and the first-run tour presses
// one and then the other while the first is still in flight. Each encodes a
// document built from a read taken before the other's write, so the later rename
// discards the earlier edit. Measured over 200 runs of an install racing a tune:
// one correct file, 177 with an edit silently gone and 22 that json.Unmarshal
// refuses — and Claude Code does not start on that last one, which is the
// outcome writeSettings' atomic rename exists to prevent.
//
// Not a cross-process lock. `vibepanel tune --apply` from a shell while the
// panel serves an install is still a race; it is a much narrower one now that
// the two never share a temp file, and closing it wants the flock in
// internal/config/lock.go.
var editMu sync.Mutex

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

	// OpencodePath is the plugin file, and OpencodeInstalled says whether the
	// one in place is *this build's*.
	//
	// Byte equality, not "a file exists": an older reporter left by an older
	// build is installed and wrong, and a page that says installed about it is
	// the failure this project keeps finding -- reading a configuration file
	// rather than whether anything ever arrived.
	OpencodePath      string `json:"opencodePath"`
	OpencodeInstalled bool   `json:"opencodeInstalled"`
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
	// Everything that does not depend on the Claude settings file is filled in
	// here, in the literal, and nothing below adds a field.
	//
	// Because there are two returns and the early one is the fresh install --
	// no ~/.claude/settings.json, which is also every user who runs Codex or
	// opencode and not Claude Code. Anything assigned after `readSettings`
	// fails is silently absent for exactly those people. It happened to
	// `Events`, which went out as `null`; the comment below records that. It
	// then happened again to OpencodePath and OpencodeInstalled, added later at
	// the bottom of the function, so the settings page drew a blank path and
	// said opencode was not installed while its plugin was in place and
	// reporting -- a false claim about hooks, which red line 3 is about,
	// arriving with no error anywhere.
	//
	// Pinned by TestOpencodeIsReportedWithNoClaudeSettingsFile.
	opencodePath, _ := OpencodePluginPath()

	st := Status{
		SettingsPath:      settingsPath,
		ScriptPath:        scriptPath,
		Snippet:           ClaudeSettings(scriptPath),
		CodexSnippet:      CodexNotify(scriptPath),
		CodexPath:         codexPath,
		CodexInstalled:    codexInstalled(codexPath),
		OpencodePath:      opencodePath,
		OpencodeInstalled: OpencodeInstalled(),
		Events:            []string{},
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
	editMu.Lock()
	defer editMu.Unlock()
	settingsPath, err := ClaudeSettingsPath()
	if err != nil {
		return Status{}, err
	}
	// The directory is writeSettings' business now; see the note there.

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
	editMu.Lock()
	defer editMu.Unlock()
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

	// The directory, here rather than in each caller.
	//
	// It was in InstallClaude only, so the next function to write this file
	// worked on every machine that had ever run Claude Code and failed on a
	// fresh one -- with `open ...settings.json.vibepanel.tmp: no such file or
	// directory`, from a temporary path the person never asked about. That is
	// the whole of the installer's audience on a new box.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("hooks: create %s: %w", filepath.Dir(path), err)
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
//
// Claude Code hands this string to a shell, so the path has to survive one.
// `--data-dir "/opt/my panel"` produced `/opt/my panel/hooks/vibepanel-report.sh
// waiting`, which a shell splits at the space and answers with 127 — and
// nothing anywhere says so: report.sh suppresses its own failures by design,
// `isOurs` matches on the marker rather than on whether the command can run, so
// Inspect reports all four events installed, and every session quietly falls
// back to the heuristic. That is the shape red line 3 is about. The Codex side
// never had this because codex execs an argv array with no shell in it.
func command(scriptPath, state string) string {
	return shellWord(scriptPath) + " " + state
}

// shellWord returns s as a single word to a POSIX shell.
//
// Quoted only when it has to be, because this string is also the snippet the
// settings page shows before anybody presses install, and the promise there is
// that what you read is what gets merged. An ordinary path is left exactly as
// it was written.
func shellWord(s string) string {
	safe := s != ""
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._-+:@%,=", r):
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	// Single quotes, which a shell takes literally down to the backslash. The
	// only thing they cannot hold is a single quote, hence the dance.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
