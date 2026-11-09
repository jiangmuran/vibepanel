package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// agy (the Antigravity CLI) reads lifecycle hooks from hooks.json in a
// customization root; the machine-wide one is ~/.gemini/config/hooks.json. The
// file is a map of *named* hooks, each holding its events:
//
//	{ "<name>": { "enabled": true, "PreToolUse": [ {matcher, hooks:[...]} ],
//	              "Stop": [ {type, command} ] } }
//
// Two things make it the easiest of the JSON installers:
//
//   - Hooks are named, so the panel owns exactly one key and uninstall is a
//     delete of that key. There is no entry-by-entry merge into somebody's
//     groups, and nothing of the user's can be taken with it.
//   - The key is named after the marker, so "is this ours" is a question about a
//     name this panel chose and not a guess about a command line.
//
// Two shapes of event exist and agy is strict about neither being swapped: the
// tool events (PreToolUse, PostToolUse) are *grouped* -- a matcher and a
// "hooks" list -- while SessionStart, PreInvocation, PostInvocation and Stop
// are *flat*, handler objects directly in the list.
//
// agy has no permission-request event: PreToolUse is a gate that decides
// allow, deny or ask, and says nothing about a person being needed. So this
// reporter reports working and done and leaves "waiting" to the panel's own
// reading of the screen, which is what every session without a hook gets
// anyway.
const agyHookName = marker

type agyEvent struct {
	event, state string
	grouped      bool
}

var agyEvents = []agyEvent{
	{"SessionStart", "done", false},
	{"PreInvocation", "working", false},
	{"PreToolUse", "working", true},
	{"PostToolUse", "working", true},
	{"Stop", "done", false},
}

// AgyHooksPath is the machine-wide hooks file agy reads.
func AgyHooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "config", "hooks.json"), nil
}

func agyHandler(cmd string) map[string]any {
	return map[string]any{"type": "command", "command": cmd, "timeout": 5}
}

// agyEntry is one event's value in the shape agy wants for it.
func agyEntry(e agyEvent, script string) []any {
	h := agyHandler(command(script, e.state))
	if e.grouped {
		return []any{map[string]any{"matcher": "*", "hooks": []any{h}}}
	}
	return []any{h}
}

func agyBlock(script string) map[string]any {
	block := map[string]any{}
	for _, e := range agyEvents {
		block[e.event] = agyEntry(e, script)
	}
	return block
}

// AgyHooks renders the block for the settings page.
func AgyHooks(script string) string {
	b, err := json.MarshalIndent(map[string]any{agyHookName: agyBlock(script)}, "", "  ")
	if err != nil {
		return ""
	}
	return string(b) + "\n"
}

// agyHandlersIn lists the handler objects of one event, whichever shape it has.
func agyHandlersIn(list []any) []map[string]any {
	var out []map[string]any
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if inner, ok := m["hooks"].([]any); ok {
			for _, h := range inner {
				if hm, ok := h.(map[string]any); ok {
					out = append(out, hm)
				}
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

func agyEventIsOurs(list []any) bool {
	for _, h := range agyHandlersIn(list) {
		if cmd, ok := h["command"].(string); ok && containsMarker(cmd) {
			return true
		}
	}
	return false
}

// agyHookEvents lists the events of ours in the file, in agyEvents order. Never
// nil, for the same reason as kimiHookEvents.
func agyHookEvents(path string) []string {
	out := []string{}
	doc, err := readSettings(path)
	if err != nil {
		return out
	}
	block, _ := doc[agyHookName].(map[string]any)
	for _, e := range agyEvents {
		if list, _ := block[e.event].([]any); agyEventIsOurs(list) {
			out = append(out, e.event)
		}
	}
	return out
}

// InstallAgy writes the panel's named hook into ~/.gemini/config/hooks.json.
//
// Replaces the whole named block rather than merging into it: the key is the
// panel's, so a stale block from an older build is simply overwritten, and
// pressing install twice changes nothing and so writes and backs up nothing.
func InstallAgy(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := AgyHooksPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readSettings(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	before := encode(doc)
	doc[agyHookName] = agyBlock(scriptPath)
	if !unchanged(before, doc) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Status{}, fmt.Errorf("hooks: create %s: %w", filepath.Dir(path), err)
		}
		if err := backup(path); err != nil {
			return Status{}, err
		}
		if err := writeSettings(path, doc); err != nil {
			return Status{}, err
		}
	}
	return Inspect(scriptPath)
}

// UninstallAgy removes the panel's named hook and nothing else.
//
// A file that is not there is left alone, and so is a key of that name whose
// handlers do not call this script: somebody else's hook that happens to share
// a name is not ours to delete.
func UninstallAgy(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := AgyHooksPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readSettings(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Inspect(scriptPath)
	case err != nil:
		return Status{}, err
	}
	block, _ := doc[agyHookName].(map[string]any)
	ours := false
	for _, v := range block {
		if list, ok := v.([]any); ok && agyEventIsOurs(list) {
			ours = true
			break
		}
	}
	if !ours {
		return Inspect(scriptPath)
	}
	delete(doc, agyHookName)
	if err := backup(path); err != nil {
		return Status{}, err
	}
	if err := writeSettings(path, doc); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}
