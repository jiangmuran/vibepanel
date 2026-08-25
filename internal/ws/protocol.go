// Package ws carries terminal traffic between the browser and the session
// manager over a single multiplexed WebSocket.
//
// One connection per browser, not one per terminal. A user with a main session
// plus four bottom terminals would otherwise hold five sockets, and mobile
// browsers throttle background connections aggressively enough that some of
// them would quietly stall.
package ws

import (
	"encoding/binary"
	"errors"
)

// Frame types for binary messages. Text messages are JSON and carry control.
const (
	// FrameData is terminal bytes, in either direction. It is binary because
	// it is the overwhelming majority of traffic and base64 in JSON would cost
	// a third more bandwidth for nothing.
	FrameData byte = 0x00

	// FrameReplay is the scrollback a viewer receives when it subscribes.
	//
	// It has to be distinguishable from live output. The buffer contains
	// whatever the application sent, including terminal capability queries
	// (device attributes, cursor position reports). A freshly created xterm
	// answers those as it parses them — and the answer goes to the shell,
	// which types it at the prompt. Without this flag every page reload
	// injects something like "[?1;2c" into whatever the session was doing.
	FrameReplay byte = 0x01
)

// binaryHeaderLen is the type byte plus the uint32 stream reference.
const binaryHeaderLen = 5

// ErrShortFrame means a binary frame was too small to contain a header.
var ErrShortFrame = errors.New("ws: short binary frame")

// EncodeReplay builds a replay frame. See FrameReplay for why it is separate.
func EncodeReplay(ref uint32, payload []byte) []byte {
	out := EncodeData(ref, payload)
	out[0] = FrameReplay
	return out
}

// EncodeData builds a binary data frame for a stream.
//
// Streams are addressed by a small integer assigned at subscribe time rather
// than by their id string. At 60 frames a second across five terminals, a
// 16-character id in every frame is real overhead for a value the peer already
// knows.
func EncodeData(ref uint32, payload []byte) []byte {
	out := make([]byte, binaryHeaderLen+len(payload))
	out[0] = FrameData
	binary.BigEndian.PutUint32(out[1:5], ref)
	copy(out[binaryHeaderLen:], payload)
	return out
}

// DecodeData splits a binary frame into its stream reference and payload.
//
// The returned payload aliases the input; callers that keep it must copy.
func DecodeData(frame []byte) (ref uint32, payload []byte, err error) {
	if len(frame) < binaryHeaderLen {
		return 0, nil, ErrShortFrame
	}
	if frame[0] != FrameData && frame[0] != FrameReplay {
		return 0, nil, errors.New("ws: unknown binary frame type")
	}
	return binary.BigEndian.Uint32(frame[1:5]), frame[binaryHeaderLen:], nil
}

// ─── control messages, client to server ───────────────────────────────────

// ClientMessage is any JSON message from the browser. The Type field selects
// which of the other fields are meaningful.
type ClientMessage struct {
	Type string `json:"t"`

	SessionID string `json:"sessionId,omitempty"`
	Ref       uint32 `json:"ref,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`

	// Text carries a MsgPaste body. Input otherwise arrives as binary frames;
	// a paste is not keystrokes and does not travel as them.
	Text string `json:"text,omitempty"`

	// Submit asks for a carriage return after the paste.
	//
	// Server-side because the two travel by different roads — the paste
	// through the tmux command socket, the return through the PTY — and a
	// client sending them one after another is racing them. Sending the return
	// only once the paste has been accepted is the only ordering that holds.
	Submit bool `json:"submit,omitempty"`
}

