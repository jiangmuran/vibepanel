package chat

import (
	"regexp"
	"strconv"
	"strings"
)

// Where a reply goes.
//
// One chat window, many sessions. Every message a person sends has to land in
// exactly one of them or nowhere, and the rules for deciding are the whole
// product: the panel exists because "which of my twelve agents is asking" is
// the hard question, and answering it wrong from a phone types a word into
// the wrong program.
//
// Resolution order, most explicit first:
//
//  1. A quoted message. The IM says which message was replied to (by ref, or
//     by its text on 微信) and the bridge remembers what every message it sent
//     was about.
//  2. A handle in the text: "3: continue", "#3 continue", "[3] continue".
//  3. The focus: the session this chat last talked to, or was last told
//     about -- but only while exactly one session is waiting. Two waiting
//     sessions and a bare "y" is refused with the list, because the cost of
//     asking again is a second message and the cost of guessing is a shell.
//
// "The one before last" and "that vibepanel one" are resolved before any of
// this by the assistant, into a handle; this file never sees natural
// language.

// handlePrefix matches a handle at the start of a message, in the three
// spellings people use, and returns the rest.
var handlePrefix = regexp.MustCompile(`^\s*(?:\[\s*(\d{1,4})\s*\]|#(\d{1,4})|(\d{1,4})\s*[:：])\s*`)

// handleInText finds a handle anywhere in quoted text, which is how a quote
// of one of the bridge's own cards is read back: every card begins "[3]".
var handleInText = regexp.MustCompile(`\[\s*(\d{1,4})\s*\]`)

// SplitHandle takes a leading handle off a message.
//
// A bare number is not a handle: "3" is what somebody answers to "which
// option", and Claude Code's menus are numbered. It has to be followed by a
// colon, wrapped in brackets, or prefixed with #.
func SplitHandle(text string) (handle int, rest string, ok bool) {
	m := handlePrefix.FindStringSubmatchIndex(text)
	if m == nil {
		return 0, text, false
	}
	for _, i := range []int{2, 4, 6} {
		if m[i] >= 0 {
			n, err := strconv.Atoi(text[m[i]:m[i+1]])
			if err != nil || n <= 0 {
				return 0, text, false
			}
			return n, strings.TrimSpace(text[m[1]:]), true
		}
	}
	return 0, text, false
}

// HandleInQuote reads the handle out of quoted text, for IMs that quote by
// text rather than by id.
func HandleInQuote(quoted string) (int, bool) {
	m := handleInText.FindStringSubmatch(quoted)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Target is where a message resolved to, and how.
type Target struct {
	SessionID string
	// How is one of: quote, handle, focus, only-waiting. Shown in the receipt
	// so a person can see why their words went where they did.
	How string
}

// Candidate is what the resolver knows about one session.
type Candidate struct {
	SessionID string
	Handle    int
	Waiting   bool
}

// Resolve applies the order above to a message. quoteSession is what the
// quoted message was about, if anything; focus is the chat's focus session;
// candidates is every session the chat may address.
//
// It returns ok == false with a reason when nothing is explicit and the focus
// rule does not apply. The reason is one of ReasonNone, ReasonSeveral and
// ReasonUnknown, which the caller turns into a sentence.
func Resolve(text string, quoteSession string, focus string, cands []Candidate) (Target, string, string) {
	if quoteSession != "" {
		return Target{SessionID: quoteSession, How: "quote"}, text, ""
	}
	if h, rest, ok := SplitHandle(text); ok {
		for _, c := range cands {
			if c.Handle == h {
				return Target{SessionID: c.SessionID, How: "handle"}, rest, ""
			}
		}
		return Target{}, text, ReasonUnknown
	}
	// Focus and the single-waiting rule, in that order of trust: a focus is
	// something this chat chose; the only waiting session is something the
	// panel noticed. Either applies alone only when it would not send the
	// words to a session that is not the one asking -- when two sessions are
	// waiting, even a focused one is refused, because a bare "y" almost
	// always means "the one that just asked", and the person cannot see
	// which one that is.
	var waiting []Candidate
	for _, c := range cands {
		if c.Waiting {
			waiting = append(waiting, c)
		}
	}
	if len(waiting) > 1 {
		return Target{}, text, ReasonSeveral
	}
	if len(waiting) == 1 {
		return Target{SessionID: waiting[0].SessionID, How: "only-waiting"}, text, ""
	}
	if focus != "" {
		for _, c := range cands {
			if c.SessionID == focus {
				return Target{SessionID: focus, How: "focus"}, text, ""
			}
		}
	}
	return Target{}, text, ReasonNone
}

// Why a bare message could not be delivered.
const (
	ReasonNone    = "none"
	ReasonSeveral = "several"
	ReasonUnknown = "unknown"
)
