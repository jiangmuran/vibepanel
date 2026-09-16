package chat

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// What happens when a person says something.
//
// The order is the security model, so it is worth stating: who is this (a
// paired peer, or a stranger who gets a pairing code and nothing else); what
// did they press or type (a button, a command, an answer, or words for an
// agent); which session (quote, handle, focus); and only then does anything
// reach a pane -- through a tool profile that decides which keys "allow" is,
// with a receipt back saying where the words went.

// pairingTTL is how long a stranger's code stays valid and how often the
// bridge repeats it to them.
const pairingTTL = 10 * time.Minute

// pendingTTL is how long "ok" has to arrive.
const pendingTTL = 2 * time.Minute

// contextLines is how many messages "context" shows without a count.
const contextLines = 10

func (b *Bridge) handle(ctx context.Context, ch *channel, in Inbound) {
	lang := b.language()
	key := chatKey(in.Channel, in.PeerID)
	peer, err := b.d.DB.GetChatPeer(ctx, in.Channel, in.PeerID)
	if errors.Is(err, store.ErrNotFound) {
		b.stranger(ctx, ch, in, lang)
		return
	}
	if err != nil {
		return
	}
	now := b.d.Now()
	peer.LastSeenAt = now.Unix()
	if in.PeerName != "" {
		peer.Display = in.PeerName
	}
	if in.ContextToken != "" {
		peer.ContextToken = in.ContextToken
	}
	_ = b.d.DB.PutChatPeer(ctx, peer)

	switch peer.Status {
	case store.PeerBlocked:
		return
	case store.PeerPending:
		b.mu.Lock()
		last := b.hello[key]
		b.hello[key] = now
		b.mu.Unlock()
		if now.Sub(last) > time.Minute {
			b.reply(ctx, ch, peer, msg(lang, "pairing", peer.PairingCode))
		}
		return
	}

	reply := func(text string) { b.reply(ctx, ch, peer, text) }
	if in.Action != nil {
		b.action(ctx, ch, peer, *in.Action, lang)
		return
	}
	if len(in.Image) > 0 {
		b.image(ctx, ch, peer, in, lang)
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return
	}
	b.d.Audit(ctx, "chat.in", fmt.Sprintf("%s: %s", key, preview(text)))

	if cmd, ok := Parse(text); ok {
		b.command(ctx, ch, peer, in, cmd, lang)
		return
	}

	// Words for an agent. Where they go is the one decision that can be
	// wrong in a way a person cannot see, which is why it comes back as a
	// receipt naming the handle.
	quoteSession := b.quoted(ctx, in)
	cands, err := b.candidates(ctx)
	if err != nil {
		return
	}
	target, rest, reason := Resolve(text, quoteSession, peer.FocusSession, cands)
	if reason != "" {
		if peer.Mode == store.ModeAdvanced && b.d.Assistant != nil {
			b.assist(ctx, ch, peer, text, cands, lang)
			return
		}
		reply(b.explain(ctx, reason, text, cands, lang))
		return
	}
	// A bare "y" to a quoted prompt is an answer, not text.
	if a, ok := Parse(rest); ok && (a.Verb == VerbApprove || a.Verb == VerbDeny) {
		reply(b.answer(ctx, target.SessionID, a.Verb == VerbApprove, lang))
		return
	}
	if peer.Mode == store.ModeAdvanced && b.d.Assistant != nil && target.How != "quote" && target.How != "handle" {
		// Focused delivery is convenient and also the case a sentence
		// meant for the assistant ("what is 3 doing") lands in a pane. In
		// advanced mode the assistant reads the sentence first and hands
		// back "send to N" when that is what it is.
		b.assist(ctx, ch, peer, text, cands, lang)
		return
	}
	reply(b.deliver(ctx, peer, target.SessionID, rest, lang))
}

