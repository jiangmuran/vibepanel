package chat

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
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

// pairingTTL is how long a stranger's code stays valid after they last
// spoke: long enough to walk to the panel, short enough that a code seen
// over a shoulder is not good tomorrow.
const pairingTTL = 10 * time.Minute

// helloEvery is how often a stranger or a pending person is answered with the
// code again. Once a minute: a person who sends three messages in a row gets
// the code once, and one who comes back later gets it again.
const helloEvery = time.Minute

// maxPending bounds how many strangers the panel remembers at once. Past it
// a new stranger is ignored rather than recorded: a flood of hellos must not
// fill the table or the audit log.
const maxPending = 50

// maxImageBytes bounds a picture a paired person sends, and maxImages how
// many are kept on disk; the oldest goes when the next arrives. A picture
// is for one agent to read once.
const (
	maxImageBytes = 8 << 20
	maxImages     = 50
)

// pendingTTL is how long "ok" has to arrive. Two minutes: a confirmation is
// answered as it is read, and one found an hour later is about a session
// that has moved on.
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
		b.d.Log.Warn("chat peer", "err", err)
		return
	}
	now := b.d.Now()
	switch peer.Status {
	case store.PeerBlocked:
		// Nothing is written for a blocked person, not even that they
		// spoke: a row they can keep changing is a row they still own.
		return
	case store.PeerPending:
		// A pending person's clock is not advanced by their own messages:
		// the code expires pairingTTL after the message that made it,
		// however much they keep talking.
		if isPairingCode(in.Text) && b.sayHello(key+"#code", now) {
			// They sent the code here, which is the most natural mistake
			// there is: say where it goes.
			where := b.d.PublicURL()
			if !reachable(where) {
				where = ""
			}
			b.reply(ctx, ch, peer, msg(lang, "pairingHere", where))
			return
		}
		if b.sayHello(key, now) {
			b.reply(ctx, ch, peer, msg(lang, "pairing", peer.PairingCode))
		}
		return
	}
	if err := b.d.DB.TouchChatPeer(ctx, peer.Channel, peer.PeerID, in.PeerName, in.ContextToken, now.Unix()); err != nil {
		b.d.Log.Warn("chat peer", "err", err)
	}
	if in.ContextToken != "" {
		peer.ContextToken = in.ContextToken
	}
	if in.PeerName != "" && peer.Display == "" {
		// Only a first name: one the owner gave stays, the way the stored
		// row keeps it (TouchChatPeer).
		peer.Display = in.PeerName
	}
	peer.LastSeenAt = now.Unix()

	// Recorded before anything can stop it, so a message the bridge held
	// back is in the log too.
	if in.Text != "" {
		b.d.Audit(ctx, "chat.in", fmt.Sprintf("%s (%s): %s", who(peer), key, preview(in.Text)))
	}
	_, isCommand := Parse(in.Text)
	_, _, addressed := SplitHandle(in.Text)
	if b.catchUp(ctx, ch, peer, in.Action != nil || isCommand || addressed, lang) {
		return
	}
	if in.Action != nil {
		b.action(ctx, ch, peer, *in.Action, lang)
		return
	}
	if in.FetchImage != nil {
		b.image(ctx, ch, peer, in, lang)
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return
	}
	if isFiller(text) {
		// "嗯", "哦哦": a voice note that caught a breath. Typed into a
		// session it becomes a task, so it never is. It is also the most
		// common yes on 微信, so while something asks for permission the
		// person is told it did not count; otherwise it is left unanswered.
		if s, ok := b.notYes(ctx, text, lang); ok {
			b.tell(ctx, ch, peer, s)
		}
		return
	}

	// Something waiting for "ok" reads "好的" as that, before any session
	// can read it as "allow": the person is answering the question the
	// bridge just asked, not one a session asked a while ago.
	if b.hasPending(key) {
		switch {
		case IsConfirm(text):
			b.command(ctx, ch, peer, in, Command{Verb: VerbConfirm}, lang)
			return
		case IsCancel(text):
			b.command(ctx, ch, peer, in, Command{Verb: VerbCancel}, lang)
			return
		}
	}
	advanced := peer.Mode == store.ModeAdvanced && b.assistant() != nil
	if cmd, ok := Parse(text); ok {
		// The assistant asked which one; "the second one" or "yes" is the
		// answer to it, not to a session.
		if advanced && cmd.Handle == 0 && (cmd.Verb == VerbApprove || cmd.Verb == VerbDeny) && b.clarifying(key) {
			cands, _ := b.candidates(ctx)
			b.assist(ctx, ch, peer, text, cands, lang)
			return
		}
		b.command(ctx, ch, peer, in, cmd, lang)
		return
	}

	// Words for an agent. Where they go is the one decision that can be
	// wrong in a way a person cannot see, which is why it comes back as a
	// receipt naming the handle.
	q := b.quoted(ctx, ch, in)
	cands, err := b.candidates(ctx)
	if err != nil {
		return
	}
	text = loosen(text, cands)
	target, rest, reason := Resolve(text, q.session, peer.FocusSession, cands, false)
	if reason != "" {
		if advanced {
			b.assist(ctx, ch, peer, text, cands, lang)
			return
		}
		b.tell(ctx, ch, peer, b.explain(ctx, reason, text, false, lang))
		return
	}
	// A bare "y" to a quoted prompt is an answer, not text.
	if a, ok := Parse(rest); ok && a.Handle == 0 && (a.Verb == VerbApprove || a.Verb == VerbDeny) {
		b.tell(ctx, ch, peer, b.answer(ctx, peer, answerReq{
			sessionID: target.SessionID, approve: a.Verb == VerbApprove, bound: q.bound, how: target.How, word: rest,
		}, lang))
		return
	}
	if advanced && target.How != HowQuote && target.How != HowHandle {
		// Focused delivery is convenient and also the case a sentence
		// meant for the assistant ("what is 3 doing") lands in a pane. In
		// advanced mode the assistant reads the sentence first and hands
		// back "send to N" when that is what it is.
		b.assist(ctx, ch, peer, text, cands, lang)
		return
	}
	b.tell(ctx, ch, peer, b.deliver(ctx, peer, target, rest, lang))
}

// loosen rewrites a handle written the way people write one on a phone into
// the form Resolve reads: "3 继续" and "3号 继续" and "第三个 继续" become
// "3: 继续". Only for a number that is a session right now; "2 files are
// enough" to a focused session with no [2] stays a sentence.
func loosen(text string, cands []Candidate) string {
	if _, _, ok := SplitHandle(text); ok {
		return text
	}
	known := func(n int) bool {
		for _, c := range cands {
			if c.Handle == n {
				return true
			}
		}
		return false
	}
	if n, rest, ok := SplitLooseHandle(text); ok && rest != "" && known(n) {
		return fmt.Sprintf("%d: %s", n, rest)
	}
	if m := spacedHandle.FindStringSubmatch(narrow(text)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && known(n) {
			return fmt.Sprintf("%d: %s", n, m[2])
		}
	}
	return text
}

// spacedHandle is "3 继续": a number, a space, and words that do not start
// with a digit ("3 4 5" is a list of numbers, not an address).
var spacedHandle = regexp.MustCompile(`^\s*(\d{1,4})\s+([^\d\s].*)$`)

func isPairingCode(text string) bool {
	return len(pairingDigits(text)) == 6 && len([]rune(strings.TrimSpace(narrow(text)))) <= 8
}

