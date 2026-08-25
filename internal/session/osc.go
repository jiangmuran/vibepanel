package session

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

// oscScanner pulls out-of-band signals from a PTY byte stream without getting
// in the way of it.
//
// It is a scanner, not a parser: it looks for a handful of specific sequences
// and ignores everything else. Writing a real terminal emulator here would mean
// maintaining one, and the browser already has one.
type oscScanner struct {
	// pending holds a trailing fragment that might be the start of a sequence
	// split across two reads. PTY reads land on arbitrary boundaries, so a
	// scanner without this misses roughly one sequence in every few thousand —
	// rarely enough to look like a flake rather than a bug.
	pending []byte

	// bell is set when a BEL arrived since the last drain. The state detector
	// reads it as "the agent wants a human".
	bell bool

	clipboard []string
	titles    []string
}

// maxPending bounds the fragment we carry between reads. An OSC payload longer
// than this is not one we care about, and holding it would let a pane grow our
// memory by emitting an unterminated sequence.
const maxPending = 64 << 10

func newOSCScanner() *oscScanner { return &oscScanner{} }

// feed scans a chunk. It never modifies or withholds the bytes: the browser
// gets the untouched stream, because anything we strip is something xterm.js
// then cannot render.
func (s *oscScanner) feed(chunk []byte) {
	buf := chunk
	if len(s.pending) > 0 {
		buf = append(s.pending, chunk...)
		s.pending = nil
	}

	consumed := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0x07 {
			// A BEL that terminates an OSC sequence is handled inside the OSC
			// branch below and never reaches here, so anything left is a real
			// bell from the application.
			s.bell = true
			consumed = i + 1
			continue
		}
		if buf[i] != 0x1b || i+1 >= len(buf) {
			continue
		}
		if buf[i+1] != ']' {
			continue
		}
		end, payload, ok := parseOSC(buf[i:])
		if !ok {
			// Possibly a sequence split across reads: keep the tail and wait.
			if len(buf)-i <= maxPending {
				s.pending = append([]byte(nil), buf[i:]...)
				return
			}
			// Too long to be anything we handle; drop it and move on rather
			// than growing without bound.
			consumed = i + 1
			continue
		}
		s.handleOSC(payload)
		i += end - 1
		consumed = i + 1
	}

	// Carry a short tail that could be the beginning of a sequence.
	if tail := len(buf) - consumed; tail > 0 && tail <= maxPending {
		if idx := lastEscapeStart(buf[consumed:]); idx >= 0 {
			s.pending = append([]byte(nil), buf[consumed+idx:]...)
		}
	}
}

// lastEscapeStart finds a trailing ESC that may begin an incomplete sequence.
func lastEscapeStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && len(b)-i <= 8; i-- {
		if b[i] == 0x1b {
			return i
		}
	}
	return -1
}

// parseOSC reads one OSC sequence starting at b[0] == ESC. It returns the
// length consumed and the payload between the introducer and the terminator.
//
// Both terminators are accepted: BEL, which most software emits, and ST
// (ESC backslash), which is what the standard actually specifies and what
// several agent TUIs use.
func parseOSC(b []byte) (n int, payload string, ok bool) {
	if len(b) < 3 || b[0] != 0x1b || b[1] != ']' {
		return 0, "", false
	}
	for i := 2; i < len(b); i++ {
		switch {
		case b[i] == 0x07:
			return i + 1, string(b[2:i]), true
		case b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\':
			return i + 2, string(b[2:i]), true
		case b[i] == 0x1b:
			// An ESC that is not the start of ST means the sequence was
			// abandoned; do not swallow the following one.
			return 0, "", false
		}
	}
	return 0, "", false
}

// handleOSC records the sequences we care about.
func (s *oscScanner) handleOSC(payload string) {
	code, rest, found := strings.Cut(payload, ";")
	if !found {
		return
	}
	switch code {
	case "0", "2":
		// Window and icon title. This is how a session gets named without the
		// user typing anything.
		if utf8.ValidString(rest) {
			s.titles = append(s.titles, rest)
		}
	case "52":
		// OSC 52: <targets>;<base64>. A payload of "?" is a read request, not
		// a copy, and must not be treated as clipboard content.
		_, data, ok := strings.Cut(rest, ";")
		if !ok || data == "" || data == "?" {
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return
		}
		if utf8.Valid(decoded) {
			s.clipboard = append(s.clipboard, string(decoded))
		}
	case "9":
		// Nothing reaches this today, and that is worth knowing before
		// reasoning about it.
		//
		// tmux does not forward OSC 9 or OSC 777 to its client under this
		// panel's configuration, and does not convert either into a window
		// bell or activity flag — measured, and pinned by
		// TestTmuxSwallowsDesktopNotificationSequences in the tmux package. So
		// the terminal bell is not merely the most reliable signal available
		// without hooks; it is the only one that arrives at all, even from an
		// agent that does the polite thing and sends a notification sequence.
		//
		// Kept and made correct anyway: a tmux that starts forwarding these
		// would otherwise arrive with the bug below already in place.
		//
		// Two unrelated things share this number, and only one of them is
		// somebody asking for a human.
		//
		// `OSC 9 ; <text>` is the iTerm2 desktop notification. That is
		// attention, and without hooks it is most of the attention there is.
		//
		// `OSC 9 ; 4 ; <state> ; <percent>` is the ConEmu progress indicator,
		// which anything drawing a progress bar emits over and over during a
		// long operation — a build, an install, a download. Reading it as a
		// notification turned every progress update into "needs you", on the
		// one signal this panel exists to surface, and waiting sorts to the
		// top: a build reporting progress would sit above the agent that
		// really had stopped and asked.
		//
		// Prefix "4;" rather than a leading "4", so a notification whose text
		// begins with a digit — "4 tests failed" — is still a notification.
		if rest == "4" || strings.HasPrefix(rest, "4;") {
			return
		}
		s.bell = true
	case "777":
		// urxvt's convention is `777;notify;<title>;<body>`, and other
		// subcommands exist under the same number. Only the notification is
		// somebody asking for something.
		if rest != "notify" && !strings.HasPrefix(rest, "notify;") {
			return
		}
		s.bell = true
	}
}