// stranger answers somebody the panel does not know with a pairing code, and
// remembers them as pending so the code shows up on the settings page.
func (b *Bridge) stranger(ctx context.Context, ch *channel, in Inbound, lang string) {
	code, err := pairingCode()
	if err != nil {
		return
	}
	p := store.ChatPeer{
		Channel: in.Channel, PeerID: in.PeerID, Display: in.PeerName, Status: store.PeerPending,
		PairingCode: code, Mode: store.ModeNormal, ContextToken: in.ContextToken,
		CreatedAt: b.d.Now().Unix(), LastSeenAt: b.d.Now().Unix(),
	}
	if err := b.d.DB.PutChatPeer(ctx, p); err != nil {
		return
	}
	b.d.Audit(ctx, "chat.stranger", chatKey(in.Channel, in.PeerID))
	b.mu.Lock()
	b.hello[chatKey(in.Channel, in.PeerID)] = b.d.Now()
	b.mu.Unlock()
	b.reply(ctx, ch, p, msg(lang, "pairing", code))
}

func pairingCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// Pair accepts a pending peer by their code. Called from the settings page.
func (b *Bridge) Pair(ctx context.Context, code string) (store.ChatPeer, error) {
	code = strings.TrimSpace(code)
	peers, err := b.d.DB.ListChatPeers(ctx)
	if err != nil {
		return store.ChatPeer{}, err
	}
	for _, p := range peers {
		if p.Status != store.PeerPending || p.PairingCode == "" || p.PairingCode != code {
			continue
		}
		if b.d.Now().Unix()-p.LastSeenAt > int64(pairingTTL.Seconds()) {
			continue
		}
		p.Status = store.PeerPaired
		p.PairingCode = ""
		if err := b.d.DB.PutChatPeer(ctx, p); err != nil {
			return store.ChatPeer{}, err
		}
		b.d.Audit(ctx, "chat.paired", chatKey(p.Channel, p.PeerID))
		if ch, ok := b.channel(p.Channel); ok {
			b.reply(ctx, ch, p, msg(b.language(), "paired"))
		}
		return p, nil
	}
	return store.ChatPeer{}, store.ErrNotFound
}

// reply sends plain text to a peer, splitting for the adapter's limit.
func (b *Bridge) reply(ctx context.Context, ch *channel, p store.ChatPeer, text string) {
	if text == "" {
		return
	}
	for _, piece := range Split(text, ch.caps.MaxText) {
		ref, err := b.send(ctx, ch, p, Outbound{Text: piece})
		if err != nil {
			b.d.Log.Warn("chat reply", "channel", p.Channel, "err", err)
			return
		}
		_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
			Channel: p.Channel, PeerID: p.PeerID, Ref: ref, Kind: store.OutboundReply,
		})
	}
}

// quoted resolves what a reply's quoted message was about, by ref where the
// IM gives one and by the handle in its text otherwise.
func (b *Bridge) quoted(ctx context.Context, in Inbound) string {
	if in.QuotedRef != "" {
		if o, ok, _ := b.d.DB.ChatOutboundByRef(ctx, in.Channel, in.PeerID, in.QuotedRef); ok && o.SessionID != "" {
			return o.SessionID
		}
	}
	if in.QuotedText != "" {
		if h, ok := HandleInQuote(in.QuotedText); ok {
			if id, ok, _ := b.d.DB.SessionByHandle(ctx, h); ok {
				return id
			}
		}
	}
	return ""
}

// candidates is every session a chat may address, with a handle each.
func (b *Bridge) candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := b.d.DB.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range rows {
		if r.ArchivedAt != nil || r.Scratch || r.Exited {
			continue
		}
		h, err := b.handleOf(ctx, r.ID)
		if err != nil {
			continue
		}
		out = append(out, Candidate{SessionID: r.ID, Handle: h, Waiting: r.State == "waiting"})
	}
	return out, nil
}

// explain says why a bare message could not be delivered.
func (b *Bridge) explain(ctx context.Context, reason, text string, cands []Candidate, lang string) string {
	switch reason {
	case ReasonSeveral:
		return msg(lang, "several", b.list(ctx, lang, true))
	case ReasonUnknown:
		h, _, _ := SplitHandle(text)
		return msg(lang, "unknownHandle", h)
	}
	return msg(lang, "none")
}

