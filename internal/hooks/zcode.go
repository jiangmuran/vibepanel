package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// zcode (Z.AI's CLI) takes its hooks from the "hooks" object in
// ~/.zcode/cli/config.json, in Claude Code's shape: per event a list of
// matcher groups, each holding command entries. Two things make it different
// from the other installers:
//
//   - hooks.enabled defaults to false and nothing runs until it is true. The
//     install flips it; the uninstall flips it back only when no hooks of
//     anybody's remain, so a user who had their own hooks configured does not
//     watch the panel switch them off.
//   - The file is machine-managed — zcode rewrites it itself on login and
//     model changes — so it is edited as JSON rather than line-by-line. It is
//     still backed up first.
//
// User-level hooks run without a trust prompt (verified against zcode
// 3.11.2: UserPromptSubmit, PreToolUse and Stop all fired on first run). The
// trust machinery in the binary governs workspace hooks discovered from
// plugins, not this file.
var zcodeEvents = []struct{ event, state string }{
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"PermissionRequest", "waiting"},
	{"Stop", "done"},
}

// ZcodeConfigPath is the file zcode reads its hooks from.
func ZcodeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".zcode", "cli", "config.json"), nil
}

// ZcodeHooks renders the events block for the settings page.
func ZcodeHooks(script string) string {
	events := map[string]any{}
	for _, e := range zcodeEvents {
		events[e.event] = []any{zcodeGroup(command(script, e.state))}
	}
	b, err := json.MarshalIndent(map[string]any{"enabled": true, "events": events}, "", "  ")
	if err != nil {
		return ""
	}
	return `"hooks": ` + string(b) + "\n"
}

func zcodeGroup(cmd string) map[string]any {
	return map[string]any{
		"matcher": ".*",
		"hooks": []any{
			map[string]any{"type": "command", "command": cmd, "timeout": 5},
		},
	}
}

func zcodeGroupIsOurs(group any) bool {
	g, _ := group.(map[string]any)
	entries, _ := g["hooks"].([]any)
	for _, e := range entries {
		h, _ := e.(map[string]any)
		if cmd, ok := h["command"].(string); ok && containsMarker(cmd) {
			return true
		}
	}
	return false
}

// zcodeHooksOf returns the live "hooks" object, tolerating its absence.
func zcodeHooksOf(doc map[string]any) map[string]any {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	return hooks
}

// zcodeAnyEventsLeft reports whether any event still has groups, which is
// what decides whether enabled stays true on uninstall.
func zcodeAnyEventsLeft(hooks map[string]any) bool {
	events, _ := hooks["events"].(map[string]any)
	for _, v := range events {
		if groups, _ := v.([]any); len(groups) > 0 {
			return true
		}
	}
	return false
}

// zcodeHookEvents lists the events in the file whose groups are ours, in
// zcodeEvents order. Never nil, for the same reason as kimiHookEvents.
func zcodeHookEvents(path string) []string {
	out := []string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return out
	}
	hooks, _ := doc["hooks"].(map[string]any)
	events, _ := hooks["events"].(map[string]any)
	for _, e := range zcodeEvents {
		groups, _ := events[e.event].([]any)
		for _, g := range groups {
			if zcodeGroupIsOurs(g) {
				out = append(out, e.event)
				break
			}
		}
	}
	return out
}

func readZcodeConfig(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
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

// InstallZcode merges the panel's hook events into ~/.zcode/cli/config.json.
func InstallZcode(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := ZcodeConfigPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Status{}, fmt.Errorf("hooks: create %s: %w", filepath.Dir(path), err)
	}
	doc, err := readZcodeConfig(path)
	if err != nil {
		return Status{}, err
	}

	hooks := zcodeHooksOf(doc)
	hooks["enabled"] = true
	events, _ := hooks["events"].(map[string]any)
	if events == nil {
		events = map[string]any{}
		hooks["events"] = events
	}
	for _, e := range zcodeEvents {
		groups, _ := events[e.event].([]any)
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			if !zcodeGroupIsOurs(g) {
				kept = append(kept, g)
			}
		}
		events[e.event] = append(kept, zcodeGroup(command(scriptPath, e.state)))
	}

	before, _ := os.ReadFile(path)
	after, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Status{}, fmt.Errorf("hooks: encode %s: %w", path, err)
	}
	if string(before) == string(after) {
		return Inspect(scriptPath)
	}
	if err := backup(path); err != nil {
		return Status{}, err
	}
	if err := writeFileLike(path, append(after, '\n')); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}

// UninstallZcode removes our groups and reverts enabled only when nothing of
// anybody's is left.
func UninstallZcode(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := ZcodeConfigPath()
	if err != nil {
		return Status{}, err
	}
	doc, err := readZcodeConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Inspect(scriptPath)
		}
		return Status{}, err
	}

	hooks := zcodeHooksOf(doc)
	events, _ := hooks["events"].(map[string]any)
	for name, v := range events {
		groups, _ := v.([]any)
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			if !zcodeGroupIsOurs(g) {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			delete(events, name)
		} else {
			events[name] = kept
		}
	}
	if !zcodeAnyEventsLeft(hooks) {
		hooks["enabled"] = false
	}

	before, _ := os.ReadFile(path)
	after, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Status{}, fmt.Errorf("hooks: encode %s: %w", path, err)
	}
	if string(before) == string(after) {
		return Inspect(scriptPath)
	}
	if err := backup(path); err != nil {
		return Status{}, err
	}
	if err := writeFileLike(path, append(after, '\n')); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}