// pairingDigits keeps the digits of a code as a person typed it: full width
// from a Chinese keyboard, spaces or dashes from reading it aloud.
func pairingDigits(code string) string {
	var b strings.Builder
	for _, r := range narrow(code) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\t':
		default:
			return ""
		}
	}
	return b.String()
}

// sayHello reports whether a stranger or pending person is due the code
// again, and remembers that they were told. The map is pruned when it grows
// past what anything but a flood produces.
func (b *Bridge) sayHello(key string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.hello) > 500 {
		for k, at := range b.hello {
			if now.Sub(at) > time.Hour {
				delete(b.hello, k)
			}
		}
	}
	// Only a hello that is said resets the clock. Recording every message
	// meant a stranger writing every forty seconds was answered once, ever.
	if now.Sub(b.hello[key]) <= helloEvery {
		return false
	}
	b.hello[key] = now
	return true
}

func (b *Bridge) stranger(ctx context.Context, ch *channel, in Inbound, lang string) {
	now := b.d.Now()
	pending, err := b.d.DB.PrunePendingPeers(ctx, now.Add(-6*pairingTTL).Unix())
	if err != nil || pending >= maxPending {
		return
	}
	code, err := pairingCode()
	if err != nil {
		return
	}
	p := store.ChatPeer{
		Channel: in.Channel, PeerID: in.PeerID, Display: in.PeerName, Status: store.PeerPending,
		PairingCode: code, Mode: store.ModeNormal, ContextToken: in.ContextToken,
		CreatedAt: now.Unix(), LastSeenAt: now.Unix(),
	}
	if err := b.d.DB.PutChatPeer(ctx, p); err != nil {
		return
	}
	// Always answered: a row is only made when there was none, so this is a
	// new code, and a code nobody was told is a person stuck. That includes
	// someone just unblocked, whose last hello may be seconds old. The
	// flood bound is maxPending above.
	b.mu.Lock()
	b.hello[chatKey(in.Channel, in.PeerID)] = now
	b.mu.Unlock()
	b.d.Audit(ctx, "chat.stranger", fmt.Sprintf("%s (%s)", who(p), in.Channel))
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
// The code is read the way it was probably typed: full-width digits, spaces.
func (b *Bridge) Pair(ctx context.Context, code string) (store.ChatPeer, error) {
	code = pairingDigits(code)
	if len(code) != 6 {
		return store.ChatPeer{}, store.ErrNotFound
	}
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
		b.d.Audit(ctx, "chat.paired", fmt.Sprintf("%s (%s)", who(p), p.Channel))
		if ch, ok := b.channel(p.Channel); ok {
			b.reply(ctx, ch, p, msg(b.language(), "paired"))
		}
		return p, nil
	}
	return store.ChatPeer{}, store.ErrNotFound
}

// reply sends plain text to a peer, splitting for the adapter's limit.
func (b *Bridge) reply(ctx context.Context, ch *channel, p store.ChatPeer, text string) {
	b.tell(ctx, ch, p, plain(text))
}

// tell sends a reply and records what it showed. The first message a reply
// was split into carries the first request it showed; any further requests
// (a list with two prompts in it) are recorded under derived refs, which no
// quote or press can name, so they count as seen without becoming
// addresses.
func (b *Bridge) tell(ctx context.Context, ch *channel, p store.ChatPeer, s said) {
	if s.text == "" {
		return
	}
	var first string
	pieces := Split(s.text, ch.caps.MaxText)
	for _, piece := range pieces {
		out := Outbound{Text: piece}
		kind := store.OutboundReply
		// A reply that shows a permission request carries the same buttons
		// the card would have: "not done yet, here it is" with nothing to
		// press sends the person back to typing a yes they just typed.
		if s.ask != nil && ch.caps.Buttons && len(pieces) == 1 {
			out.Buttons = requestButtons(s.ask.session, s.ask.message, b.language())
			kind = store.OutboundRequest
		}
		ref, err := b.send(ctx, ch, p, out)
		if err != nil {
			b.d.Log.Warn("chat reply", "channel", p.Channel, "err", err)
			return
		}
		o := store.ChatOutbound{Channel: p.Channel, PeerID: p.PeerID, Ref: ref, Kind: kind}
		if first == "" {
			first = ref
			if len(s.shows) > 0 {
				o.SessionID, o.MessageID = s.shows[0].session, s.shows[0].message
			}
		}
		_ = b.d.DB.RecordChatOutbound(ctx, o)
	}
	for i, sh := range s.shows {
		if i == 0 {
			continue
		}
		_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
			Channel: p.Channel, PeerID: p.PeerID, Ref: fmt.Sprintf("%s#%d", first, i),
			Kind: store.OutboundReply, SessionID: sh.session, MessageID: sh.message,
		})
	}
}

// quote is what a quoted message said about where a reply goes.
type quote struct {
	// session is the session it was about, "" for none.
	session string
	// bound is which of its requests: the message id the quoted message
	// showed, 0 when it showed none in particular, and -1 when it showed
	// one that is not the session's current request.
	bound int64
	// unclear is a quote that was there and names no one session: a reply
	// the bridge sent about nothing, or a list with several handles in it.
	// Words may still find their session by the other rules; a yes may not,
	// because the person pointed at something and it was not that.
	unclear bool
}

func (b *Bridge) quoted(ctx context.Context, ch *channel, in Inbound) quote {
	if ch.caps.QuoteRefs && in.QuotedRef != "" {
		if o, ok, _ := b.d.DB.ChatOutboundByRef(ctx, in.Channel, in.PeerID, in.QuotedRef); ok && o.SessionID != "" {
			return quote{session: o.SessionID, bound: o.MessageID}
		}
		return quote{unclear: true}
	}
	if in.QuotedText == "" {
		return quote{}
	}
	h, ok := HandleInQuote(in.QuotedText)
	if !ok {
		// Not a card. A reply about exactly one session ("→ [3] 已拒绝",
		// "[3] 现在要你允许：…") is about that session, and read for its
		// request like a card is; anything else names nobody.
		hs := handlesIn(in.QuotedText)
		if len(hs) != 1 {
			return quote{unclear: true}
		}
		h = hs[0]
	}
	id, ok, _ := b.d.DB.SessionByHandle(ctx, h)
	if !ok {
		return quote{unclear: true}
	}
	// 微信 quotes by text, so which request a quoted card showed is read
	// back from the text: the card carries the request's words, and a card
	// whose words are not the current request's is an old card.
	row, err := b.d.DB.GetSession(ctx, id)
	if err != nil {
		return quote{session: id}
	}
	last, has, _ := b.d.DB.LatestSessionMessage(ctx, id)
	if !has {
		return quote{session: id}
	}
	kind := CurrentKind(row, last)
	if kind != store.MessagePrompt && kind != store.MessageQuestion {
		return quote{session: id, bound: -1}
	}
	if b.quotedRequest(ctx, id, in.QuotedText) == last.ID {
		return quote{session: id, bound: last.ID}
	}
	return quote{session: id, bound: -1}
}

