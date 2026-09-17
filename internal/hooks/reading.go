package hooks

import (
	"encoding/json"
)

// What a hook document changes about the state its hook reported.
//
// The state a hook reports is fixed by the event it is installed on: report.sh
// is handed "working", "waiting" or "done" in the agent's configuration, and
// that is all it knows. For most events that is the whole truth. For some of
// Claude Code's it is not, and the difference is written in the document the
// hook pipes along with it. Each rule below was measured against Claude Code
// 2.1.273 in a throwaway tmux, with every hook logging its document; the build
// log entry "Claude Code states, read from what the hook says" has the
// sequences.
//
// The rules match on event names and fields, not on which agent sent them.
// Codex, Kimi Code and zcode copied Claude Code's hook shape, so a rule applies
// to them exactly where their documents carry the same thing -- and the fields
// these rules read (notification_type, background_tasks, is_interrupt, source,
// agent_id) are ones only Claude Code sends today, so for the others the
// reported state passes through.
//
//   - Notification fires for fifteen reasons, and one hook entry reports all of
//     them as waiting. `idle_prompt` is Claude saying it has been idle for
//     sixty seconds, which it says a minute after every finished turn: on the
//     live panel 401 of 421 done-to-waiting transitions in a week came 59 to 62
//     seconds after the done. `auth_success`, `push_notification` and the rest
//     are not a person being needed either.
//
//   - Stop fires when the main thread's turn ends, including a turn that ended
//     by launching background agents. Its `background_tasks` lists them. Those
//     agents go on calling tools -- and reporting working through PreToolUse --
//     so the session read done, working, done, working, and every flip was a
//     push to a phone saying it had finished.
//
//   - SessionStart fires again after a compaction, in the middle of a turn.

// Reading is a hook report after its document has been read.
type Reading struct {
	// State is the state to record. Empty means the report changes nothing:
	// the event happened, and it says nothing about what the session is doing.
	State string

	// Agent is the subagent a report came from, empty for the main thread.
	Agent string

	// Answerable marks a prompt a keystroke answers. See
	// session.HookReport.Answerable.
	Answerable bool
}

// news lists the notification types that are not a person being needed: a
// sign-in, a push the agent chose to send, a quota resuming. Listed rather than
// taken as "everything but the prompts", because a type this list has never
// heard of -- a new one, or an agent that copied Claude Code's hook shape --
// should keep meaning what its Notification hook says, which is waiting.
var news = map[string]bool{
	"idle_prompt":                true,
	"auth_success":               true,
	"agent_completed":            true,
	"push_notification":          true,
	"computer_use_enter":         true,
	"computer_use_exit":          true,
	"quota_auto_resume_fired":    true,
	"quota_auto_resume_stale":    true,
	"quota_auto_resume_disabled": true,
	"model_refusal_fallback":     true,
}

// menus lists the notification types that announce a permission menu, which a
// single keystroke answers.
var menus = map[string]bool{
	"permission_prompt":        true,
	"worker_permission_prompt": true,
}

// agentWork lists the background task types that hold a finished turn at
// working. An allowlist: an agent always ends and reports back into the
// session, and a type this list does not know -- a teammate, a remote agent --
// might not, which would leave the session working forever.
//
// Shells and monitors are not on it, on purpose. A background shell is as often
// `npm run dev` as a build, and Claude's own footer reads "done · 1 shell still
// running" for the same situation.
var agentWork = map[string]bool{
	"subagent": true,
	"workflow": true,
}

// readingDocument is the part of a hook document the state is read from.
type readingDocument struct {
	Event            string  `json:"hook_event_name"`
	NotificationType *string `json:"notification_type"`
	Source           string  `json:"source"`
	AgentID          string  `json:"agent_id"`
	IsInterrupt      bool    `json:"is_interrupt"`
	ToolName         string  `json:"tool_name"`
	BackgroundTasks  []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	} `json:"background_tasks"`
}

// Read decides what a report means, given the state its hook was installed to
// report and the document it sent.
//
// A document that cannot be read leaves the report as it was: the state
// travels in the query string precisely so that a document the panel cannot
// parse costs the reading and never the state.
func Read(reported string, raw []byte) Reading {
	r := Reading{State: reported}
	if len(raw) > MaxPayload {
		return r
	}
	var doc readingDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return r
	}
	r.Agent = Clean(doc.AgentID, 128)

	switch doc.Event {
	case "Notification":
		// Absent is an agent or an older Claude that does not say why; the
		// hook's own state stands, as it did before this was read.
		if doc.NotificationType == nil {
			return r
		}
		switch kind := *doc.NotificationType; {
		case news[kind]:
			r.State = ""
		case menus[kind]:
			r.Answerable = true
		}
	case "PermissionRequest":
		// AskUserQuestion arrives as a permission request too, and it is not
		// a menu: a dialog of several questions takes a keystroke each, and
		// releasing on the first read working with the second still on screen.
		r.Answerable = doc.ToolName != "AskUserQuestion"
	case "Stop":
		if reported == "done" && agentsStillRunning(doc) {
			r.State = "working"
		}
	case "PostToolUseFailure":
		// Escape pressed while a tool was running, per the hook schema; not
		// captured live. The transcript reader covers it either way.
		if doc.IsInterrupt {
			r.State = "done"
		}
	case "SessionStart":
		// A compaction restarts the session record halfway through a turn,
		// and the turn goes on.
		if doc.Source == "compact" {
			r.State = ""
		}
	}
	return r
}

// agentsStillRunning reports whether a Stop left agent work in flight.
func agentsStillRunning(doc readingDocument) bool {
	for _, t := range doc.BackgroundTasks {
		if agentWork[t.Type] && (t.Status == "running" || t.Status == "pending") {
			return true
		}
	}
	return false
}