// deliver pastes words into a session, or says why not.
func (b *Bridge) deliver(ctx context.Context, p store.ChatPeer, sessionID, text string, lang string) string {
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return msg(lang, "gone", h)
	}
	if row.State == "working" {
		return msg(lang, "working", h, h)
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	prof, ok := b.tools(ctx)[tool]
	if !ok {
		return msg(lang, "noProfile", h, tool)
	}
	if err := b.d.Term.Paste(ctx, row.TmuxName, text); err != nil {
		return msg(lang, "gone", h)
	}
	if len(prof.Submit) > 0 {
		_ = b.d.Term.Keys(ctx, row.TmuxName, prof.Submit...)
	}
	b.d.Audit(ctx, "chat.send", fmt.Sprintf("[%d] %s: %s", h, chatKey(p.Channel, p.PeerID), preview(text)))
	p.FocusSession = sessionID
	_ = b.d.DB.PutChatPeer(ctx, p)
	return msg(lang, "receipt", h)
}

// answer presses the tool's approve or deny keys, only while the session is
// waiting on a prompt.
func (b *Bridge) answer(ctx context.Context, sessionID string, approve bool, lang string) string {
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return msg(lang, "gone", h)
	}
	if row.State != "waiting" {
		return msg(lang, "notWaiting", h)
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	if tool == "shell" {
		return msg(lang, "noShell", h)
	}
	prof, ok := b.tools(ctx)[tool]
	keys := prof.Deny
	if approve {
		keys = prof.Approve
	}
	if !ok || len(keys) == 0 {
		return msg(lang, "noProfile", h, tool)
	}
	if err := b.d.Term.Keys(ctx, row.TmuxName, keys...); err != nil {
		return msg(lang, "gone", h)
	}
	what := "denied"
	if approve {
		what = "approved"
	}
	b.d.Audit(ctx, "chat."+what, fmt.Sprintf("[%d] %s", h, tool))
	return msg(lang, "receiptKey", h, msg(lang, what))
}

func (b *Bridge) interrupt(ctx context.Context, sessionID string, lang string) string {
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return msg(lang, "gone", h)
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	prof, ok := b.tools(ctx)[tool]
	if !ok || len(prof.Interrupt) == 0 {
		return msg(lang, "noProfile", h, tool)
	}
	if err := b.d.Term.Keys(ctx, row.TmuxName, prof.Interrupt...); err != nil {
		return msg(lang, "gone", h)
	}
	b.d.Audit(ctx, "chat.interrupt", fmt.Sprintf("[%d] %s", h, tool))
	return msg(lang, "receiptKey", h, msg(lang, "interrupted"))
}

func (b *Bridge) tools(ctx context.Context) map[string]ToolProfile {
	raw, _ := b.d.DB.GetSetting(ctx, ToolsKey, "")
	return ParseTools(raw)
}

func (b *Bridge) sessionAndHandle(ctx context.Context, sessionID string) (store.Session, int, error) {
	h, _ := b.handleOf(ctx, sessionID)
	row, err := b.d.DB.GetSession(ctx, sessionID)
	if err != nil || row.ArchivedAt != nil || row.Exited {
		return store.Session{}, h, errors.New("gone")
	}
	return row, h, nil
}

// action handles a button press.
func (b *Bridge) action(ctx context.Context, ch *channel, p store.ChatPeer, a Action, lang string) {
	verb, sessionID, _ := strings.Cut(a.Value, ":")
	var text string
	switch verb {
	case "approve", "deny":
		text = b.answer(ctx, sessionID, verb == "approve", lang)
	case "screen":
		text = b.screen(ctx, sessionID, lang)
	default:
		return
	}
	_ = ch.ad.Ack(ctx, a, firstLine(text))
	b.reply(ctx, ch, p, text)
}

