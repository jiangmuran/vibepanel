package hooks

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// NOT RUN. Written in a stretch where nothing could be executed, so it has
// neither compiled nor been checked against the drift it describes. Run it
// first; then rename session.StateWaiting's value and confirm it fails, because
// a test that has not been seen to fail is a decoration.
//
// What reading did establish, so the remaining risk is a typo rather than a
// design mistake: `events` is a map[string]string in this package;
// `ClaudeSettings` and `CodexNotify` both take a string and return one;
// `session.AllStates` is a []State over a string type, so string(s) converts;
// and there is no import cycle — internal/session imports only internal/tmux,
// which imports no internal package at all, so this file's dependency on
// session is safe to add from inside package hooks.
//
// Red line 3 names two things that mirror the state enum: the TypeScript
// constants in wire.ts and the SQL ordering in store/sessions.go. There is a
// third, and it is the one with no type system on either side of it.
//
// This package writes state strings into files that leave the repository: the
// reporter script, the `notify` line in ~/.codex/config.toml, and the hooks
// block merged into ~/.claude/settings.json. `internal/hooks` does not import
// `internal/session` at all — measured, zero references — so every one of those
// strings is a bare literal.
//
// Partly covered already, by accident rather than by design:
// TestTheReporterScriptActuallyReportsState in internal/httpapi posts a
// hard-coded "waiting" through the real script and waits for the state to
// change, so renaming the enum's *value* breaks that round trip. What it
// cannot see is the mapping this file checks — the script is handed a state
// directly there, so a typo in `events`, a snippet listing a state the map
// does not know, or a new enum member nothing reports, all pass.
//
// What drift looks like from the outside is the worst version of it. The server
// validates the state a hook posts (red line 6) and rejects an unknown one; the
// reporter script suppresses its own failures on purpose, because a hook that
// makes an agent wait is worse than a missed state update; and the settings page
// reports hooks as installed because it reads the agent's configuration file,
// not whether anything ever arrived. So a rename here produces: no error
// anywhere, a settings page saying it is fine, and every session quietly back on
// the heuristic. That is the same symptom as the bind-address trap in the
// runbook, from a different cause.
func TestEveryStateAHookReportsIsARealState(t *testing.T) {
	valid := map[string]bool{}
	for _, s := range session.AllStates {
		valid[string(s)] = true
	}
	// A guard that finds nothing passes. See the sweeper and the route walk.
	if len(valid) == 0 {
		t.Fatal("session.AllStates is empty, so this test is comparing nothing")
	}
	if len(events) == 0 {
		t.Fatal("the events map is empty, so this test is comparing nothing")
	}

	for event, state := range events {
		if !valid[state] {
			t.Errorf("the %s hook reports %q, which is not a state the server accepts (%v). "+
				"Nothing would say so at runtime: the server rejects it, the script swallows "+
				"the rejection, and the settings page still reports hooks as installed.",
				event, state, session.AllStates)
		}
	}

	// The snippet shown to the user and merged into their settings is a second
	// literal listing the same events, in hooks.go rather than install.go. The
	// map is what Inspect uses to decide whether hooks are installed, so a
	// snippet that installs an event the map does not know reads as "not
	// installed" forever after installing it.
	snippet := ClaudeSettings("/tmp/report.sh")
	for event, state := range events {
		if !strings.Contains(snippet, `"`+event+`"`) {
			t.Errorf("the events map knows %q and ClaudeSettings does not write it; "+
				"Inspect would look for a hook that installing never creates", event)
		}
		if !strings.Contains(snippet, state) {
			t.Errorf("ClaudeSettings never mentions %q, which the events map says %q reports",
				state, event)
		}
	}

	// Codex has one command for one event and can only ever report waiting.
	// Whatever that one is, it has to be a state the server accepts.
	codex := CodexNotify("/tmp/report.sh")
	found := false
	for state := range valid {
		if strings.Contains(codex, `"`+state+`"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the Codex notify line reports no state the server accepts: %s", codex)
	}
}

// The other direction: a snippet entry the events map does not know.
//
// There are two producers of one mapping. `ClaudeSettings` composes four
// hardcoded `entry(...)` calls for the settings page to show, and
// `InstallClaude` ignores it entirely and iterates the `events` map through
// `mergeEvent`. Nothing compared them -- `TestInstallIsIdempotent` asserts four
// events, but reads `events` on both sides, so it agrees with itself.
//
// A disagreement means the page shows one thing and the button writes another,
// against the one promise the install flow makes: you see the exact JSON that
// will be merged before you agree to it. The forward direction is covered above;
// this is the reverse, which is the one that lets the page advertise a hook
// nothing installs.
func TestTheSnippetPromisesNothingTheInstallerWillNotWrite(t *testing.T) {
	const script = "/tmp/report.sh"
	snippet := ClaudeSettings(script)

	// Every `"Event": [` in the snippet, which is the shape entry() produces.
	re := regexp.MustCompile(`"([A-Za-z]+)": \[`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(snippet, -1) {
		if m[1] == "hooks" {
			continue
		}
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatalf("no events parsed out of the snippet; the pattern has stopped matching and "+
			"this test is checking nothing:\n%s", snippet)
	}

	for event := range found {
		state, ok := events[event]
		if !ok {
			t.Errorf("the settings page advertises a %q hook and the events map has no such "+
				"event, so pressing install writes nothing for it", event)
			continue
		}
		// And the state it shows has to be the state that would be written.
		want := script + " " + state
		if !strings.Contains(snippet, `"command": "`+want+`"`) {
			t.Errorf("the snippet's %q entry does not carry %q; the page would show one state "+
				"and the installer write another", event, want)
		}
	}
	if len(found) != len(events) {
		t.Errorf("the snippet shows %d events and the map has %d", len(found), len(events))
	}
}
