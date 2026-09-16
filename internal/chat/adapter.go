// Package chat is the bridge between the panel and the person's chat app.
//
// It has three layers and this file is the seam between two of them. Below it
// are the adapters -- one package per IM, each knowing one protocol and
// nothing about sessions. Above it is the bridge, which knows sessions,
// handles, routing rules and the write path into a tmux pane, and nothing
// about any IM. The seam is the Adapter interface plus the Capabilities it
// declares: the bridge never asks "is this Telegram", it asks "can this edit a
// message" and picks a strategy.
//
// Everything is private chat. A peer is one person; there are no groups,
// threads or topics anywhere in these types, on purpose (the owner wanted
// 单聊 and nothing else), so an adapter for an IM that has them has nowhere to
// put them and the bridge never has to think about who else is reading.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Capabilities is what an adapter can do, declared once and read by the
// bridge to choose how a session's day is presented.
//
// Every field here is a strategy switch in the bridge, not documentation. If a
// field is added that nothing reads, it is documentation, and it will drift.
type Capabilities struct {
	// Edit: a sent message can be replaced in place. With it, a session has
	// one status message that changes; without it, every change is a new
	// message, so the bridge sends fewer of them.
	Edit bool
	// Buttons: a message can carry buttons whose presses come back as an
	// Action. Without them, a prompt is answered by typing a word.
	Buttons bool
	// QuoteRefs: an inbound reply that quotes a message carries the quoted
	// message's Ref, and the bridge resolves the quote by it. Without it
	// the bridge reads QuotedText and finds the handle in the text, which
	// is why every card starts with its handle.
	QuoteRefs bool
	// Proactive: the adapter can message a peer at any time. Without it, the
	// bridge can only speak while it holds a ContextToken from the peer's
	// last inbound message, and a push with none is dropped and counted.
	Proactive bool
	// MaxText is the longest text one message may carry, in runes; the
	// bridge splits past it. Zero means the adapter has no known limit.
	MaxText int
	// Images: SendImage works.
	Images bool
	// Typing: Typing works and is worth calling.
	Typing bool
}

// How an adapter renders is its own business: the bridge composes a Card and
// the adapter draws it in its IM's markup (RenderPlain, RenderHTML,
// RenderMarkdown are there to share). An earlier version declared a
// "flavor" here that nothing read, which is the drift the comment above
// warns about.

// ErrNeedsHello is what an adapter wraps when a message could not go out
// because the person has to speak first: 微信 answers only inside a context
// token from the person's last message, and that token expires and runs out
// of replies. The bridge counts it apart from other failures and, when the
// person next writes, shows them what they missed rather than pretending it
// was delivered.
var ErrNeedsHello = errors.New("chat: the person has to message first")

// Peer is who the bridge is talking to, as an adapter needs to address them.
type Peer struct {
	ID string
	// ContextToken is the token the adapter attached to this peer's last
	// inbound message, when its IM needs one to reply (微信); empty otherwise.
	ContextToken string
}

// Inbound is one thing a person sent.
type Inbound struct {
	Channel  string
	PeerID   string
	PeerName string
	// Ref is the message's own id, kept so a later reply can quote it.
	Ref  string
	Text string
	// QuotedRef is the id of the message this one replies to, when the IM
	// says (Capabilities.QuoteRefs). QuotedText is the quoted text when it
	// only says that.
	QuotedRef  string
	QuotedText string
	// Action is set when this is a button press rather than a message, and
	// Text is then empty. Value is what the bridge put on the button.
	Action *Action
	// ContextToken is what the IM needs echoed to answer this person; see
	// Peer.ContextToken. Empty for IMs that need nothing.
	ContextToken string
	// FetchImage downloads an attached picture, as the bytes of a PNG or
	// JPEG, when the bridge asks. A function rather than the bytes because
	// the download costs the IM's bandwidth and the panel's disk and the
	// bridge only asks for a paired person; a stranger's picture is never
	// fetched. Nil when the message carries no picture.
	FetchImage func(ctx context.Context) ([]byte, error)
	// A voice note arrives as Text when the IM transcribes it (微信 does)
	// and as nothing at all when it does not: the bridge ignores an empty
	// message, which is better than pretending to have heard.
	At time.Time
}

// Action is a button press.
type Action struct {
	// Value is the opaque string the bridge put on the button.
	Value string
	// ID is what the adapter needs to acknowledge the press, where its IM
	// wants that (Telegram's callback query id).
	ID string
	// MessageRef is the message the button was on, for editing it afterwards.
	MessageRef string
}

// Outbound is one thing to send. Exactly one of Text and Card is set.
type Outbound struct {
	// Text is a plain reply: the answer to a command, a receipt, an error.
	Text string
	// Code is monospace text to show as a block: a screen capture.
	Code string
	// Card is a session's status, structured so each adapter can lay it out.
	Card *Card
	// Buttons go under the card, on adapters that have them.
	Buttons []Button
	// ReplyTo quotes the inbound message this answers, where the IM shows
	// that; adapters that cannot ignore it.
	ReplyTo string
}

// Card is one session, as a chat shows it.
//
// Handle is first in every rendering, in the form "[3]", because on the one
// IM that cannot carry a message id in a quote it is the only way a reply
// finds its way back. The state is a glyph and a word, never a colour (red
// line 4 reaches the phone too).
type Card struct {
	Handle  int
	Title   string
	Project string
	// State is the panel's state word: waiting, working, done.
	State string
	// Glyph and StateText are State rendered for a person: "▲" and "在等你".
	Glyph     string
	StateText string
	// Body is what the agent said or is asking, already cut to fit.
	Body string
	// Footer is a short line under the body: how long ago, what tool.
	Footer string
	// URL opens the panel at this session. Empty when the panel's address
	// is one a phone cannot open (localhost), rather than a dead link.
	URL string
	// LinkLabel is the word the link is drawn as, in the chat's language.
	LinkLabel string
	// Hint is a last line saying how to answer, on an IM with no buttons.
	Hint string
}

