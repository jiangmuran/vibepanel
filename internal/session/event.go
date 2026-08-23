package session

// EventKind tags what a subscriber is being told about.
type EventKind uint8

const (
	// EventOutput carries raw PTY bytes. This is the overwhelming majority of
	// traffic, which is why it travels as binary rather than JSON.
	EventOutput EventKind = iota

	// EventClipboard carries text the pane asked to put on the clipboard, via
	// OSC 52. tmux emits this to us when set-clipboard is on; we relay it so a
	// copy inside the terminal lands on the viewer's real clipboard.
	EventClipboard

	// EventSize announces the authoritative grid. Viewers that are not the
	// controller scale to it rather than reflowing.
	EventSize

	// EventTitle carries a new pane title, used for automatic tab naming.
	EventTitle

	// EventExit means the attachment ended: the tmux session is gone, or the
	// panel detached. Viewers show the session as no longer live.
	EventExit
)

// Event is one thing that happened in a session.
type Event struct {
	Kind EventKind
	Data []byte // EventOutput
	Text string // EventClipboard, EventTitle
	Cols int    // EventSize
	Rows int    // EventSize
}
