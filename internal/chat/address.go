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
//  3. For a sentence, the focus, the session this person chose with
//     "focus N"; then the one session that is waiting, if exactly one is.
//     For an answer (a bare "y"), the other way round: the waiting session
//     first, because a yes means the thing that asked. Two waiting sessions
//     and a bare "y" is refused with the list, because the cost of asking
//     again is a second message and the cost of guessing is a shell.
//
// The split is what the focus is for. With six sessions something is nearly
// always waiting, and when waiting outranked the focus for everything, every
// push took over the next sentence and the focus did nothing.
//
// "The one before last" and "that vibepanel one" are resolved before any of
// this by the assistant, into a handle; this file never sees natural
// language.

// handlePrefix matches a handle at the start of a message, in the three
// spellings people use, and returns the rest.
var handlePrefix = regexp.MustCompile(`^\s*(?:\[\s*(\d{1,4})\s*\]|#(\d{1,4})|(\d{1,4})\s*[:：]|【\s*(\d{1,4})\s*】)\s*`)

// cardHead is how a quoted card is recognised: a state glyph, then the
// handle, at the very start. Only a card is an address. The list the bridge
// sends when two sessions are waiting also contains handles, and a quote of
// it must not resolve to the first one, which is what "any [n] anywhere"
// did; the list's lines do not start the text, so they do not match.
var cardHead = regexp.MustCompile(`^\s*[▲●✓○]\s*\[\s*(\d{1,4})\s*\]`)

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
	for _, i := range []int{2, 4, 6, 8} {
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
// text rather than by id. Only a card counts; see cardHead.
func HandleInQuote(quoted string) (int, bool) {
	m := cardHead.FindStringSubmatch(quoted)
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
	How       How
}

// How a message found its session.
type How string

const (
	HowQuote       How = "quote"
	HowHandle      How = "handle"
	HowFocus       How = "focus"
	HowOnlyWaiting How = "only-waiting"
)

// Reason is why a bare message could not be delivered.
type Reason string

const (
	// ReasonNone: nothing is waiting and there is no focus.
	ReasonNone Reason = "none"
	// ReasonSeveral: more than one session is waiting.
	ReasonSeveral Reason = "several"
	// ReasonUnknown: the handle named is nobody.
	ReasonUnknown Reason = "unknown"
)

// Candidate is what the resolver knows about one session.
type Candidate struct {
	SessionID string
	Handle    int
	Waiting   bool
}

// Resolve applies the order above to a message. quoteSession is what the
// quoted message was about, if anything; focus is the chat's focus session;
// candidates is every session the chat may address; answer is whether the
// message is a yes or a no rather than words.
//
// A Reason other than "" means nothing was explicit and the rules below did
// not apply; the caller turns it into a sentence.
func Resolve(text string, quoteSession string, focus string, cands []Candidate, answer bool) (Target, string, Reason) {
	if quoteSession != "" {
		return Target{SessionID: quoteSession, How: HowQuote}, text, ""
	}
	if h, rest, ok := SplitHandle(text); ok {
		for _, c := range cands {
			if c.Handle == h {
				return Target{SessionID: c.SessionID, How: HowHandle}, rest, ""
			}
		}
		return Target{}, text, ReasonUnknown
	}
	focused := func() (Target, bool) {
		for _, c := range cands {
			if focus != "" && c.SessionID == focus {
				return Target{SessionID: focus, How: HowFocus}, true
			}
		}
		return Target{}, false
	}
	if !answer {
		if t, ok := focused(); ok {
			return t, text, ""
		}
	}
	// A yes: the one waiting session, else the focus, and two waiting is
	// refused, even with a focus, because the person cannot see which one
	// just asked.
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
		return Target{SessionID: waiting[0].SessionID, How: HowOnlyWaiting}, text, ""
	}
	if t, ok := focused(); ok {
		return t, text, ""
	}
	return Target{}, text, ReasonNone
}