// Button is one choice under a card.
type Button struct {
	Label string
	// Value comes back in Action.Value.
	Value string
	// Danger marks a choice an adapter should draw as destructive.
	Danger bool
}

// Adapter is one IM.
//
// Run blocks until ctx ends, delivering everything received through the sink.
// The other methods may be called from any goroutine while Run is running.
// An adapter that cannot do something its Capabilities say it cannot is never
// asked; one that fails at something it can do returns the error and the
// bridge logs and counts it.
type Adapter interface {
	Kind() string
	Capabilities() Capabilities
	Run(ctx context.Context, sink Sink) error
	Send(ctx context.Context, to Peer, m Outbound) (ref string, err error)
	Edit(ctx context.Context, to Peer, ref string, m Outbound) error
	SendImage(ctx context.Context, to Peer, png []byte, caption string) (ref string, err error)
	Typing(ctx context.Context, to Peer, on bool) error
	// Ack acknowledges a button press, with a short line for the IM to show
	// where it shows one. Called once per Action, before any Send, from the
	// bridge's own goroutine -- after a webhook adapter has already answered
	// the IM's HTTP request, so only an IM with an out-of-band
	// acknowledgement (Telegram's answerCallbackQuery) can do anything here;
	// the others make it a no-op.
	Ack(ctx context.Context, a Action, text string) error
}

// Sink is where an adapter puts what it receives and how it is doing.
type Sink interface {
	// Inbound hands over one message or press. It returns at once; the
	// bridge handles messages from one person in the order they arrived and
	// people independently of each other.
	Inbound(ctx context.Context, in Inbound)
	// Health reports a poll or connection outcome. ok with err == nil is a
	// successful round; ok == false is a failed one. The channel's page shows
	// the last of each, which is the only way to tell "nothing arrived" from
	// "nothing could arrive".
	Health(ok bool, err error)
	// State persists adapter state that must survive a restart -- a poll
	// cursor, a cached ticket. Kept sealed with the channel's config.
	State(ctx context.Context, state json.RawMessage)
}

// WebhookAdapter is an adapter whose IM calls it, rather than one it polls.
// The bridge mounts the handler at /api/chat/hooks/{kind}, unauthenticated;
// verifying the caller is the adapter's job, and it must do so on every
// request. 飞书 is this.
type WebhookAdapter interface {
	Adapter
	WebhookHandler() http.Handler
}

// LoginAdapter is an adapter that signs in by a QR code scanned on a phone
// rather than by a token typed into a form. 微信 is this.
//
// The flow is: StartLogin returns a QR image URL and a handle; the settings
// page shows the image and polls LoginStatus until it reports done or an
// error, at which point the adapter has stored what it needs through
// Sink.State and Login.Credentials tells the bridge what to seal.
type LoginAdapter interface {
	Adapter
	StartLogin(ctx context.Context) (Login, error)
	LoginStatus(ctx context.Context, id string) (Login, error)
	// SubmitCode supplies a verification code the phone displayed, for IMs
	// that ask for one mid-login.
	SubmitCode(ctx context.Context, id, code string) error
}

// Login is the state of a QR sign-in.
type Login struct {
	ID string `json:"id"`
	// QRURL is what to draw as a QR code; empty once it has been used.
	QRURL string `json:"qrUrl"`
	// Status is one of: waiting, scanned, needCode, done, expired, failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Credentials is the configuration to seal once Status is done. It
	// replaces the channel's stored config.
	Credentials json.RawMessage `json:"-"`
}

// Field describes one entry of an adapter's configuration, for the settings
// page to draw a form from and for the server to know which values to
// withhold on the way back out.
type Field struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
	// Hint is shown in the field; where a value comes from. HintZh is the
	// same in Chinese, because the page is bilingual and a hint in the wrong
	// language is a hint nobody reads.
	Hint   string `json:"hint,omitempty"`
	HintZh string `json:"hintZh,omitempty"`
}

// Env is what an adapter gets from the panel when it is built.
type Env struct {
	// HTTP is the client to use for the IM's API, so tests can point it at
	// a fake and so one timeout policy covers every adapter.
	HTTP *http.Client
	// State is the adapter's persisted state from the last run, or nil.
	State json.RawMessage
	// PublicURL is the panel's own address, for adapters that must tell
	// their IM where to call back.
	PublicURL string
	Logf      func(format string, args ...any)
}

// Factory builds an adapter from its sealed configuration.
type Factory struct {
	Kind string
	// Label is the IM's name as a person knows it.
	Label string
	// Fields is the configuration form. An adapter with a QR login has none.
	Fields []Field
	// Login marks an adapter built by scanning rather than typing.
	Login bool
	// Webhook marks an adapter the IM calls back, so the page can show the
	// URL to paste into the IM's console.
	Webhook bool
	New     func(config json.RawMessage, env Env) (Adapter, error)
}

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register adds an adapter kind. Called from each adapter package's init, so
// that importing the package is what makes the IM available and the bridge
// has no list of its own to keep in step.
func Register(f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if f.Kind == "" || f.New == nil {
		panic("chat: a factory needs a kind and a constructor")
	}
	if _, dup := registry[f.Kind]; dup {
		panic(fmt.Sprintf("chat: adapter %q registered twice", f.Kind))
	}
	registry[f.Kind] = f
}

// Factories lists the registered kinds, sorted, for the settings page.
func Factories() []Factory {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Factory, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// FactoryFor returns one kind's factory.
func FactoryFor(kind string) (Factory, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	f, ok := registry[kind]
	return f, ok
}