// image saves a picture next to the panel and types its path into the
// session the picture was sent about, ready for the agent to read.
func (b *Bridge) image(ctx context.Context, ch *channel, p store.ChatPeer, in Inbound, lang string) {
	sessionID := b.quoted(ctx, in)
	if sessionID == "" {
		if h, _, ok := SplitHandle(in.Text); ok {
			sessionID, _, _ = b.d.DB.SessionByHandle(ctx, h)
		}
	}
	if sessionID == "" {
		cands, _ := b.candidates(ctx)
		t, _, reason := Resolve("", "", p.FocusSession, cands)
		if reason != "" {
			b.reply(ctx, ch, p, msg(lang, "imageNoTarget"))
			return
		}
		sessionID = t.SessionID
	}
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil || b.d.PastedDir == "" {
		b.reply(ctx, ch, p, msg(lang, "gone", h))
		return
	}
	if err := os.MkdirAll(b.d.PastedDir, 0o700); err != nil {
		return
	}
	ext := ".png"
	if len(in.Image) > 2 && in.Image[0] == 0xff && in.Image[1] == 0xd8 {
		ext = ".jpg"
	}
	path := filepath.Join(b.d.PastedDir, fmt.Sprintf("chat-%d%s", b.d.Now().UnixNano(), ext))
	if err := os.WriteFile(path, in.Image, 0o600); err != nil {
		return
	}
	// Typed, not submitted: the person adds their words and presses Enter,
	// the same way the panel's own upload works.
	if err := b.d.Term.Paste(ctx, row.TmuxName, path+" "); err != nil {
		b.reply(ctx, ch, p, msg(lang, "gone", h))
		return
	}
	b.d.Audit(ctx, "chat.image", fmt.Sprintf("[%d] %s", h, filepath.Base(path)))
	b.reply(ctx, ch, p, msg(lang, "imageSaved", h))
}