// quotedRequest is which of a session's requests a quoted text shows: of the
// requests whose whole words are in the quote, the longest, and 0 for none.
//
// The longest, not any. "Does the quote contain the current request" is
// true of a card for rm -rf /home/zhou/projects/app/tmp when the current
// request is rm -rf /home/zhou, and narrow-then-broad is exactly how an
// agent escalates a delete: quoting the narrow card allowed the broad one.
// The card for the narrow request contains both, and the longer is the one it
// shows. Two requests with identical words read as the later, which is the
// same command either way.
func (b *Bridge) quotedRequest(ctx context.Context, sessionID, quoted string) int64 {
	msgs, err := b.d.DB.ListSessionMessages(ctx, sessionID, store.MessagesKeptPerSession)
	if err != nil {
		return 0
	}
	var best int64
	bestLen := 0
	for _, m := range msgs {
		if m.Kind != store.MessagePrompt && m.Kind != store.MessageQuestion {
			continue
		}
		if !quoteShows(quoted, m.Text) {
			continue
		}
		if n := len(squeezeSpace(m.Text)); n >= bestLen {
			best, bestLen = m.ID, n
		}
	}
	return best
}

func squeezeSpace(s string) string { return strings.Join(strings.Fields(s), "") }

// handlesIn is every distinct [n] in a text, in order.
func handlesIn(text string) []int {
	var out []int
	seen := map[int]bool{}
	for _, m := range anyHandle.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

var anyHandle = regexp.MustCompile(`\[\s*(\d{1,4})\s*\]`)

// quoteShows says whether quoted text carries all of a request's words, as
// the card showed them.
//
// All of them. The first version compared the first twenty-four characters,
// and Claude Code's commands start with "cd <the project> && ", so a card
// for clearing a cache and a card for deleting src read as the same card and
// quoting the first allowed the second. A client that shortens a long quote
// makes the card read as an old one, which shows the current request again:
// the safe way to be wrong.
func quoteShows(quoted, request string) bool {
	squeeze := func(s string) string {
		return strings.Join(strings.Fields(s), "")
	}
	shown, _ := cut(request, BodyLimit)
	req := squeeze(shown)
	if req == "" {
		return false
	}
	return strings.Contains(squeeze(quoted), req)
}

func (b *Bridge) candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := b.d.DB.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range rows {
		if !Addressable(r) {
			continue
		}
		h, err := b.Handle(ctx, r.ID)
		if err != nil {
			continue
		}
		_, kind := b.current(ctx, r)
		out = append(out, Candidate{SessionID: r.ID, Handle: h, Waiting: r.State == session.StateWaiting, Asking: kind == store.MessagePrompt})
	}
	return out, nil
}

// explain says why nothing was delivered. The list of waiting sessions shows
// what each asks, and counts as the person having seen it.
func (b *Bridge) explain(ctx context.Context, reason Reason, text string, answer bool, lang string) said {
	switch reason {
	case ReasonSeveral:
		l := b.list(ctx, lang, true)
		if answer {
			example := 3
			if cands, err := b.candidates(ctx); err == nil {
				for _, c := range cands {
					if c.Asking {
						example = c.Handle
						break
					}
				}
			}
			l.text = msg(lang, "severalAnswer", example, l.text)
			return l
		}
		l.text = msg(lang, "several", l.text)
		return l
	case ReasonUnknown:
		h, _, _ := SplitHandle(text)
		return plain(b.unknownHandle(ctx, h, lang))
	}
	return plain(msg(lang, "none"))
}

// unknownHandle tells a handle that never existed from one whose session
// has ended: "there is no [4]" about the session somebody was talking to an
// hour ago reads as the panel having lost it.
func (b *Bridge) unknownHandle(ctx context.Context, h int, lang string) string {
	if _, ok, _ := b.d.DB.SessionByHandle(ctx, h); ok {
		return msg(lang, "gone", h)
	}
	return msg(lang, "unknownHandle", h)
}

// current is the session message a waiting session is waiting on, if the
// panel knows it.
func (b *Bridge) current(ctx context.Context, row store.Session) (store.SessionMessage, string) {
	if row.State != session.StateWaiting {
		return store.SessionMessage{}, ""
	}
	last, ok, _ := b.d.DB.LatestSessionMessage(ctx, row.ID)
	if !ok {
		return store.SessionMessage{}, ""
	}
	kind := CurrentKind(row, last)
	if kind == "" {
		return store.SessionMessage{}, ""
	}
	return last, kind
}

func (b *Bridge) seen(ctx context.Context, p store.ChatPeer, messageID int64) bool {
	ok, _ := b.d.DB.ChatShown(ctx, p.Channel, p.PeerID, messageID)
	return ok
}

// request shortens a request's words for a reply that has to show them.
func request(text string) string {
	head, more := cut(strings.TrimSpace(text), 600)
	if more {
		head += " …"
	}
	return head
}

// gate is what stops words reaching a session that cannot take them: one
// that is working, and one at a permission prompt, where a paste followed
// by Enter is "allow". It is also what shows a person the question that a
// bare sentence would otherwise answer unseen.
func (b *Bridge) gate(ctx context.Context, p store.ChatPeer, row store.Session, h int, how How, lang string) (said, bool) {
	if row.State == session.StateWorking {
		return plain(msg(lang, "working", h, h)), false
	}
	cur, kind := b.current(ctx, row)
	switch kind {
	case store.MessagePrompt:
		// Claude Code's dialog ignores typed text and Enter picks the
		// highlighted choice, so pasting "wait, not that" and pressing
		// Enter would allow the very thing.
		return asking(msg(lang, "atPrompt", h, request(cur.Text), h, h), row.ID, cur.ID), false
	case store.MessageQuestion:
		// Only the session that happened to be the one waiting, and a
		// question this person never saw: a sentence typed for something
		// else would become the answer.
		if how == HowOnlyWaiting && !b.seen(ctx, p, cur.ID) {
			return showing(msg(lang, "showQuestion", h, request(cur.Text), h), row.ID, cur.ID), false
		}
	}
	return said{}, true
}

func (b *Bridge) deliver(ctx context.Context, p store.ChatPeer, target Target, text string, lang string) said {
	sessionID := target.SessionID
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return plain(msg(lang, "gone", h))
	}
	if s, ok := b.gate(ctx, p, row, h, target.How, lang); !ok {
		return s
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	prof, ok := b.tools(ctx)[tool]
	if !ok {
		return plain(msg(lang, "noProfile", h, tool))
	}
	if err := b.d.Term.Paste(ctx, row.TmuxName, text); err != nil {
		return plain(msg(lang, "gone", h))
	}
	if len(prof.Submit) > 0 {
		if err := b.d.Term.Keys(ctx, row.TmuxName, prof.Submit...); err != nil {
			// The words are in the pane and Enter was not: the receipt
			// must not say "sent" about a line that is waiting to be.
			return plain(msg(lang, "gone", h))
		}
	}
	b.d.Audit(ctx, "chat.send", fmt.Sprintf("[%d] %s · %s via %s: %s", h, row.Title, who(p), target.How, preview(text)))
	if target.How == HowFocus {
		// The one route a person did not name in the message itself, so the
		// receipt says so, and says who is still waiting while the words
		// went elsewhere.
		text := msg(lang, "receiptFocus", h)
		if others := b.waitingOthers(ctx, row.ID); others != "" {
			text += msg(lang, "stillWaiting", others)
		}
		return plain(text)
	}
	return plain(msg(lang, "receipt", h))
}

