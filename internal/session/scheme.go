package session

import "bytes"

// Telling tmux the palette changed.
//
// tmux asks its client for the foreground, the background and the colour
// scheme the moment it attaches, caches the answers, and gives them to every
// pane that asks later. It also turns on DEC private mode 2031, which is a
// client's way of saying "tell me, unprompted, when that changes". Measured
// against tmux 3.6 on a throwaway socket: after the client writes
// `\x1b[?997;1n`, tmux asks for OSC 10 and 11 again straight away, and a pane
// that queries afterwards gets the new background.
//
// The panel never sent it. A browser switching theme updated what the pump
// would answer, and tmux never asked again, so every agent started after the
// switch was told the old palette.

// schemeReportsMode reports whether a chunk of tmux's output turns mode 2031
// on or off, and whether it mentioned the mode at all.
//
// The later of the two wins when a chunk holds both. Split across two reads it
// is missed: tmux writes it in the same attach burst as the colour queries,
// and terminalQueryReplies already relies on those arriving whole, so this
// assumes nothing weaker than what is there.
func schemeReportsMode(chunk []byte) (on, seen bool) {
	set := bytes.LastIndex(chunk, []byte("\x1b[?2031h"))
	reset := bytes.LastIndex(chunk, []byte("\x1b[?2031l"))
	switch {
	case set < 0 && reset < 0:
		return false, false
	case set > reset:
		return true, true
	default:
		return false, true
	}
}

// schemeReport is the notification itself: DSR 997, 1 for dark and 2 for
// light. The same two answers terminalQueryReplies gives when asked.
func schemeReport(dark bool) []byte {
	if dark {
		return []byte("\x1b[?997;1n")
	}
	return []byte("\x1b[?997;2n")
}
