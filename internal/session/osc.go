package session

import (
	"encoding/base64"
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
	case "9", "777":
		// Desktop notification. Treated as "this session wants attention"
		// rather than being shown verbatim, because the text is usually the
		// agent repeating what is already on screen.
		s.bell = true
	}
}

// drain returns everything collected since the last call and resets.
func (s *oscScanner) drain() (bell bool, clipboard, titles []string) {
	bell, clipboard, titles = s.bell, s.clipboard, s.titles
	s.bell, s.clipboard, s.titles = false, nil, nil
	return
}
