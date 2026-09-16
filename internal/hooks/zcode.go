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
//     install flips it and writes a marker beside the config saying so; the
//     uninstall flips it back only if that marker is there and no hooks of
//     anybody's remain, so neither a user with their own events nor one whose
//     hooks live outside this file watches the panel switch them off.
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

// zcodeEnabledMarker is where the panel records that hooks.enabled was off
// until it turned it on.
//
// Beside the config, named like the backups the same package already leaves
// there, because the alternative is guessing on the way out. `enabled` belongs
// to the whole "hooks" object and gates hooks this file does not list, so the
// question the uninstall has to answer is not "are there events left" but "was
// it on before we arrived" -- and nothing in the file says. Without this, a
// zcode set up with workspace hooks and no user-level events had them switched
// off by a `hook remove` that had installed nothing.
func zcodeEnabledMarker(path string) string {
	return path + ".vibepanel-enabled"
}

// InstallZcode merges the panel's hook events into ~/.zcode/cli/config.json.
//
// Read-modify-write through the same readSettings/encode/writeSettings as the
// Claude and Codex installers rather than a second copy of them: `unchanged`
// compares canonical encodings, so pressing install twice writes nothing and
// leaves no second backup, which comparing raw bytes against a re-encode never
// does -- the file on disk ends with a newline and the encoding does not.
func InstallZcode(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := ZcodeConfigPath()
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

	hooks := zcodeHooksOf(doc)
	wasEnabled := hooks["enabled"] == true
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

	if !unchanged(before, doc) {
		if err := backup(path); err != nil {
			return Status{}, err
		}
		if err := writeSettings(path, doc); err != nil {
			return Status{}, err
		}
	}
	// After the write, so a failed install does not leave a note saying the
	// panel switched something on that it did not.
	//
	// A marker that cannot be written is not a failed install -- the hooks are
	// in the file and working. It costs the uninstall its licence to put
	// `enabled` back, which is the conservative half of that decision, and
	// returning an error here would instead show a 500 over an install that
	// took effect.
	if !wasEnabled {
		_ = os.WriteFile(zcodeEnabledMarker(path), nil, 0o600) //nolint:errcheck
	}
	return Inspect(scriptPath)
}

// UninstallZcode removes our groups, and puts enabled back only if the panel
// is the one that turned it on and nothing of anybody's is left.
//
// A config that is not there is left alone: this used to create one, because
// "not there" arrived as an empty document and the empty document was then
// written back with hooks.enabled=false in it. On a machine without zcode --
// which is most of them -- that write failed for want of a directory, and
// `vibepanel hook remove` reported a failure and then kept the reporter script
// because something had failed.
func UninstallZcode(scriptPath string) (Status, error) {
	editMu.Lock()
	defer editMu.Unlock()
	path, err := ZcodeConfigPath()
	if err != nil {
		return Status{}, err
	}
	// The marker says "the panel turned hooks.enabled on", and it is only true
	// while the panel's hooks are in that file. Both early returns below used
	// to leave it: zcode rewrites this config itself on login, so a file that
	// came back without a hooks object left a marker behind that said the
	// panel owned a flag it no longer had anything to do with -- and the next
	// removal, after the user had turned enabled on for their own workspace
	// hooks, turned it off again. Forgetting it is the safe direction; the
	// worst it costs is an `enabled: true` the panel declines to revert.
	marker := zcodeEnabledMarker(path)
	forget := func() error {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("hooks: forget %s: %w", marker, err)
		}
		return nil
	}

	doc, err := readSettings(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := forget(); err != nil {
			return Status{}, err
		}
		return Inspect(scriptPath)
	case err != nil:
		return Status{}, err
	}

	// Read-only on the way in: an "uninstall" that adds a "hooks" object to a
	// file that never had one has edited somebody's config to say the panel
	// was here.
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		if err := forget(); err != nil {
			return Status{}, err
		}
		return Inspect(scriptPath)
	}
	before := encode(doc)
	events, _ := hooks["events"].(map[string]any)
	for name, v := range events {
		groups, _ := v.([]any)
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			if !zcodeGroupIsOurs(g) {
				kept = append(kept, g)
			}
		}
		// Only an event this removal emptied. An event key that was already
		// empty is the user's -- `"SessionStart": []` is a thing zcode itself
		// writes -- and deleting it made "remove the hooks" rewrite the config
		// and leave a backup on a machine where the panel had installed
		// nothing.
		if len(kept) == len(groups) {
			continue
		}
		if len(kept) == 0 {
			delete(events, name)
		} else {
			events[name] = kept
		}
	}
	_, markerErr := os.Stat(marker)
	if markerErr == nil && !zcodeAnyEventsLeft(hooks) {
		hooks["enabled"] = false
	}

	if !unchanged(before, doc) {
		if err := backup(path); err != nil {
			return Status{}, err
		}
		if err := writeSettings(path, doc); err != nil {
			return Status{}, err
		}
	}
	if err := forget(); err != nil {
		return Status{}, err
	}
	return Inspect(scriptPath)
}