// askingOthers names the sessions other than one that ask for permission.
func (b *Bridge) askingOthers(ctx context.Context, except string) string {
	cands, err := b.candidates(ctx)
	if err != nil {
		return ""
	}
	var hs []string
	for _, c := range cands {
		if c.Asking && c.SessionID != except {
			hs = append(hs, fmt.Sprintf("[%d]", c.Handle))
		}
	}
	return strings.Join(hs, " ")
}

// notYes tells a person that a word they may have meant as a yes was not
// taken as one, naming the one request that is asking. ok is false when no
// one request is.
func (b *Bridge) notYes(ctx context.Context, word, lang string) (said, bool) {
	cands, err := b.candidates(ctx)
	if err != nil {
		return said{}, false
	}
	var one *Candidate
	for i := range cands {
		if cands[i].Asking {
			if one != nil {
				return said{}, false
			}
			one = &cands[i]
		}
	}
	if one == nil {
		return said{}, false
	}
	row, err := b.d.DB.GetSession(ctx, one.SessionID)
	if err != nil {
		return said{}, false
	}
	cur, _ := b.current(ctx, row)
	return asking(msg(lang, "notYes", word, one.Handle, request(cur.Text), one.Handle), row.ID, cur.ID), true
}

// waitingOthers names the sessions other than one that are waiting: "[1] [4]".
func (b *Bridge) waitingOthers(ctx context.Context, except string) string {
	cands, err := b.candidates(ctx)
	if err != nil {
		return ""
	}
	var hs []string
	for _, c := range cands {
		if c.Waiting && c.SessionID != except {
			hs = append(hs, fmt.Sprintf("[%d]", c.Handle))
		}
	}
	return strings.Join(hs, " ")
}

// who names a person for the audit log and for the others who were shown
// the same request.
func who(p store.ChatPeer) string {
	if p.Display != "" {
		return p.Display
	}
	// "owner@im.wechat" is an address, and the part before the @ is the
	// only piece of it a person recognises.
	if i := strings.IndexByte(p.PeerID, '@'); i > 0 {
		return p.PeerID[:i]
	}
	return p.PeerID
}

// answerReq is one "allow" or "deny". bound is the request it answers, as
// answerReq's caller learnt it: the message id on a pressed button or a
// quoted card, -1 for a quoted card about an older request, 0 when the
// message named no request (a bare "y", "3: y").
type answerReq struct {
	sessionID string
	approve   bool
	bound     int64
	how       How
	// word is what the person typed, for a session waiting on something
	// other than a permission prompt, where "y" is an answer in words.
	word string
}

// HowButton and HowAssistant are the two ways an answer arrives that are not
// a typed message's address.
const (
	HowButton    How = "button"
	HowAssistant How = "assistant"
)

// answer presses a tool's allow or deny keys, for exactly the request the
// person was looking at.
//
// A session asks, is answered, and asks again, and the keys are the same
// both times. So an answer is bound to a request: one that names an older
// request is refused with the current one shown, and one that names none
// goes through only if this person has been shown the current one. The
// person is never one keystroke from allowing a command they have not read.
func (b *Bridge) answer(ctx context.Context, p store.ChatPeer, req answerReq, lang string) said {
	row, h, err := b.sessionAndHandle(ctx, req.sessionID)
	if err != nil {
		return plain(msg(lang, "gone", h))
	}
	if row.State != session.StateWaiting {
		if req.bound != 0 {
			// Answered some other way, at the laptop most likely; the card
			// that was pressed or quoted should stop offering it. Whoever
			// is still asking is named, since the person was trying to
			// answer something.
			b.sweep(ctx, row)
			text := msg(lang, "staleGone", h)
			if others := b.askingOthers(ctx, row.ID); others != "" {
				text += "\n" + msg(lang, "othersAsking", others)
			}
			return plain(text)
		}
		return plain(msg(lang, "notWaiting", h))
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	if tool == "shell" {
		return plain(msg(lang, "noShell", h))
	}
	cur, kind := b.current(ctx, row)
	stale := req.bound < 0 || (req.bound > 0 && req.bound != cur.ID)
	switch kind {
	case store.MessagePrompt:
		if stale {
			b.sweep(ctx, row)
			return asking(msg(lang, "stalePrompt", h, request(cur.Text), h, h), row.ID, cur.ID)
		}
		// A deny the person addressed goes through unseen: refusing is the
		// safe direction, and making them read the thing before refusing it
		// only delays a no.
		explicitNo := !req.approve && req.how != HowOnlyWaiting && req.how != HowFocus
		if req.bound == 0 && !explicitNo && !b.seen(ctx, p, cur.ID) {
			text := msg(lang, "showPrompt", h, request(cur.Text), h, h)
			if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, row.ID, b.d.Now().Unix()); until > 0 {
				text += msg(lang, "mutedNote", h)
			}
			return asking(text, row.ID, cur.ID)
		}
	case store.MessageQuestion, store.MessageAssistant, store.MessageNotice, store.MessageUser:
		// Not a permission prompt: the keys would pick a menu's first
		// choice or submit an empty line. "y" here is an answer in words,
		// and deliver's gate shows a question the person has not seen.
		if stale {
			return showing(msg(lang, "showQuestion", h, request(cur.Text), h), row.ID, cur.ID)
		}
		if req.word == "" {
			return plain(msg(lang, "notWaiting", h))
		}
		return b.deliver(ctx, p, Target{SessionID: row.ID, How: req.how}, req.word, lang)
	default:
		// Waiting on something the hooks did not describe. A person who
		// named the session, pressed its button or quoted its card chose
		// it; a bare "y" must at least follow a card about this wait.
		if req.how == HowOnlyWaiting || req.how == HowFocus {
			if !b.toldSince(ctx, p, row) {
				return plain(msg(lang, "showUnknownWait", h, h, h))
			}
		}
	}
	prof, ok := b.tools(ctx)[tool]
	keys := prof.Deny
	if req.approve {
		keys = prof.Approve
	}
	if !ok || len(keys) == 0 {
		return plain(msg(lang, "noProfile", h, tool))
	}
	if err := b.d.Term.Keys(ctx, row.TmuxName, keys...); err != nil {
		return plain(msg(lang, "gone", h))
	}
	what := "denied"
	if req.approve {
		what = "approved"
	}
	b.d.Audit(ctx, "chat."+what, fmt.Sprintf("[%d] %s · %s · %s via %s: %s", h, row.Title, tool, who(p), req.how, preview(cur.Text)))
	if cur.ID > 0 {
		b.close(ctx, row, h, cur.ID, cur.Text, msg(lang, "answeredBy", msg(lang, what), who(p)), &p,
			msg(lang, "answeredElsewhere", h, who(p), pick(lang, map[string]string{"approved": "允许", "denied": "拒绝"}[what], msg(lang, what)), preview(cur.Text)))
		return plain(msg(lang, "receiptKeyCmd", h, msg(lang, what), preview(cur.Text)))
	}
	return plain(msg(lang, "receiptKey", h, msg(lang, what)))
}

// toldSince says whether this person was sent anything about a session since
// it last changed state.
func (b *Bridge) toldSince(ctx context.Context, p store.ChatPeer, row store.Session) bool {
	recent, err := b.d.DB.RecentChatOutbound(ctx, p.Channel, p.PeerID, 50)
	if err != nil {
		return false
	}
	for _, o := range recent {
		if o.SessionID == row.ID && o.At >= row.StateChangedAt-messageSlack {
			return true
		}
	}
	return false
}

