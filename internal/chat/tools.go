package chat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The write path into a pane, per agent.
//
// "Allow" is not one keystroke. Claude Code draws a menu with the first
// choice highlighted, so Enter allows and Escape refuses; Codex's approval
// prompt reads y and n; a shell has no prompt to answer at all. Getting this
// wrong is the most expensive mistake this package can make -- a "y" typed
// into the wrong program is an instruction, not an answer -- so the mapping is
// data, per tool, editable from the settings page, and a tool without a
// profile is refused rather than guessed at.
//
// The defaults for opencode, Kimi Code and zcode are Claude Code's, because
// their hook shapes are Claude Code's and their prompts were drawn to look
// like it; they are defaults, not measurements, and the settings page says so.

// ToolProfile is how one agent's prompts are answered.
type ToolProfile struct {
	// Approve, Deny and Interrupt are tmux key names, pressed in order.
	Approve   []string `json:"approve"`
	Deny      []string `json:"deny"`
	Interrupt []string `json:"interrupt"`
	// Submit follows a pasted instruction. Enter for every agent so far, and
	// kept per tool because the first one that wants Ctrl-Enter will be
	// wired up here rather than special-cased.
	Submit []string `json:"submit"`
}

// ToolsKey is the settings row holding the profiles, as JSON.
const ToolsKey = "chat.tools"

// DefaultTools is what a fresh panel answers prompts with.
func DefaultTools() map[string]ToolProfile {
	claudeLike := ToolProfile{
		Approve: []string{"Enter"}, Deny: []string{"Escape"},
		Interrupt: []string{"Escape"}, Submit: []string{"Enter"},
	}
	return map[string]ToolProfile{
		"claude":   claudeLike,
		"codex":    {Approve: []string{"y"}, Deny: []string{"n"}, Interrupt: []string{"Escape"}, Submit: []string{"Enter"}},
		"opencode": claudeLike,
		"kimi":     claudeLike,
		"zcode":    claudeLike,
		// A shell has no prompt; Interrupt and Submit are all it needs.
		"shell": {Interrupt: []string{"C-c"}, Submit: []string{"Enter"}},
	}
}

// ParseTools reads the setting, filling anything missing from the defaults
// so that a row written by an older panel still answers every tool.
func ParseTools(raw string) map[string]ToolProfile {
	out := DefaultTools()
	if raw == "" {
		return out
	}
	var stored map[string]ToolProfile
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return out
	}
	for tool, p := range stored {
		if !validKeys(p.Approve) || !validKeys(p.Deny) || !validKeys(p.Interrupt) || !validKeys(p.Submit) {
			continue
		}
		out[tool] = p
	}
	return out
}

// CheckProfile says what is wrong with a profile a person typed, or "" when
// nothing is. The settings route asks before storing one. ParseTools drops
// an invalid stored profile silently, because a stored row must never stop
// the bridge from answering; the route refuses out loud, because a person is
// there to be told.
//
// Stricter than what ParseTools accepts, on purpose. "Entr" is one token and
// no shell command, and send-keys types it as four letters into a
// permission dialog, where the first of them may well be a choice. So a
// typed key must be a name tmux knows or a single character, and the keys a
// phone relies on -- allow, deny, and the Enter after a pasted line -- may
// not be left empty for an agent.
func CheckProfile(tool string, p ToolProfile) string {
	for field, keys := range map[string][]string{"approve": p.Approve, "deny": p.Deny, "interrupt": p.Interrupt, "submit": p.Submit} {
		if !validKeys(keys) {
			return field + " has a key that is not a tmux key name"
		}
		for _, k := range keys {
			if !KnownKey(k) {
				return fmt.Sprintf("%s: tmux has no key called %q", field, k)
			}
		}
	}
	if len(p.Submit) == 0 {
		return "submit needs a key"
	}
	if tool != "shell" && (len(p.Approve) == 0 || len(p.Deny) == 0) {
		return "allow and deny need a key"
	}
	return ""
}

// namedKeys are tmux's key names (key-names in tmux(1)), in the case tmux
// prints them. tmux also reads them case-insensitively, and so does this.
var namedKeys = map[string]bool{
	"enter": true, "escape": true, "tab": true, "btab": true, "space": true, "bspace": true,
	"up": true, "down": true, "left": true, "right": true, "home": true, "end": true,
	"ic": true, "dc": true, "insert": true, "delete": true,
	"pageup": true, "pagedown": true, "ppage": true, "npage": true, "pgup": true, "pgdn": true,
}

// KnownKey reports whether tmux reads k as a key rather than as text: a
// named key or a single character, optionally behind C-, M- and S-
// modifiers.
func KnownKey(k string) bool {
	for {
		if len(k) > 2 && (strings.HasPrefix(k, "C-") || strings.HasPrefix(k, "M-") || strings.HasPrefix(k, "S-")) {
			k = k[2:]
			continue
		}
		break
	}
	if utf8.RuneCountInString(k) == 1 {
		return true
	}
	lower := strings.ToLower(k)
	if namedKeys[lower] {
		return true
	}
	if len(lower) >= 2 && lower[0] == 'f' {
		if n, err := strconv.Atoi(lower[1:]); err == nil && n >= 1 && n <= 12 {
			return true
		}
	}
	return false
}

// validKeys refuses anything that is not a tmux key name.
//
// The names go to send-keys, which types anything it does not recognise as
// text: "rm -rf /" is a valid argument. A key is one token with no space,
// which rules that out while allowing Enter, Escape, C-c, M-x, F1, and any
// single character.
func validKeys(keys []string) bool {
	for _, k := range keys {
		if k == "" || len(k) > 16 || strings.ContainsAny(k, " \t\n\r;") {
			return false
		}
	}
	return true
}

// AgentFor names the tool a session is running: the foreground process when
// it is an agent the panel knows, otherwise the command it was launched with,
// otherwise "shell".
//
// The foreground process first: a shell in which somebody typed `claude` is
// running Claude Code whatever its launch command says, and a Codex session
// that has dropped to a shell prompt is not going to answer a Codex prompt.
func AgentFor(launch []string, current string) string {
	if name := agentName(current); name != "" {
		return name
	}
	if len(launch) > 0 {
		if name := agentName(launch[0]); name != "" {
			return name
		}
	}
	return "shell"
}

func agentName(cmd string) string {
	switch filepath.Base(strings.TrimSpace(cmd)) {
	case "claude", "codex", "opencode", "kimi", "zcode":
		return filepath.Base(strings.TrimSpace(cmd))
	}
	return ""
}
