package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tomlString quotes a value for a TOML basic string.
//
// The script path is built from the data directory, which is a flag: it can
// contain a backslash or a quote, and either one written raw produces a
// config.toml that does not parse. Kimi Code refuses to start on that, so the
// panel would have broken the agent it was trying to wire up -- and this
// string is not only shown, it is what InstallKimi writes.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// kimiEvents maps a Kimi Code hook event to the state it reports, in the order
// the blocks are written.
//
// The mapping is Claude Code's: a prompt or a tool call means working, a
// permission prompt means waiting, the turn ending means done. Kimi fires
// Interrupt in place of Stop when somebody presses Esc, so without the last
// entry an interrupted session would read as working for good.
var kimiEvents = []struct{ event, state string }{
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"PermissionRequest", "waiting"},
	{"Stop", "done"},
	{"Interrupt", "done"},
}

// KimiConfigPath is the file Kimi Code reads its [[hooks]] from.
func KimiConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".kimi-code", "config.toml"), nil
}

// KimiHooks renders the [[hooks]] blocks for the settings page.
//
// The same text the installer appends: the snippet's promise is that what you
// read is what gets merged.
func KimiHooks(script string) string {
	var b strings.Builder
	for i, e := range kimiEvents {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[[hooks]]\nevent = %s\ncommand = %s\n",
			tomlString(e.event), tomlString(command(script, e.state)))
	}
	return b.String()
}

// InstallKimi appends the panel's [[hooks]] blocks to ~/.kimi-code/config.toml.
//
// Line-based rather than parse-and-re-encode, for the same reason the Codex
// config.toml edit is: the file is the user's, full of providers and model
// tables, and every TOML round-trip loses comments and reorders keys. The
// blocks are appended at the end of the file — each [[hooks]] header starts a
// new table, so nothing that precedes them changes meaning, and a block is
// recognised on removal by the marker in its command rather than by position.
func InstallKimi(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := KimiConfigPath()
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
	after := withoutKimiHooks(string(before))
	if after != "" && !strings.HasSuffix(after, "\n") {
		after += "\n"
	}
	if after != "" {
		after += "\n"
	}
	after += KimiHooks(scriptPath)
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

// UninstallKimi removes only the [[hooks]] blocks this panel wrote.
func UninstallKimi(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := KimiConfigPath()
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
	after := withoutKimiHooks(string(before))
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

// kimiHookEvents lists the events whose blocks in the file are ours, in
// kimiEvents order. Never nil: the wire contract says "nothing installed" is
// an empty list, not null.
func kimiHookEvents(path string) []string {
	out := []string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	body := string(b)
	for _, e := range kimiEvents {
		for _, block := range kimiHookBlocks(body) {
			if strings.Contains(block, `event = "`+e.event+`"`) && containsMarker(block) {
				out = append(out, e.event)
				break
			}
		}
	}
	return out
}

// kimiHookBlocks returns each [[hooks]] block in the document, header to the
// next table header or EOF.
func kimiHookBlocks(doc string) []string {
	lines := strings.Split(doc, "\n")
	var blocks []string
	for i := 0; i < len(lines); {
		if !isKimiHooksHeader(lines[i]) {
			i++
			continue
		}
		end := i + 1
		for end < len(lines) && !isTableHeader(lines[end]) {
			end++
		}
		blocks = append(blocks, strings.Join(lines[i:end], "\n"))
		i = end
	}
	return blocks
}

// withoutKimiHooks drops every [[hooks]] block whose command carries the
// marker, leaving the user's own hooks and everything else byte-identical.
//
// Removal is by content rather than position, so a block the user moved or
// copied elsewhere in the file is still recognised — and a block they wrote
// themselves, on the same events, is left alone.
func withoutKimiHooks(doc string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if !isKimiHooksHeader(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		end := i + 1
		for end < len(lines) && !isTableHeader(lines[end]) {
			end++
		}
		if !containsMarker(strings.Join(lines[i:end], "\n")) {
			out = append(out, lines[i:end]...)
		} else {
			// The block ends at the next table header, so anything the user
			// wrote between our last key and that header -- a blank line and
			// the comment introducing their own section -- is inside the range
			// being dropped. Their comment went with our hooks. Keep the
			// trailing run of blank and comment lines: it reads as belonging
			// to what follows, and it is never ours, because KimiHooks writes
			// neither.
			out = append(out, trailingKept(lines[i:end])...)
		}
		i = end
	}
	return strings.Join(out, "\n")
}

// trailingKept is the run of blank and comment lines at the end of a block,
// and nothing when that run is only blank lines.
//
// The blank line is the one the installer itself writes between blocks, so
// keeping it would leave one more empty line in the file on every
// install-remove cycle. A comment in there is somebody's, and is kept with the
// blanks around it exactly as it was written.
func trailingKept(block []string) []string {
	keep := len(block)
	comment := false
	for keep > 0 {
		t := strings.TrimSpace(block[keep-1])
		if t == "" {
			keep--
			continue
		}
		if strings.HasPrefix(t, "#") {
			comment = true
			keep--
			continue
		}
		break
	}
	if !comment {
		return nil
	}
	return block[keep:]
}

func isKimiHooksHeader(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[[") || !strings.HasSuffix(t, "]]") {
		return false
	}
	return strings.TrimSpace(t[2:len(t)-2]) == "hooks"
}

// isTableHeader reports whether a line opens a TOML table, which is what ends
// a [[hooks]] block. Comments are not headers.
func isTableHeader(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "#")
}
