// Package hooks holds the reporter script agents run to report their state.
//
// State reporting is optional throughout: a session whose agent has no hook
// installed falls back to the output heuristic. That is a deliberate ordering
// of costs — a panel that only works after you have edited your agent's
// configuration is a panel most people never see working at all.
package hooks

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// ReportScript is the shell script installed into the data directory.
//
//go:embed report.sh
var ReportScript []byte

// InstallScript writes the reporter script and returns its path.
//
// Rewritten on every call rather than only when missing: after an upgrade the
// binary's copy is the truth, and a stale script on disk would keep reporting
// in an old shape with nothing to indicate why.
func InstallScript(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("hooks: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "vibepanel-report.sh")

	// Nothing to do when the file is already exactly this, which is almost
	// always. Callers treat this as "tell me where the script is": the settings
	// page asks on every poll, and the state snapshot asks in order to decide
	// whether hooks are installed — which means every state broadcast reached
	// here. Rewriting a file a few times a second is the smaller half of the
	// problem.
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, ReportScript) {
		if info, serr := os.Stat(path); serr == nil && info.Mode().Perm() == 0o700 {
			return path, nil
		}
		if cerr := os.Chmod(path, 0o700); cerr != nil {
			return "", fmt.Errorf("hooks: set mode on %s: %w", path, cerr)
		}
		return path, nil
	}

	// Write beside it and rename. The bigger half: this file is executed by
	// agents' hooks at moments nobody controls, and os.WriteFile truncates
	// before it writes. A shell reads a script incrementally, so overwriting
	// one mid-execution can fail in ways that are almost impossible to
	// attribute. A rename swaps the name; anything already running keeps the
	// inode it started with.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, ReportScript, 0o700); err != nil {
		return "", fmt.Errorf("hooks: write %s: %w", tmp, err)
	}
	// WriteFile only applies the mode when it creates the file, so a leftover
	// temp file would keep whatever it had.
	if err := os.Chmod(tmp, 0o700); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return "", fmt.Errorf("hooks: set mode on %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return "", fmt.Errorf("hooks: replace %s: %w", path, err)
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
// `notify` is a single command rather than a set of per-event hooks, so a
// Codex session can only ever report "waiting" through it. The second element
// is that literal state; Codex appends its own JSON argument after it, which
// report.sh ignores.
//
// This comment used to explain that as a limitation of Codex — "Codex has a
// single notify command rather than per-event hooks". That is no longer true
// and possibly never was. codex-cli 0.147 ships a hooks system: the binary
// carries `hooks/src/legacy_notify.rs`, a `Stop` event, `bypass_hook_trust`
// and a `--dangerously-bypass-hook-trust` flag. `notify` living in a file
// called *legacy* is the whole story.
//
// So `working` and `done` are reachable for Codex too, and the panel does not
// ask for them. Not changed here because the hooks schema is only known from
// strings in a binary, and confirming it needs a real Codex session. What is
// confirmed is that this setting still works: `codex doctor` on 0.147 with
// exactly this line reports `config.toml parse ok` and no deprecation.
//
// The reason to write it down is that the old comment told the next reader
// Codex could not do better, which is the kind of sentence that stops someone
// looking.
func CodexNotify(script string) string {
	return fmt.Sprintf(`notify = ["%s", "waiting"]`, script)
}

// SessionEnv is what a session needs in its environment for its agent's hooks
// to report state.
//
// Three of these are load-bearing and report.sh exits quietly without any of
// them: the session id says which session is talking, the token authenticates,
// the URL is where to post. The admin CLI used to inject the session id and
// the project id and neither of the other two — a session that looks
// configured, installs cleanly and reports nothing, because the script
// suppresses its own errors by design. The only symptom was state that stayed
// guessed, in a panel whose settings page said hooks were installed.
//
// VIBEPANEL_PROJECT_ID is not one of the three. Nothing in the panel reads it
// and neither does the script; it is there for whatever the person runs inside
// the session, which is a reasonable thing to offer and a bad thing to
// mistake for part of the mechanism. An earlier version of this comment
// claimed it made reports attributable after a session id was recycled, which
// was invention — the script does not send it.
//
// Here rather than in either caller because there were two callers and one of
// them was wrong.
func SessionEnv(sessionID, projectID, url, token string) []string {
	env := []string{
		"VIBEPANEL_SESSION_ID=" + sessionID,
		"VIBEPANEL_PROJECT_ID=" + projectID,
		"VIBEPANEL_URL=" + url,
	}
	// A panel that cannot read its own token still creates usable sessions;
	// they fall back to the output heuristic, which is the documented
	// behaviour when hooks are not in play.
	if token != "" {
		env = append(env, "VIBEPANEL_TOKEN="+token)
	}
	return env
}
