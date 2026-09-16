// Package hooks holds the reporter script agents run to report their state.
//
// State reporting is optional throughout: a session whose agent has no hook
// installed falls back to the output heuristic. That is a deliberate ordering
// of costs — a panel that only works after you have edited your agent's
// configuration is a panel most people never see working at all.
package hooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// ReportToken derives the credential one session reports itself with: an HMAC
// of its own session id under the panel's root hook token.
//
// The root token used to go into every session's environment as-is, which made
// it a credential any process in any session held against every other session
// -- the endpoint it authenticates never bound the bearer to the session named
// in the body, so one compromised agent could reorder every other session. A
// session now holds MAC(root, its own id), which authorizes only itself; the
// root token stays accepted for sessions that outlived this change, because
// their environment was stamped at creation and only a restart can re-stamp it.
func ReportToken(root, sessionID string) string {
	mac := hmac.New(sha256.New, []byte(root))
	// The domain prefix keeps a value derived for one purpose from reading as
	// a value derived for another, should the root token ever mint a second
	// kind of per-object credential.
	mac.Write([]byte("vibepanel/report/v1:" + sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

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
//
// The command goes through the same command() the installer writes, rather than
// a %s of its own. Building it twice meant a data directory with a space in it
// showed an unquoted command here and got a quoted one written to the file, and
// this snippet's whole job is the promise that what you read is what gets
// merged.
func ClaudeSettings(script string) string {
	entry := func(event, state string) string {
		return fmt.Sprintf(`    "%s": [
      {
        "hooks": [
          { "type": "command", "command": "%s" }
        ]
      }
    ]`, event, command(script, state))
	}
	return fmt.Sprintf(`{
  "hooks": {
%s,
%s,
%s,
%s,
%s
  }
}`,
		entry("Notification", "waiting"),
		entry("PermissionRequest", "waiting"),
		entry("Stop", "done"),
		entry("UserPromptSubmit", "working"),
		entry("PreToolUse", "working"),
	)
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