// Client message types.
const (
	// MsgSubscribe asks to start receiving a session's output. The server
	// replies with MsgSubscribed carrying the stream reference and the replay
	// buffer arrives as ordinary data frames straight afterwards.
	MsgSubscribe = "subscribe"

	// MsgUnsubscribe stops the stream and frees the reference.
	MsgUnsubscribe = "unsubscribe"

	// MsgPaste delivers a block of text as a paste rather than as typing.
	//
	// Keystrokes go into the PTY as bytes, and a multi-line block sent that
	// way is indistinguishable from someone pressing Enter after every line:
	// a shell runs each one, and an agent acts on the first sentence of a
	// three-line instruction before it has read the third. Bracketed paste is
	// what makes it one submission, and only the pane's own application knows
	// whether it wants the markers — so this goes through tmux, which tracks
	// that per pane, instead of through the PTY.
	MsgPaste = "paste"

	// MsgResize reports the viewer's viewport. Honoured only if this viewer
	// controls the session; otherwise the server ignores it and the viewer
	// keeps scaling to the authoritative grid.
	MsgResize = "resize"

	// MsgTakeControl claims the grid for this viewer and applies its size.
	MsgTakeControl = "takeControl"

	// MsgPing keeps intermediaries from closing an idle connection. Mobile
	// networks and reverse proxies both do this on quiet sockets.
	MsgPing = "ping"
)

// ─── control messages, server to client ───────────────────────────────────

// ServerMessage is any JSON message to the browser.
type ServerMessage struct {
	Type string `json:"t"`

	SessionID string `json:"sessionId,omitempty"`
	Ref       uint32 `json:"ref,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Text      string `json:"text,omitempty"`
	Message   string `json:"message,omitempty"`

	// Controlling tells the viewer whether it owns the grid, so the UI can show
	// a "take control" affordance instead of silently ignoring resizes.
	Controlling bool `json:"controlling,omitempty"`
}

// Server message types.
const (
	// MsgSubscribed confirms a subscription and assigns its stream reference.
	MsgSubscribed = "subscribed"

	// MsgSize announces the authoritative grid for a session.
	MsgSize = "size"

	// MsgClipboard carries text the pane copied via OSC 52.
	MsgClipboard = "clipboard"

	// MsgTitle carries a new pane title for automatic tab naming.
	MsgTitle = "title"

	// MsgExit means the session ended; the viewer should stop expecting output.
	MsgExit = "exit"

	// MsgDropped means this viewer fell too far behind and its stream was cut.
	// The client resubscribes, which replays from the ring buffer.
	MsgDropped = "dropped"

	// MsgError reports a failed request without closing the connection.
	MsgError = "error"

	// MsgPong answers MsgPing.
	MsgPong = "pong"

	// MsgState carries the full project and session list after anything
	// changed. A full snapshot rather than a delta: the list is small, and a
	// delta protocol is a second source of truth that can drift from the first
	// in ways nobody notices until the sidebar is showing a session that was
	// killed ten minutes ago.
	MsgState = "state"

	// MsgPanel says a project's note or todo list changed elsewhere, without
	// carrying either: they are per project, they can be long, and pushing a
	// document to every viewer on every keystroke would be waste. The panel
	// that cares refetches.
	//
	// This arrived as a bare string in the sender while its nine siblings had
	// constants, which is the drift this file exists to prevent.
	MsgPanel = "panel"
)

// AllServerMessages and AllClientMessages exist so the two ends of this
// protocol can be compared.
//
// The names live here and again in web/src/protocol/wire.ts as a union type,
// and nothing checked that the two agreed. A message the server sends and the
// client's switch has no case for is discarded in silence — which is exactly
// how a viewer that had been cut off would stop recovering, with no error
// anywhere. TestMessageTypesMatchTheClient reads that file and compares.
var (
	AllServerMessages = []string{
		MsgSubscribed, MsgSize, MsgClipboard, MsgTitle, MsgExit,
		MsgDropped, MsgError, MsgPong, MsgState, MsgPanel,
	}
	AllClientMessages = []string{
		MsgSubscribe, MsgUnsubscribe, MsgResize, MsgTakeControl, MsgPing,
		MsgPaste,
	}
)