// close takes a request off every message that showed it. Where the IM can
// edit, the buttons go and the card says what happened (stateText); where it
// cannot, the others who were shown it by a card get a line, if there is one,
// so nobody answers a request that is already over. by is who answered,
// nil when nobody in a chat did.
func (b *Bridge) close(ctx context.Context, row store.Session, h int, messageID int64, text, stateText string, by *store.ChatPeer, line string) {
	outs, err := b.d.DB.ChatOutboundsForMessage(ctx, row.ID, messageID)
	if err != nil {
		return
	}
	told := map[string]bool{}
	if by != nil {
		told[chatKey(by.Channel, by.PeerID)] = true
	}
	for _, o := range outs {
		b.closeOne(ctx, row, h, o, text, stateText)
		ch, ok := b.channel(o.Channel)
		if !ok || ch.caps.Edit || line == "" || o.Kind != store.OutboundStatus {
			continue
		}
		p, err := b.d.DB.GetChatPeer(ctx, o.Channel, o.PeerID)
		k := chatKey(o.Channel, o.PeerID)
		if err != nil || p.Status != store.PeerPaired || told[k] {
			continue
		}
		told[k] = true
		b.reply(ctx, ch, p, line)
	}
}

// closeOne edits one message's buttons away, if it has any.
func (b *Bridge) closeOne(ctx context.Context, row store.Session, h int, o store.ChatOutbound, text, stateText string) {
	if o.Kind != store.OutboundRequest {
		return
	}
	_ = b.d.DB.SetChatOutboundKind(ctx, o.Channel, o.PeerID, o.Ref, store.OutboundAnswered)
	ch, ok := b.channel(o.Channel)
	if !ok || !ch.caps.Edit {
		return
	}
	p, err := b.d.DB.GetChatPeer(ctx, o.Channel, o.PeerID)
	if err != nil {
		return
	}
	project := ""
	if pr, perr := b.d.DB.GetProject(ctx, row.ProjectID); perr == nil {
		project = pr.Name
	}
	lang := b.language()
	body, _ := cut(text, BodyLimit)
	card := &Card{
		Handle: h, Title: row.Title, Project: project, State: string(row.State),
		Glyph: glyph(string(row.State)), StateText: stateText,
		Body: body, URL: b.sessionURL(row.ID), LinkLabel: msg(lang, "linkLabel"),
	}
	if err := ch.ad.Edit(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, o.Ref, Outbound{Card: card}); err != nil {
		b.d.Log.Warn("chat close", "channel", o.Channel, "err", err)
	}
}

// sweep closes a session's requests that are over without a chat having
// answered them: allowed at the laptop, or replaced by the next one. A card
// that keeps its buttons after that invites exactly the stale press the
// binding exists to refuse, and makes it impossible to tell, scrolling back,
// what is still open.
func (b *Bridge) sweep(ctx context.Context, row store.Session) {
	open, err := b.d.DB.OpenChatRequests(ctx, row.ID)
	if err != nil || len(open) == 0 {
		return
	}
	var current int64
	if cur, kind := b.current(ctx, row); kind == store.MessagePrompt {
		current = cur.ID
	}
	h, _ := b.Handle(ctx, row.ID)
	lang := b.language()
	texts := map[int64]string{}
	if msgs, err := b.d.DB.ListSessionMessages(ctx, row.ID, store.MessagesKeptPerSession); err == nil {
		for _, m := range msgs {
			texts[m.ID] = m.Text
		}
	}
	for _, o := range open {
		if o.MessageID == current {
			continue
		}
		// The command stays on the card: "handled" alone left no way to
		// tell, scrolling back, what it was that had been handled.
		b.closeOne(ctx, row, h, o, texts[o.MessageID], msg(lang, "handled"))
	}
}

func (b *Bridge) interrupt(ctx context.Context, sessionID string, lang string) said {
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return plain(msg(lang, "gone", h))
	}
	// Confirmed up to two minutes after it was asked, by which time the
	// session may have stopped at a prompt, where the interrupt key is the
	// deny key. Say so rather than deny something in passing.
	if cur, kind := b.current(ctx, row); kind == store.MessagePrompt {
		return asking(msg(lang, "stopAtPrompt", h, request(cur.Text), h), row.ID, cur.ID)
	}
	if row.State != session.StateWorking {
		return plain(msg(lang, "notWorking", h))
	}
	tool := AgentFor(row.LaunchCommand, row.Command)
	prof, ok := b.tools(ctx)[tool]
	if !ok || len(prof.Interrupt) == 0 {
		return plain(msg(lang, "noProfile", h, tool))
	}
	if err := b.d.Term.Keys(ctx, row.TmuxName, prof.Interrupt...); err != nil {
		return plain(msg(lang, "gone", h))
	}
	b.d.Audit(ctx, "chat.interrupt", fmt.Sprintf("[%d] %s · %s", h, row.Title, tool))
	return plain(msg(lang, "receiptKey", h, msg(lang, "interrupted")))
}

func (b *Bridge) tools(ctx context.Context) map[string]ToolProfile {
	raw, _ := b.d.DB.GetSetting(ctx, ToolsKey, "")
	return ParseTools(raw)
}

func (b *Bridge) sessionAndHandle(ctx context.Context, sessionID string) (store.Session, int, error) {
	// The row first, the handle second: a handle is only assigned to a
	// session that exists, so an id a client made up numbers nothing.
	row, err := b.d.DB.GetSession(ctx, sessionID)
	if err != nil || !Addressable(row) {
		h, _ := b.handleIfAny(sessionID)
		return store.Session{}, h, errGone
	}
	h, err := b.Handle(ctx, sessionID)
	if err != nil {
		return store.Session{}, 0, err
	}
	return row, h, nil
}

// errGone is a session that cannot be reached: deleted, exited, or a scratch
// terminal. The handle in the reply is whatever was assigned before, so the
// person recognises which one is being talked about.
var errGone = errors.New("chat: session gone")

func (b *Bridge) handleIfAny(sessionID string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.handles[sessionID]
	return h, ok
}

// action is a button press: "verb:session:message".
func (b *Bridge) action(ctx context.Context, ch *channel, p store.ChatPeer, a Action, lang string) {
	parts := strings.SplitN(a.Value, ":", 3)
	if len(parts) < 2 {
		return
	}
	verb, sessionID := parts[0], parts[1]
	var bound int64
	if len(parts) == 3 {
		bound, _ = strconv.ParseInt(parts[2], 10, 64)
	}
	// The value came back from the IM's servers, which is not this person.
	// When the press names the message it was on, that message must be one
	// the bridge sent this person about this session, and the request it
	// answers is the one that message showed, not the one the value claims.
	// An IM without message refs on presses still gets the value's bound
	// and the session-must-be-waiting check in answer().
	if a.MessageRef != "" {
		o, ok, _ := b.d.DB.ChatOutboundByRef(ctx, p.Channel, p.PeerID, a.MessageRef)
		if !ok || o.SessionID != sessionID {
			b.d.Audit(ctx, "chat.refused", fmt.Sprintf("%s: press %q on a message that is not theirs", chatKey(p.Channel, p.PeerID), a.Value))
			return
		}
		bound = o.MessageID
	}
	var out said
	switch verb {
	case "approve", "deny":
		if bound == 0 {
			// A button always names its request; one without is from a
			// card sent before requests were named, and is old.
			bound = -1
		}
		out = b.answer(ctx, p, answerReq{sessionID: sessionID, approve: verb == "approve", bound: bound, how: HowButton}, lang)
	case "screen":
		out = plain(b.screen(ctx, sessionID, lang))
	default:
		return
	}
	if err := ch.ad.Ack(ctx, a, firstLine(out.text)); err != nil {
		b.d.Log.Warn("chat ack", "channel", p.Channel, "err", err)
	}
	// A press that did what it said is already visible twice: the IM's own
	// acknowledgement and the card, edited to say who answered. A third
	// message saying the same is noise; anything else still needs saying.
	if (verb == "approve" || verb == "deny") && ch.caps.Edit && strings.HasPrefix(out.text, "→") {
		return
	}
	b.tell(ctx, ch, p, out)
}