// command runs a parsed command.
func (b *Bridge) command(ctx context.Context, ch *channel, p store.ChatPeer, in Inbound, cmd Command, lang string) {
	key := chatKey(p.Channel, p.PeerID)
	reply := func(text string) { b.reply(ctx, ch, p, text) }

	// Verbs that need a session: the handle, the quote, or the focus rule.
	target := func(verb string) (string, bool) {
		if cmd.Handle > 0 {
			id, ok, _ := b.d.DB.SessionByHandle(ctx, cmd.Handle)
			if !ok {
				reply(msg(lang, "unknownHandle", cmd.Handle))
				return "", false
			}
			return id, true
		}
		if q := b.quoted(ctx, in); q != "" {
			return q, true
		}
		cands, _ := b.candidates(ctx)
		t, _, reason := Resolve("", "", p.FocusSession, cands)
		switch reason {
		case "":
			return t.SessionID, true
		case ReasonSeveral:
			// The same refusal a bare sentence gets, with the list: a "y"
			// with two sessions waiting is the case the rule exists for.
			reply(b.explain(ctx, reason, "", cands, lang))
		default:
			reply(msg(lang, "needHandle", verb))
		}
		return "", false
	}

	switch cmd.Verb {
	case VerbHelp:
		reply(msg(lang, "help"))
	case VerbList:
		reply(b.list(ctx, lang, false))
	case VerbApprove, VerbDeny:
		id, ok := target("y")
		if !ok {
			return
		}
		reply(b.answer(ctx, id, cmd.Verb == VerbApprove, lang))
	case VerbScreen:
		id, ok := target("screen")
		if !ok {
			return
		}
		b.replyCode(ctx, ch, p, b.screenHead(ctx, id, lang), b.screenText(ctx, id))
	case VerbShot:
		id, ok := target("shot")
		if !ok {
			return
		}
		if b.d.Shot == nil || !ch.caps.Images {
			reply(msg(lang, "shotUnavailable"))
			return
		}
		row, h, err := b.sessionAndHandle(ctx, id)
		if err != nil {
			reply(msg(lang, "gone", h))
			return
		}
		b.sendShot(ctx, ch, p, row, h)
	case VerbOpen:
		id, ok := target("open")
		if !ok {
			return
		}
		h, _ := b.handleOf(ctx, id)
		reply(msg(lang, "open", h, b.sessionURL(id)))
	case VerbMute:
		id, ok := target("mute")
		if !ok {
			return
		}
		d := parseDuration(cmd.Arg, 2*time.Hour)
		until := b.d.Now().Add(d)
		_ = b.d.DB.MuteChat(ctx, p.Channel, p.PeerID, id, until.Unix())
		h, _ := b.handleOf(ctx, id)
		reply(msg(lang, "muted", h, until.In(b.d.Zone()).Format("15:04"), h))
	case VerbUnmute:
		id, ok := target("unmute")
		if !ok {
			return
		}
		_ = b.d.DB.MuteChat(ctx, p.Channel, p.PeerID, id, 0)
		h, _ := b.handleOf(ctx, id)
		reply(msg(lang, "unmuted", h))
	case VerbFocus:
		id, ok := target("focus")
		if !ok {
			return
		}
		p.FocusSession = id
		_ = b.d.DB.PutChatPeer(ctx, p)
		h, _ := b.handleOf(ctx, id)
		reply(msg(lang, "focused", h))
	case VerbContext:
		id, ok := target("context")
		if !ok {
			return
		}
		n := contextLines
		if v, err := strconv.Atoi(cmd.Arg); err == nil && v > 0 {
			n = v
		}
		reply(b.context(ctx, id, n, lang))
	case VerbUsage:
		reply(b.usage(ctx, lang))
	case VerbMore:
		b.mu.Lock()
		rest := b.more[key]
		delete(b.more, key)
		b.mu.Unlock()
		if rest == "" {
			reply(msg(lang, "noMore"))
			return
		}
		head, more := cut(rest, BodyLimit)
		if more {
			tail := strings.TrimSpace(strings.TrimPrefix(rest, head))
			b.mu.Lock()
			b.more[key] = tail
			b.mu.Unlock()
			head += "\n" + msg(lang, "more", len([]rune(tail)))
		}
		reply(head)
	case VerbConfirm:
		b.mu.Lock()
		pd, ok := b.pend[key]
		delete(b.pend, key)
		b.mu.Unlock()
		if !ok {
			reply(msg(lang, "nothingPending"))
			return
		}
		if b.d.Now().After(pd.expires) {
			reply(msg(lang, "expired"))
			return
		}
		reply(pd.run(ctx))
	case VerbCancel:
		b.mu.Lock()
		_, ok := b.pend[key]
		delete(b.pend, key)
		b.mu.Unlock()
		if !ok {
			reply(msg(lang, "nothingPending"))
			return
		}
		reply(msg(lang, "cancelled"))
	case VerbStop:
		id, ok := target("stop")
		if !ok {
			return
		}
		h, _ := b.handleOf(ctx, id)
		b.ask(key, msg(lang, "confirmStop", h), func(ctx context.Context) string { return b.interrupt(ctx, id, lang) })
		reply(msg(lang, "confirmStop", h))
	case VerbAsk:
		if p.Mode != store.ModeAdvanced || b.d.Assistant == nil {
			reply(msg(lang, "noAssistant"))
			return
		}
		cands, _ := b.candidates(ctx)
		b.askAssistant(ctx, ch, p, cmd.Arg, cands, lang)
	}
}

// ask parks an action until "ok".
func (b *Bridge) ask(key, describe string, run func(ctx context.Context) string) {
	b.mu.Lock()
	b.pend[key] = pending{describe: describe, run: run, expires: b.d.Now().Add(pendingTTL)}
	b.mu.Unlock()
}

