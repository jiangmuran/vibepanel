package chat

import "context"

// The advanced mode's brain, as the bridge sees it.
//
// Two calls, deliberately unlike each other. Translate turns one sentence into
// one intent and is given nothing an agent wrote -- only the person's words
// and a table of handles, titles and states -- so that nothing on a screen
// can talk it into a write. Ask answers a question with read-only tools and
// returns prose, and has no way to reach a pane at all. The write path is
// Translate's intent going through the same executor a typed command does,
// with the same confirmation.
//
// The implementation lives in internal/chat/assistant, which shells out to
// a headless agent; this file is the contract so the bridge can be tested
// with a fake.

// AssistantRequest is one turn from one person.
type AssistantRequest struct {
	// Chat identifies the conversation, so the assistant can continue it.
	Chat string
	Text string
	Lang string
	// Sessions is the table the assistant may refer to, in handle order.
	Sessions []SessionBrief
	// Recent is what the bridge last said to this person, newest first, so
	// "the one before last" resolves to a handle.
	Recent []int
}

// SessionBrief is one row of the table Translate sees.
type SessionBrief struct {
	Handle  int    `json:"handle"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	// Kind is what it is waiting on, when it is waiting.
	Kind string `json:"kind,omitempty"`
	Tool string `json:"tool"`
}

// Intent is what Translate decided the person meant.
type Intent struct {
	// Verb is one of: send, approve, deny, stop, screen, shot, open, context,
	// list, mute, unmute, usage, ask, clarify, none.
	Verb string `json:"verb"`
	// Handle is which session, 0 when the verb takes none or it is unclear.
	Handle int `json:"handle"`
	// Text is what to send, for send; the question, for ask.
	Text string `json:"text"`
	// Say is a sentence for the person: the clarifying question, or a
	// refusal's reason.
	Say string `json:"say"`
}

// AssistantAnswer is Ask's reply.
type AssistantAnswer struct {
	Text string
	// CostUSD is what the call cost, for the daily budget.
	CostUSD float64
}

// Assistant is the brain. Nil means advanced mode is unavailable.
type Assistant interface {
	Translate(ctx context.Context, req AssistantRequest) (Intent, float64, error)
	Ask(ctx context.Context, req AssistantRequest) (AssistantAnswer, error)
	// Budget reports the daily cap in USD; zero means none.
	Budget() float64
}