func (b *Bridge) image(ctx context.Context, ch *channel, p store.ChatPeer, in Inbound, lang string) {
	caption := strings.TrimSpace(in.Text)
	how := HowQuote
	sessionID := b.quoted(ctx, ch, in).session
	cands, _ := b.candidates(ctx)
	if sessionID == "" {
		caption = loosen(caption, cands)
		if h, rest, ok := SplitHandle(caption); ok {
			sessionID, _, _ = b.d.DB.SessionByHandle(ctx, h)
			caption, how = rest, HowHandle
		}
	}
	if sessionID == "" {
		t, _, reason := Resolve("", "", p.FocusSession, cands, false)
		if reason != "" {
			b.reply(ctx, ch, p, msg(lang, "imageNoTarget"))
			return
		}
		sessionID, how = t.SessionID, t.How
	}
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil || b.d.PastedDir == "" {
		b.reply(ctx, ch, p, msg(lang, "gone", h))
		return
	}
	// A path typed into a permission dialog is keystrokes into it, so the
	// picture waits on the same checks words do, and before it is fetched.
	if s, ok := b.gate(ctx, p, row, h, how, lang); !ok {
		b.tell(ctx, ch, p, s)
		return
	}
	// Fetched only now: the person is paired and the picture has a
	// session to go to.
	img, err := in.FetchImage(ctx)
	if err != nil || len(img) == 0 || len(img) > maxImageBytes {
		b.reply(ctx, ch, p, msg(lang, "imageFailed"))
		return
	}
	if err := os.MkdirAll(b.d.PastedDir, 0o700); err != nil {
		return
	}
	pruneImages(b.d.PastedDir)
	ext := ".png"
	if len(img) > 2 && img[0] == 0xff && img[1] == 0xd8 {
		ext = ".jpg"
	}
	path := filepath.Join(b.d.PastedDir, fmt.Sprintf("chat-%d%s", b.d.Now().UnixNano(), ext))
	if err := os.WriteFile(path, img, 0o600); err != nil {
		return
	}
	b.d.Audit(ctx, "chat.image", fmt.Sprintf("[%d] %s", h, filepath.Base(path)))
	if caption != "" {
		// A picture with words is one message to the agent, sent the way
		// words are.
		s := b.deliver(ctx, p, Target{SessionID: sessionID, How: how}, path+" "+caption, lang)
		if strings.HasPrefix(s.text, "→") {
			s = plain(msg(lang, "imageSent", h))
		}
		b.tell(ctx, ch, p, s)
		return
	}
	// Typed, not submitted: the person's next words go in after it and
	// Enter sends both, the same way the panel's own upload works.
	if err := b.d.Term.Paste(ctx, row.TmuxName, path+" "); err != nil {
		b.reply(ctx, ch, p, msg(lang, "gone", h))
		return
	}
	b.reply(ctx, ch, p, msg(lang, "imageSaved", h))
}

// pruneImages keeps the newest maxImages-1 chat pictures, so the directory
// holds a bounded number of them however many are sent.
func pruneImages(dir string) {
	names, err := filepath.Glob(filepath.Join(dir, "chat-*"))
	if err != nil || len(names) < maxImages {
		return
	}
	sort.Strings(names) // chat-<unixnano>: lexical order is time order
	for _, n := range names[:len(names)-maxImages+1] {
		_ = os.Remove(n)
	}
}

// catchUp answers a person's first message after pushes failed to reach them
// with the requests they missed, and reports whether it did. Their message
// itself is not run: it was written without having seen these.
//
// held says whether the message was one that would have done something, so
// the header says it was held back only when it was.
func (b *Bridge) catchUp(ctx context.Context, ch *channel, p store.ChatPeer, held bool, lang string) bool {
	key := chatKey(p.Channel, p.PeerID)
	b.mu.Lock()
	ids := b.missed[key]
	delete(b.missed, key)
	other := b.missedOther[key]
	delete(b.missedOther, key)
	b.mu.Unlock()
	if len(ids) == 0 {
		return false
	}
	type item struct {
		row    store.Session
		c      Change
		last   store.SessionMessage
		handle int
	}
	var items []item
	for id := range ids {
		row, c, last, ok := b.change(ctx, id)
		if !ok || !Addressable(row) || row.State != session.StateWaiting {
			continue
		}
		h, err := b.Handle(ctx, id)
		if err != nil {
			continue
		}
		items = append(items, item{row, c, last, h})
	}
	if len(items) == 0 {
		return false
	}
	sort.Slice(items, func(i, j int) bool { return items[i].handle < items[j].handle })
	head := msg(lang, "missed")
	if held {
		head = msg(lang, "missedHeld")
	}
	if other > 0 {
		head = msg(lang, "otherMissed", other) + "\n" + head
	}
	b.reply(ctx, ch, p, head)
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	routes := ParseRoutes(raw)
	for _, it := range items {
		project := ""
		if pr, err := b.d.DB.GetProject(ctx, it.row.ProjectID); err == nil {
			project = pr.Name
		}
		d := routes.Decide(it.c, b.d.Now().In(b.d.Zone()))
		card, rest := b.cardFor(it.row, it.c, it.last, it.handle, project, d.Body, lang)
		_ = b.sendCard(ctx, ch, p, it.row, it.c, it.last, card, rest, lang)
	}
	return true
}

func (b *Bridge) hasPending(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	pd, ok := b.pend[key]
	return ok && !b.d.Now().After(pd.expires)
}

func (b *Bridge) clarifying(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.clarify[key]
	return ok && b.d.Now().Before(until)
}