// list renders every session for a phone.
func (b *Bridge) list(ctx context.Context, lang string, waitingOnly bool) string {
	rows, err := b.d.DB.ListSessions(ctx)
	if err != nil {
		return ""
	}
	type line struct {
		weight int
		text   string
	}
	var lines []line
	for _, r := range rows {
		if r.ArchivedAt != nil || r.Scratch || r.Exited {
			continue
		}
		if waitingOnly && r.State != "waiting" {
			continue
		}
		h, err := b.handleOf(ctx, r.ID)
		if err != nil {
			continue
		}
		project := ""
		if p, perr := b.d.DB.GetProject(ctx, r.ProjectID); perr == nil {
			project = p.Name
		}
		kind := ""
		if last, ok, _ := b.d.DB.LatestSessionMessage(ctx, r.ID); ok && last.At >= r.StateChangedAt-1 {
			kind = last.Kind
		}
		since := ""
		if r.StateChangedAt > 0 {
			since = " · " + ago(b.d.Now().Sub(time.Unix(r.StateChangedAt, 0)), lang)
		}
		lines = append(lines, line{
			weight: r.State.SortWeight(),
			text:   fmt.Sprintf("%s [%d] %s · %s · %s%s", glyph(string(r.State)), h, r.Title, project, stateText(lang, string(r.State), kind), since),
		})
	}
	if len(lines) == 0 {
		return msg(lang, "listEmpty")
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].weight < lines[j].weight })
	if len(lines) > 40 {
		lines = lines[:40]
	}
	var out []string
	if !waitingOnly {
		out = append(out, msg(lang, "listHead"))
	}
	for _, l := range lines {
		out = append(out, l.text)
	}
	return strings.Join(out, "\n")
}

func (b *Bridge) screenHead(ctx context.Context, sessionID, lang string) string {
	h, _ := b.handleOf(ctx, sessionID)
	return msg(lang, "screenHead", h)
}

// screenText is the visible pane's last lines, trailing blank lines dropped.
func (b *Bridge) screenText(ctx context.Context, sessionID string) string {
	row, err := b.d.DB.GetSession(ctx, sessionID)
	if err != nil {
		return ""
	}
	out, err := b.d.Term.Screen(ctx, row.TmuxName, false)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n "), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return strings.Join(lines, "\n")
}

func (b *Bridge) screen(ctx context.Context, sessionID, lang string) string {
	return b.screenHead(ctx, sessionID, lang) + "\n" + b.screenText(ctx, sessionID)
}

func (b *Bridge) replyCode(ctx context.Context, ch *channel, p store.ChatPeer, head, code string) {
	ref, err := b.send(ctx, ch, p, Outbound{Text: head, Code: code})
	if err != nil {
		b.d.Log.Warn("chat reply", "channel", p.Channel, "err", err)
		return
	}
	_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{Channel: p.Channel, PeerID: p.PeerID, Ref: ref, Kind: store.OutboundReply})
}

// context renders the last n messages of a session, both sides.
func (b *Bridge) context(ctx context.Context, sessionID string, n int, lang string) string {
	h, _ := b.handleOf(ctx, sessionID)
	msgs, err := b.d.DB.ListSessionMessages(ctx, sessionID, n)
	if err != nil || len(msgs) == 0 {
		return msg(lang, "contextEmpty", h)
	}
	var out []string
	out = append(out, msg(lang, "contextHead", h, len(msgs)))
	for _, m := range msgs {
		who := "◇"
		switch m.Kind {
		case "user":
			who = "▷"
		case "prompt":
			who = "▲"
		case "question":
			who = "?"
		}
		text, _ := cut(m.Text, 400)
		out = append(out, fmt.Sprintf("%s %s", who, text))
	}
	return strings.Join(out, "\n\n")
}

// usage answers "usage" from what the panel counts.
func (b *Bridge) usage(ctx context.Context, lang string) string {
	loc := b.d.Zone()
	now := b.d.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	changed, waited := 0, 0
	if bk, err := b.d.DB.CountSessionEvents(ctx, dayStart, store.EventScope{}); err == nil {
		changed = bk.Started + bk.Finished + bk.Waited
		waited = bk.Waited
	}
	usd, calls, _ := b.d.DB.ChatSpend(ctx, now.Format("2006-01-02"))
	text := msg(lang, "usage", changed, waited, calls, usd)
	if b.d.Usage != nil {
		if extra := b.d.Usage(ctx, lang); extra != "" {
			text += "\n" + extra
		}
	}
	return text
}

