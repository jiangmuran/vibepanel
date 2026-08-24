// Package hooks holds the reporter script agents run to report their state.
//
// State reporting is optional throughout: a session whose agent has no hook
// installed falls back to the output heuristic. That is a deliberate ordering
// of costs — a panel that only works after you have edited your agent's
// configuration is a panel most people never see working at all.
package hooks

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// ReportScript is the shell script installed into the data directory.
//
//go:embed report.sh
var ReportScript []byte

// Install writes the reporter script and returns its path.
//
// Rewritten on every call rather than only when missing: after an upgrade the
// binary's copy is the truth, and a stale script on disk would keep reporting
// in an old shape with nothing to indicate why.
func Install(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("hooks: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "vibepanel-report.sh")
	if err := os.WriteFile(path, ReportScript, 0o700); err != nil {
		return "", fmt.Errorf("hooks: write %s: %w", path, err)
	}
	return path, nil
}

// ClaudeSettings renders the hooks block for ~/.claude/settings.json.
//
// The four events map onto the three states the panel shows. Notification is
// the one that matters: it is what Claude Code emits when it has stopped and
// wants a person.
func ClaudeSettings(script string) string {
	entry := func(event, state string) string {
		return fmt.Sprintf(`    "%s": [
      {
        "hooks": [
          { "type": "command", "command": "%s %s" }
        ]
      }
    ]`, event, script, state)
	}
	return fmt.Sprintf(`{
  "hooks": {
%s,
%s,
%s,
%s
  }
}`,
		entry("Notification", "waiting"),
		entry("Stop", "done"),
		entry("UserPromptSubmit", "working"),
		entry("PreToolUse", "working"),
	)
}

// CodexNotify renders the notify setting for ~/.codex/config.toml.
//
// Codex has a single notify command rather than per-event hooks, so it can
// only say "something happened that wants you" — which is exactly the state
// worth knowing about.
func CodexNotify(script string) string {
	return fmt.Sprintf(`notify = ["%s", "waiting"]`, script)
}
