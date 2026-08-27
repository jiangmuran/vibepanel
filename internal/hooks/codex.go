package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodexConfigPath is the file Codex reads its settings from.
func CodexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// InstallCodex writes the notify line into ~/.codex/config.toml.
//
// Line-based rather than parse-and-re-encode, and that is the whole design.
// The file is the user's: this machine's has a model provider, thirty-odd
// `[projects."..."]` tables and a `[notice]` section in it. Every TOML library
// that round-trips a document loses comments and reorders keys, so a panel that
// decoded and re-encoded it would hand back something the user did not write in
// exchange for one line. Editing lines leaves everything else byte-identical.
//
// Writes nothing when the line is already exactly right, for the same reason
// InstallClaude does: the settings page calls this whenever somebody presses
// the button, and rewriting a file to make no change to it still moves its
// mtime and still leaves a backup recording an edit that never happened.
func InstallCodex(scriptPath string) (Status, error) {
	path, err := CodexConfigPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Status{}, fmt.Errorf("hooks: create %s: %w", filepath.Dir(path), err)
	}

	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	after := withCodexNotify(string(before), scriptPath)
	if after == string(before) {
		return Inspect(scriptPath)
	}
	if err := backup(path); err != nil {
		return Status{}, err
	}
	if err := writeFileLike(path, []byte(after)); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// UninstallCodex removes only a notify line this panel wrote.
//
// A notify pointing at somebody else's script is left exactly where it is.
// Codex has one notify slot, so "remove ours" and "remove whatever is there"
// are the same keystroke from the settings page and very much not the same act.
func UninstallCodex(scriptPath string) (Status, error) {
	path, err := CodexConfigPath()
	if err != nil {
		return Status{}, err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Inspect(scriptPath)
		}
		return Status{}, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	after := withoutCodexNotify(string(before))
	if after == string(before) {
		return Inspect(scriptPath)
	}
	if err := backup(path); err != nil {
		return Status{}, err
	}
	if err := writeFileLike(path, []byte(after)); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// codexInstalled reports whether the config's notify line is ours.
func codexInstalled(path string) bool {
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

// ─── editing ──────────────────────────────────────────────────────────────

// withCodexNotify returns the document with our notify line in it.
func withCodexNotify(doc, scriptPath string) string {
	want := CodexNotify(scriptPath)
	lines := strings.Split(doc, "\n")

	if start, end, ok := notifySpan(lines); ok {
		replacement := []string{want}
		if !containsMarker(strings.Join(lines[start:end], "\n")) {
			// Somebody else's notify. Codex has exactly one slot, so installing
			// means taking it — but taking it silently would delete a hook the
			// user wrote, and a backup beside the file is not where anyone
			// looks. Keep the old line where it was, commented, so what
			// happened is legible in the file itself.
			replacement = append([]string{"# replaced by vibepanel:"}, replacement...)
			for _, old := range lines[start:end] {
				replacement = append(replacement, "# "+old)
			}
		}
		out := append([]string{}, lines[:start]...)
		out = append(out, replacement...)
		out = append(out, lines[end:]...)
		return strings.Join(out, "\n")
	}

	// No notify yet: it has to go *before the first table header*.
	//
	// TOML keys belong to the table they follow, so appending to the end of
	// this machine's config would define `notify` inside `[notice]`, which is a
	// different key that Codex never reads. Nothing reports that: the file
	// still parses, `codex doctor` is happy, the settings page reads the line
	// back and says installed, and no session ever reports a state.
	at := len(lines)
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			at = i
			break
		}
	}
	out := append([]string{}, lines[:at]...)
	// Trailing blank lines belong to the gap before the table, not to the keys
	// above it; inserting after them puts our line against the header.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, want)
	if at < len(lines) {
		out = append(out, "")
	}
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
}

// withoutCodexNotify returns the document with our notify line removed.
func withoutCodexNotify(doc string) string {
	lines := strings.Split(doc, "\n")
	start, end, ok := notifySpan(lines)
	if !ok || !containsMarker(strings.Join(lines[start:end], "\n")) {
		return doc
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
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
