package chat

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/store"
)

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

// ─── the bridge's side ─────────────────────────────────────────────────────

// assist hands a sentence to the assistant and runs what it decided, with a
// confirmation before anything that writes.
func (b *Bridge) assist(ctx context.Context, ch *channel, p store.ChatPeer, text string, cands []Candidate, lang string) {
	key := chatKey(p.Channel, p.PeerID)
	reply := func(text string) { b.reply(ctx, ch, p, text) }
	b.mu.Lock()
	delete(b.clarify, key)
	b.mu.Unlock()
	if !b.withinBudget(ctx) {
		reply(msg(lang, "assistantBudget"))
		return
	}
	req := b.assistantRequest(ctx, p, text, cands, lang)
	if ch.caps.Typing {
		_ = ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, true)
		defer ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, false) //nolint:errcheck
	}
	intent, cost, err := b.assistant().Translate(ctx, req)
	b.spend(ctx, cost)
	if err != nil {
		reply(msg(lang, "assistantFailed", err.Error()))
		return
	}
	b.d.Audit(ctx, "chat.intent", fmt.Sprintf("%s: %s -> %s [%d] %s", key, preview(text), intent.Verb, intent.Handle, preview(intent.Text)))
	sessionFor := func() (string, bool) {
		for _, c := range cands {
			if c.Handle == intent.Handle {
				return c.SessionID, true
			}
		}
		reply(b.unknownHandle(ctx, intent.Handle, lang))
		return "", false
	}
	switch intent.Verb {
	case "send":
		id, ok := sessionFor()
		if !ok {
			return
		}
		if strings.TrimSpace(intent.Text) == "" {
			reply(stripMarkdown(firstNonEmpty(intent.Say, msg(lang, "none"))))
			return
		}
		// Named by the assistant, so for the gate it is as if the person
		// had typed the handle: the confirmation below shows which.
		target := Target{SessionID: id, How: HowAssistant}
		prefix := b.ask(key, intent.Text, func(ctx context.Context) said { return b.deliver(ctx, p, target, intent.Text, lang) }, lang)
		reply(prefix + msg(lang, "confirmSend", intent.Handle, intent.Text))
	case "approve", "deny":
		// A keystroke the model asked for is a write like any other: the
		// table it read has titles a pane can set, so it waits for ok. The
		// confirmation shows the request, and the ok answers that request
		// and no later one.
		id, ok := sessionFor()
		if !ok {
			return
		}
		row, h, err := b.sessionAndHandle(ctx, id)
		if err != nil {
			reply(msg(lang, "gone", h))
			return
		}
		cur, kind := b.current(ctx, row)
		if kind != store.MessagePrompt {
			reply(msg(lang, "notWaiting", h))
			return
		}
		approve := intent.Verb == "approve"
		word := pick(lang, map[bool]string{true: "允许", false: "拒绝"}[approve], map[bool]string{true: "allow", false: "deny"}[approve])
		prefix := b.ask(key, word+" "+preview(cur.Text), func(ctx context.Context) said {
			return b.answer(ctx, p, answerReq{sessionID: id, approve: approve, bound: cur.ID, how: HowAssistant}, lang)
		}, lang)
		b.tell(ctx, ch, p, showing(prefix+msg(lang, "confirmAnswer", h, request(cur.Text), word), id, cur.ID))
	case "stop":
		if _, ok := sessionFor(); !ok {
			return
		}
		// The typed command's path, confirmation included.
		b.command(ctx, ch, p, Inbound{}, Command{Verb: VerbStop, Handle: intent.Handle}, lang)
	case "screen", "shot", "open", "context", "mute", "unmute", "focus":
		b.command(ctx, ch, p, Inbound{}, Command{Verb: Verb(intent.Verb), Handle: intent.Handle, Arg: intent.Text}, lang)
	case "list", "usage":
		b.command(ctx, ch, p, Inbound{}, Command{Verb: Verb(intent.Verb)}, lang)
	case "ask":
		b.askAssistant(ctx, ch, p, firstNonEmpty(intent.Text, text), cands, lang)
	case "clarify":
		// The next "yes" or "the second one" is an answer to this.
		b.mu.Lock()
		b.clarify[key] = b.d.Now().Add(pendingTTL)
		b.mu.Unlock()
		reply(stripMarkdown(firstNonEmpty(intent.Say, msg(lang, "none"))))
	default:
		reply(stripMarkdown(firstNonEmpty(intent.Say, msg(lang, "none"))))
	}
}