func (b *Bridge) command(ctx context.Context, ch *channel, p store.ChatPeer, in Inbound, cmd Command, lang string) {
	key := chatKey(p.Channel, p.PeerID)
	reply := func(text string) { b.reply(ctx, ch, p, text) }

	// Verbs that need a session: the handle, the quote, or the focus rule.
	// Also how it was found and which request a quote named.
	answerVerb := cmd.Verb == VerbApprove || cmd.Verb == VerbDeny
	target := func(verb string) (string, How, int64, bool) {
		if cmd.Handle > 0 {
			id, ok, _ := b.d.DB.SessionByHandle(ctx, cmd.Handle)
			if !ok {
				reply(msg(lang, "unknownHandle", cmd.Handle))
				return "", "", 0, false
			}
			return id, HowHandle, 0, true
		}
		q := b.quoted(ctx, ch, in)
		if q.session != "" {
			return q.session, HowQuote, q.bound, true
		}
		cands, _ := b.candidates(ctx)
		if q.unclear && answerVerb {
			// The person pointed at a message; falling back to "the one
			// waiting" would answer something they did not point at.
			example := 3
			for _, c := range cands {
				if c.Waiting {
					example = c.Handle
					break
				}
			}
			reply(msg(lang, "unclearQuote", example))
			return "", "", 0, false
		}
		t, _, reason := Resolve("", "", p.FocusSession, cands, answerVerb)
		switch {
		case reason == "":
			return t.SessionID, t.How, 0, true
		case reason == ReasonSeveral:
			// The same refusal a bare sentence gets, with the list: a "y"
			// with two sessions waiting is the case the rule exists for.
			b.tell(ctx, ch, p, b.explain(ctx, reason, "", answerVerb, lang))
		case answerVerb:
			reply(msg(lang, "nothingWaiting"))
		default:
			reply(msg(lang, "needHandle", pick(lang, verbWord(verb), verb)))
		}
		return "", "", 0, false
	}

	switch cmd.Verb {
	case VerbHelp:
		reply(b.help(ctx, p, lang))
	case VerbList:
		b.tell(ctx, ch, p, b.list(ctx, lang, false, p))
	case VerbUnknown:
		reply(msg(lang, "unknownCommand", cmd.Arg))
	case VerbUnclear:
		word, example, _ := strings.Cut(cmd.Arg, "\x00")
		reply(msg(lang, "unclearCommand", word, example))
	case VerbAll:
		reply(msg(lang, "refuseAll", b.list(ctx, lang, true).text))
	case VerbApprove, VerbDeny:
		id, how, bound, ok := target("y")
		if !ok {
			return
		}
		// The word as typed goes along: to a session asking a question
		// rather than for permission, "y" is the answer in words.
		word := strings.TrimSpace(in.Text)
		if n, rest, ok := SplitLooseHandle(word); ok && n == cmd.Handle {
			word = rest
		}
		b.tell(ctx, ch, p, b.answer(ctx, p, answerReq{sessionID: id, approve: cmd.Verb == VerbApprove, bound: bound, how: how, word: word}, lang))
	case VerbScreen:
		id, _, _, ok := target("screen")
		if !ok {
			return
		}
		if _, h, err := b.sessionAndHandle(ctx, id); err != nil {
			reply(msg(lang, "gone", h))
			return
		}
		b.replyCode(ctx, ch, p, b.screenHead(ctx, id, lang), b.screenText(ctx, id))
	case VerbShot:
		id, _, _, ok := target("shot")
		if !ok {
			return
		}
		if b.shooter() == nil || !ch.caps.Images {
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
		id, _, _, ok := target("open")
		if !ok {
			return
		}
		h, _ := b.Handle(ctx, id)
		u := b.sessionURL(id)
		if u == "" {
			reply(msg(lang, "noPublicURL"))
			return
		}
		reply(msg(lang, "open", h, u))
	case VerbMute:
		id, _, _, ok := target("mute")
		if !ok {
			return
		}
		d, understood := parseDuration(cmd.Arg, 2*time.Hour)
		until := b.d.Now().Add(d)
		_ = b.d.DB.MuteChat(ctx, p.Channel, p.PeerID, id, until.Unix())
		h, _ := b.Handle(ctx, id)
		text := msg(lang, "muted", h, until.In(b.d.Zone()).Format("01-02 15:04"), h)
		if !understood {
			text = msg(lang, "mutedDefault", cmd.Arg) + "\n" + text
		}
		reply(text)
	case VerbUnmute:
		id, _, _, ok := target("unmute")
		if !ok {
			return
		}
		_ = b.d.DB.MuteChat(ctx, p.Channel, p.PeerID, id, 0)
		h, _ := b.Handle(ctx, id)
		reply(msg(lang, "unmuted", h))
	case VerbFocus:
		id, _, _, ok := target("focus")
		if !ok {
			return
		}
		if _, h, err := b.sessionAndHandle(ctx, id); err != nil {
			reply(msg(lang, "gone", h))
			return
		}
		if err := b.d.DB.SetChatPeerFocus(ctx, p.Channel, p.PeerID, id); err != nil {
			b.d.Log.Warn("chat focus", "err", err)
		}
		h, _ := b.Handle(ctx, id)
		reply(msg(lang, "focused", h))
	case VerbUnfocus:
		if err := b.d.DB.SetChatPeerFocus(ctx, p.Channel, p.PeerID, ""); err != nil {
			b.d.Log.Warn("chat focus", "err", err)
		}
		reply(msg(lang, "unfocused"))
	case VerbContext:
		id, _, _, ok := target("context")
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
		handle := cmd.Handle
		if handle == 0 {
			// Quoting the long message is saying which one.
			if q := b.quoted(ctx, ch, in); q.session != "" {
				handle, _ = b.Handle(ctx, q.session)
			}
		}
		reply(b.continueMore(ctx, key, handle, lang))
	case VerbConfirm:
		b.mu.Lock()
		pd, ok := b.pend[key]
		delete(b.pend, key)
		b.mu.Unlock()
		if !ok {
			// "OK" with nothing to confirm is the most common yes there is,
			// and a session may well be asking for one. It goes the way any
			// other yes goes, shown-first rule and all -- unless it follows a
			// stop; see Bridge.stopped.
			b.mu.Lock()
			afterStop := b.d.Now().Before(b.stopped[key].Add(pendingTTL))
			b.mu.Unlock()
			if afterStop {
				// Said out loud, and for the whole window: an ok that got
				// "nothing to confirm" and a second identical ok that
				// allowed the command was the same word meaning two things
				// ten seconds apart.
				if s, ok := b.notYes(ctx, strings.TrimSpace(in.Text), lang); ok {
					b.tell(ctx, ch, p, s)
					return
				}
				reply(msg(lang, "nothingPending"))
				return
			}
			b.command(ctx, ch, p, in, Command{Verb: VerbApprove}, lang)
			return
		}
		if b.d.Now().After(pd.expires) {
			reply(msg(lang, "expired"))
			return
		}
		b.tell(ctx, ch, p, pd.run(ctx))
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
		b.mu.Lock()
		b.stopped[key] = b.d.Now()
		b.mu.Unlock()
		id, _, _, ok := target("stop")
		if !ok {
			return
		}
		row, h, err := b.sessionAndHandle(ctx, id)
		if err != nil {
			reply(msg(lang, "gone", h))
			return
		}
		// Asked before the confirmation, not only after: "stop 3" about a
		// session that has already stopped would otherwise cost a round
		// trip to learn nothing.
		if row.State != session.StateWorking {
			b.tell(ctx, ch, p, b.interrupt(ctx, id, lang))
			return
		}
		prefix := b.ask(key, msg(lang, "describeStop", h), func(ctx context.Context) said { return b.interrupt(ctx, id, lang) }, lang)
		reply(prefix + msg(lang, "confirmStop", h))
	case VerbAsk:
		if p.Mode != store.ModeAdvanced || b.assistant() == nil {
			reply(msg(lang, "noAssistant"))
			return
		}
		cands, _ := b.candidates(ctx)
		b.askAssistant(ctx, ch, p, cmd.Arg, cands, lang)
	}
}

// verbWord is the Chinese command for an English verb, for the one reply
// that has to name the command back.
func verbWord(verb string) string {
	switch verb {
	case "y":
		return "好"
	case "screen":
		return "屏幕"
	case "shot":
		return "截图"
	case "open":
		return "打开"
	case "mute":
		return "静音"
	case "unmute":
		return "取消静音"
	case "focus":
		return "切到"
	case "context":
		return "上下文"
	case "stop":
		return "停"
	}
	return verb
}

// help is the command list with a handle that exists, so the example can be
// copied as it stands.
func (b *Bridge) help(ctx context.Context, p store.ChatPeer, lang string) string {
	example := 3
	if cands, err := b.candidates(ctx); err == nil && len(cands) > 0 {
		example = cands[0].Handle
		for _, c := range cands {
			if c.Handle < example {
				example = c.Handle
			}
		}
	}
	text := msg(lang, "help", example)
	if p.Mode == store.ModeAdvanced && b.assistant() != nil {
		text += "\n" + msg(lang, "helpAdvanced")
	}
	return text
}

// continueMore sends the next part of a long message: the session named, or
// the one whose message was cut last.
func (b *Bridge) continueMore(ctx context.Context, key string, handle int, lang string) string {
	b.mu.Lock()
	sessionID := b.moreLast[key]
	b.mu.Unlock()
	if handle > 0 {
		id, ok, _ := b.d.DB.SessionByHandle(ctx, handle)
		if !ok {
			return msg(lang, "unknownHandle", handle)
		}
		sessionID = id
	}
	b.mu.Lock()
	rest := b.more[key+"|"+sessionID]
	b.mu.Unlock()
	if sessionID == "" || rest == "" {
		return msg(lang, "noMore")
	}
	h, _ := b.Handle(ctx, sessionID)
	head, more := cut(rest, BodyLimit)
	tail := ""
	if more {
		tail = strings.TrimSpace(strings.TrimPrefix(rest, head))
	}
	b.park(key, sessionID, tail)
	text := msg(lang, "moreHead", h) + "\n" + head + "\n"
	if tail != "" {
		return text + msg(lang, "more", len([]rune(tail)), h)
	}
	return text + msg(lang, "moreEnd")
}

// ask parks an action until "ok", and returns a line saying which earlier
// one it replaced, if any: one person has one confirmation at a time, and a
// second silently taking the first one's place is how "ok" runs the wrong
// thing.
func (b *Bridge) ask(key, describe string, run func(ctx context.Context) said, lang string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	prefix := ""
	if old, ok := b.pend[key]; ok && !b.d.Now().After(old.expires) {
		prefix = msg(lang, "replaced", old.describe) + "\n"
	}
	b.pend[key] = pending{describe: describe, run: run, expires: b.d.Now().Add(pendingTTL)}
	return prefix
}

// list renders every session for a phone. A waiting session shows what it is
// asking, and that counts as the person having seen it.
func (b *Bridge) list(ctx context.Context, lang string, waitingOnly bool, viewer ...store.ChatPeer) said {
	rows, err := b.d.DB.ListSessions(ctx)
	if err != nil {
		return said{}
	}
	type line struct {
		weight int
		text   string
		shows  *shown
	}
	var lines []line
	now := b.d.Now().Unix()
	for _, r := range rows {
		if !Addressable(r) {
			continue
		}
		if waitingOnly && r.State != session.StateWaiting {
			continue
		}
		h, err := b.Handle(ctx, r.ID)
		if err != nil {
			continue
		}
		project := ""
		if p, perr := b.d.DB.GetProject(ctx, r.ProjectID); perr == nil {
			project = p.Name
		}
		cur, kind := b.current(ctx, r)
		since := ""
		if r.StateChangedAt > 0 {
			since = " · " + ago(b.d.Now().Sub(time.Unix(r.StateChangedAt, 0)), lang)
		}
		muted := ""
		if len(viewer) > 0 {
			if until, _ := b.d.DB.ChatMutedUntil(ctx, viewer[0].Channel, viewer[0].PeerID, r.ID, now); until > 0 {
				muted = " · " + msg(lang, "listMuted")
			}
			if viewer[0].FocusSession == r.ID {
				muted += " · " + msg(lang, "listFocus")
			}
		}
		l := line{
			weight: r.State.SortWeight(),
			text:   fmt.Sprintf("%s [%d] %s · %s · %s%s%s", glyph(string(r.State)), h, r.Title, project, stateText(lang, string(r.State), kind), since, muted),
		}
		if kind == store.MessagePrompt || kind == store.MessageQuestion {
			words, more := cut(strings.Join(strings.Fields(cur.Text), " "), 120)
			if more {
				words += " …"
			}
			l.text += "\n    " + words
			l.shows = &shown{session: r.ID, message: cur.ID}
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return plain(msg(lang, "listEmpty"))
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].weight < lines[j].weight })
	if len(lines) > 40 {
		lines = lines[:40]
	}
	var out said
	var texts []string
	if !waitingOnly {
		texts = append(texts, msg(lang, "listHead"))
	}
	for _, l := range lines {
		texts = append(texts, l.text)
		if l.shows != nil {
			out.shows = append(out.shows, *l.shows)
		}
	}
	out.text = strings.Join(texts, "\n")
	return out
}