// Nominal cell size in pixels, for answering tmux's pixel-dimension query.
//
// Nothing measures a real cell here — the real terminal is xterm.js in a
// browser we cannot see. These are a plausible 13px monospace cell, and the
// answer is only used to derive pixel dimensions for image protocols.
const (
	cellWidthPx  = 8
	cellHeightPx = 17
)

// terminalQueryReplies answers the capability queries tmux sends its client.
//
// On attach tmux asks a batch of questions and waits for the answers. Ours
// answered none, so five seconds later tmux gave up, applied defaults, and
// re-initialised every session at once. That burst counted as output and reset
// every session's state — wiping any "waiting" a bell had just raised, which
// is the one thing this panel exists to show.
//
// Answering is also simply correct. From tmux's point of view this PTY is the
// terminal; a terminal that ignores the questions leaves tmux guessing about
// colour support and cell size for every session.
//
// The answers deliberately mirror what xterm.js reports, because xterm.js is
// what actually renders this: tmux's model of the terminal should match the
// thing on the other end of it.
//
// Only we answer. The queries do reach the browser in the output stream, but
// they arrive at attach — which now happens when a session is created, before
// any viewer subscribes — and replayed scrollback is delivered with the
// browser's responses suppressed. Two answers would be worse than none: the
// second is delivered to the pane as if the user had typed it.
func terminalQueryReplies(chunk []byte, cols, rows int) []byte {
	var out []byte
	add := func(s string) { out = append(out, s...) }

	for _, q := range []struct {
		query string
		reply func()
	}{
		// Primary and secondary device attributes.
		{"\x1b[c", func() { add("\x1b[?1;2c") }},
		{"\x1b[>c", func() { add("\x1b[>0;276;0c") }},
		// XTVERSION. The name is free-form and shows up in tmux's logs.
		{"\x1b[>q", func() { add("\x1bP>|vibepanel\x1b\\") }},
		// Foreground and background colour. The real palette lives in the
		// browser and changes with the theme; these exist so tmux stops
		// waiting, and it only uses them to decide light-versus-dark defaults.
		{"\x1b]10;?\x1b\\", func() { add("\x1b]10;rgb:cccc/cccc/cccc\x1b\\") }},
		{"\x1b]11;?\x1b\\", func() { add("\x1b]11;rgb:0000/0000/0000\x1b\\") }},
		// Colour-scheme report: 1 is dark, 2 is light.
		{"\x1b[?996n", func() { add("\x1b[?997;1n") }},
		// Text area size, in characters and in pixels.
		{"\x1b[18t", func() { add(fmt.Sprintf("\x1b[8;%d;%dt", rows, cols)) }},
		{"\x1b[14t", func() {
			add(fmt.Sprintf("\x1b[4;%d;%dt", rows*cellHeightPx, cols*cellWidthPx))
		}},
	} {
		if bytes.Contains(chunk, []byte(q.query)) {
			q.reply()
		}
	}
	return out
}

// hasPrintable reports whether a chunk contains anything a person would see.
//
// Used to decide what counts as activity. A chunk of nothing but mode sets and
// cursor moves is the terminal being reconfigured, not the session producing
// output, and treating it as work is how a quiet session reads as busy.
func hasPrintable(b []byte) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == 0x1b:
			// Skip the escape sequence rather than inspecting its parameters,
			// which are ASCII and would otherwise look like content.
			i += escapeLength(b[i:]) - 1
		case c == '\n', c == '\t', c >= 0x20 && c != 0x7f:
			return true
		}
	}
	return false
}

// escapeLength returns how many bytes the escape sequence at the start of b
// occupies, or 1 if it is not one we recognise.
func escapeLength(b []byte) int {
	if len(b) < 2 || b[0] != 0x1b {
		return 1
	}
	switch b[1] {
	case '[':
		// CSI: parameters and intermediates, then a final byte in 0x40–0x7e.
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		return len(b)
	case ']':
		// OSC: runs to BEL or ST.
		if n, _, ok := parseOSC(b); ok {
			return n
		}
		return len(b)
	case '(', ')', '*', '+', '#', ' ':
		// Character-set and similar two-parameter sequences.
		return 3
	}
	return 2
}

// drain returns everything collected since the last call and resets.
func (s *oscScanner) drain() (bell bool, clipboard, titles []string) {
	bell, clipboard, titles = s.bell, s.clipboard, s.titles
	s.bell, s.clipboard, s.titles = false, nil, nil
	return
}