func (b *Bridge) askAssistant(ctx context.Context, ch *channel, p store.ChatPeer, question string, cands []Candidate, lang string) {
	reply := func(text string) { b.reply(ctx, ch, p, text) }
	if !b.withinBudget(ctx) {
		reply(msg(lang, "assistantBudget"))
		return
	}
	if ch.caps.Typing {
		_ = ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, true)
		defer ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, false) //nolint:errcheck
	}
	ans, err := b.assistant().Ask(ctx, b.assistantRequest(ctx, p, question, cands, lang))
	b.spend(ctx, ans.CostUSD)
	if err != nil {
		reply(msg(lang, "assistantFailed", err.Error()))
		return
	}
	b.d.Audit(ctx, "chat.ask", fmt.Sprintf("%s: %s", chatKey(p.Channel, p.PeerID), preview(question)))
	reply(stripMarkdown(ans.Text))
}

// stripMarkdown takes the markup a model writes out of a reply that will be
// shown as plain text: on a phone "**[3]** is `waiting`" is asterisks and
// backticks, not emphasis.
func stripMarkdown(s string) string {
	s = mdFence.ReplaceAllString(s, "")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdBold.ReplaceAllString(s, "$1$2")
	s = mdCode.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1 $2")
	return strings.TrimSpace(s)
}

var (
	mdFence   = regexp.MustCompile("(?m)^```[a-zA-Z0-9]*\\s*$\\n?")
	mdHeading = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdCode    = regexp.MustCompile("`([^`\\n]+)`")
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
)

func (b *Bridge) assistantRequest(ctx context.Context, p store.ChatPeer, text string, cands []Candidate, lang string) AssistantRequest {
	req := AssistantRequest{Chat: chatKey(p.Channel, p.PeerID), Text: text, Lang: lang}
	for _, c := range cands {
		row, err := b.d.DB.GetSession(ctx, c.SessionID)
		if err != nil {
			continue
		}
		project := ""
		if pr, perr := b.d.DB.GetProject(ctx, row.ProjectID); perr == nil {
			project = pr.Name
		}
		kind := ""
		if last, ok, _ := b.d.DB.LatestSessionMessage(ctx, row.ID); ok {
			kind = CurrentKind(row, last)
		}
		req.Sessions = append(req.Sessions, SessionBrief{
			Handle: c.Handle, Title: row.Title, Project: project, State: string(row.State), Kind: kind,
			Tool: AgentFor(row.LaunchCommand, row.Command),
		})
	}
	sort.Slice(req.Sessions, func(i, j int) bool { return req.Sessions[i].Handle < req.Sessions[j].Handle })
	if recent, err := b.d.DB.RecentChatOutbound(ctx, p.Channel, p.PeerID, 10); err == nil {
		b.mu.Lock()
		for _, o := range recent {
			if o.Kind != store.OutboundStatus || o.SessionID == "" {
				continue
			}
			if h, ok := b.handles[o.SessionID]; ok {
				req.Recent = append(req.Recent, h)
			}
		}
		b.mu.Unlock()
	}
	return req
}

func (b *Bridge) withinBudget(ctx context.Context) bool {
	a := b.assistant()
	if a == nil {
		return false
	}
	cap := a.Budget()
	if cap <= 0 {
		return true
	}
	usd, _, err := b.d.DB.ChatSpend(ctx, b.d.Now().In(b.d.Zone()).Format("2006-01-02"))
	return err == nil && usd < cap
}

func (b *Bridge) spend(ctx context.Context, usd float64) {
	if usd <= 0 {
		usd = 0
	}
	_, _ = b.d.DB.AddChatSpend(ctx, b.d.Now().In(b.d.Zone()).Format("2006-01-02"), usd)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