func (b *Bridge) screenHead(ctx context.Context, sessionID, lang string) string {
	h, _ := b.Handle(ctx, sessionID)
	return msg(lang, "screenHead", h)
}

// screenLines is how much of a pane "screen" sends. A phone shows about
// twenty lines; the thirty this was sent two screens of scrolling to reach
// the prompt, which is the line that matters.
const screenLines = 20

// screenText is the visible pane's last lines, trailing blanks dropped.
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
	if len(lines) > screenLines {
		lines = lines[len(lines)-screenLines:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
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
	h, _ := b.Handle(ctx, sessionID)
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

// parseDuration reads a mute's length the way people say it: "2h", "30m",
// "1d", "3天", "2小时", "两个小时", "半小时", "15分钟", "forever", plain
// minutes. ok is false when there was something and it was not understood,
// so the reply can say the default was used rather than silently pick it.
func parseDuration(s string, def time.Duration) (time.Duration, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(strings.ToLower(narrow(s))), " ", "")
	switch s {
	case "":
		return def, true
	case "forever", "永久", "一直":
		return 100 * 365 * 24 * time.Hour, true // long past any session's life
	case "半小时", "半个小时", "halfanhour":
		return 30 * time.Minute, true
	case "半天":
		return 12 * time.Hour, true
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, true
	}
	for _, u := range []struct {
		suffix string
		unit   time.Duration
	}{
		{"天", 24 * time.Hour}, {"d", 24 * time.Hour}, {"个小时", time.Hour}, {"小时", time.Hour}, {"h", time.Hour},
		{"分钟", time.Minute}, {"分", time.Minute}, {"min", time.Minute}, {"m", time.Minute}, {"", time.Minute},
	} {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		num := strings.TrimSuffix(s, u.suffix)
		num = strings.ReplaceAll(num, "两", "二")
		if n := parseNumber(num); n > 0 {
			return time.Duration(n) * u.unit, true
		}
	}
	return def, false
}

// preview is the first line of text, shortened, for the audit log.
func preview(s string) string {
	line := firstLine(strings.TrimSpace(s))
	head, more := cut(line, 80)
	if more {
		head += " …"
	}
	return head
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// isFiller is a message that is only a sound: "嗯", "哦哦", "啊。".
func isFiller(text string) bool {
	switch Normalize(text) {
	case "", "嗯", "哦", "噢", "喔", "啊", "呃", "额", "唔", "哈", "嗯哼", "那个":
		return true
	}
	return false
}