// parseDuration reads "2h", "30m", "1d", "forever" and plain minutes.
func parseDuration(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return def
	case "forever", "永久", "一直":
		return 100 * 365 * 24 * time.Hour
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(s, "d"), "天")); err == nil && n > 0 && (strings.HasSuffix(s, "d") || strings.HasSuffix(s, "天")) {
		return time.Duration(n) * 24 * time.Hour
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "分钟")); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "小时")); err == nil && n > 0 && strings.HasSuffix(s, "小时") {
		return time.Duration(n) * time.Hour
	}
	return def
}

// preview is the first line of text, shortened, for the audit log.
func preview(s string) string {
	line := firstLine(s)
	head, _ := cut(line, 80)
	return head
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ─── advanced mode ─────────────────────────────────────────────────────────

// assist hands a sentence to the assistant and runs what it decided, with a
// confirmation before anything that writes.
func (b *Bridge) assist(ctx context.Context, ch *channel, p store.ChatPeer, text string, cands []Candidate, lang string) {
	key := chatKey(p.Channel, p.PeerID)
	reply := func(text string) { b.reply(ctx, ch, p, text) }
	if !b.withinBudget(ctx) {
		reply(msg(lang, "assistantBudget"))
		return
	}
	req := b.assistantRequest(ctx, p, text, cands, lang)
	if ch.caps.Typing {
		_ = ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, true)
		defer ch.ad.Typing(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, false) //nolint:errcheck
	}
	intent, cost, err := b.d.Assistant.Translate(ctx, req)
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
		reply(msg(lang, "unknownHandle", intent.Handle))
		return "", false
	}
	switch intent.Verb {
	case "send":
		id, ok := sessionFor()
		if !ok {
			return
		}
		if strings.TrimSpace(intent.Text) == "" {
			reply(msg(lang, "clarify", intent.Say))
			return
		}
		b.ask(key, intent.Text, func(ctx context.Context) string { return b.deliver(ctx, p, id, intent.Text, lang) })
		reply(msg(lang, "confirmSend", intent.Handle, intent.Text))
	case "approve", "deny":
		id, ok := sessionFor()
		if !ok {
			return
		}
		reply(b.answer(ctx, id, intent.Verb == "approve", lang))
	case "stop":
		id, ok := sessionFor()
		if !ok {
			return
		}
		b.ask(key, "stop", func(ctx context.Context) string { return b.interrupt(ctx, id, lang) })
		reply(msg(lang, "confirmStop", intent.Handle))
	case "screen", "shot", "open", "context", "mute", "unmute", "focus":
		b.command(ctx, ch, p, Inbound{}, Command{Verb: Verb(intent.Verb), Handle: intent.Handle, Arg: intent.Text}, lang)
	case "list", "usage":
		b.command(ctx, ch, p, Inbound{}, Command{Verb: Verb(intent.Verb)}, lang)
	case "ask":
		b.askAssistant(ctx, ch, p, firstNonEmpty(intent.Text, text), cands, lang)
	default:
		if intent.Say != "" {
			reply(msg(lang, "clarify", intent.Say))
		} else {
			reply(msg(lang, "none"))
		}
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
	ans, err := b.d.Assistant.Ask(ctx, b.assistantRequest(ctx, p, question, cands, lang))
	b.spend(ctx, ans.CostUSD)
	if err != nil {
		reply(msg(lang, "assistantFailed", err.Error()))
		return
	}
	b.d.Audit(ctx, "chat.ask", fmt.Sprintf("%s: %s", chatKey(p.Channel, p.PeerID), preview(question)))
	reply(ans.Text)
}

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
		if last, ok, _ := b.d.DB.LatestSessionMessage(ctx, row.ID); ok && last.At >= row.StateChangedAt-1 {
			kind = last.Kind
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
	cap := b.d.Assistant.Budget()
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
