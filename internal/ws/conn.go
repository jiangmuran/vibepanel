package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
)

// writeTimeout bounds a single write. A viewer whose TCP window has closed
// must not be able to hold a goroutine forever.
const writeTimeout = 10 * time.Second

// pingInterval keeps idle connections alive through proxies and mobile NAT,
// both of which drop quiet sockets after a minute or two.
const pingInterval = 30 * time.Second

// maxInboundMessage bounds one message from the browser.
//
// The library's default is 32 KiB, and this is one socket for the whole page:
// every terminal, the state stream and the notifications ride on it. So a
// paste of a build log or a diff — the ordinary way somebody feeds context to
// an agent — did not fail as a paste, it closed the spine. Measured: 32769
// bytes in one frame and every terminal on the page reset, replayed its ring,
// and the pasted text never reached the pty.
//
// Four megabytes is past anything a person pastes by hand and small enough
// that holding one in memory per connection is nothing.
const maxInboundMessage = 4 << 20

// readLimit is the size the *library* is still allowed to close over.
//
// Deliberately above maxInboundMessage: readMessage enforces our bound by
// reading one byte past it and draining the rest, and it needs headroom to do
// that. Something still sending at twice the bound has stopped being a browser
// and the connection is the right thing to lose.
const readLimit = 2 * maxInboundMessage

// Resolver turns a session id into the tmux identity and size needed to attach.
// The HTTP layer supplies it so this package does not depend on the store.
type Resolver interface {
	Resolve(ctx context.Context, sessionID string) (tmuxName string, cols, rows int, err error)
	// RecordSize persists a size change so a reconnecting browser starts at
	// the grid the session was last used with.
	RecordSize(ctx context.Context, sessionID string, cols, rows int) error
}

// Handler upgrades HTTP requests and runs one Conn per browser.
type Handler struct {
	Manager *session.Manager
	Resolve Resolver
	Log     *slog.Logger
	// OriginPatterns is passed to the WebSocket accept check. Empty means
	// same-origin only, which is what we want when the panel is the edge: a
	// terminal reachable cross-origin is a terminal any page can drive.
	OriginPatterns []string

	// Hub receives every connection so state changes can be pushed to it.
	Hub *Hub

	// Snapshot, if set, builds the state payload sent to a client the moment
	// it connects. See where it is called for why that is not optional.
	Snapshot func(context.Context) []byte

	// StillAuthorized, if set, is asked periodically whether the request that
	// opened this connection would still be allowed. See revalidate.
	StillAuthorized func(*http.Request) bool

	// RevalidateEvery overrides how often that question is asked. Zero means
	// revalidateInterval. Tests set it short; nothing else should.
	RevalidateEvery time.Duration
}

// stream is one subscribed session on one connection.
type stream struct {
	ref       uint32
	sessionID string
	live      *session.Live
	sub       *session.Subscriber
	cancel    context.CancelFunc
}

// Conn is a single browser connection.
type Conn struct {
	h        *Handler
	ws       *websocket.Conn
	clientID string

	// writeMu serialises writes. The websocket library allows one writer at a
	// time, and events arrive from one goroutine per subscribed session.
	writeMu sync.Mutex

	// One pending state snapshot, written by a goroutine of this connection's
	// own. See queueState.
	stateMu      sync.Mutex
	statePending []byte
	stateWake    chan struct{}
	// Messages that are not snapshots, kept beside the snapshot slot rather
	// than in it. See queueEvent.
	eventPending [][]byte
	eventSeen    map[string]bool

	mu      sync.Mutex
	streams map[uint32]*stream
	byID    map[string]*stream
	nextRef uint32
}

// queueState hands the connection the latest state snapshot to write, and
// never blocks.
//
// The hub used to call sendRaw for every connection in turn, from the caller's
// goroutine. sendRaw takes writeMu and then writes with a ten second timeout —
// but *taking* writeMu has no timeout at all, so a viewer whose TCP window had
// closed, with its own pump goroutine sitting inside a ten second write, held
// up the broadcast to everybody else for as long as that took.
//
// Measured against the real binary: one viewer that stopped reading delayed
// every other viewer's state update by 2.2 seconds. That was one connection
// tearing itself down quickly; the loop is serial, so the cost is per stalled
// viewer, and a client that reads just slowly enough never to be closed pays
// it again on every broadcast.
//
// A single slot rather than a queue, because a state snapshot is absolute: it
// describes the whole world, so a newer one makes an older one worthless.
// Dropping the superseded payload loses nothing, which is why this can be
// lossy where the terminal streams cannot — those carry bytes that exist
// nowhere else, and the session package drops them explicitly and says so with
// a `dropped` message.
func (c *Conn) queueState(payload []byte) {
	c.stateMu.Lock()
	c.statePending = payload
	c.stateMu.Unlock()
	select {
	case c.stateWake <- struct{}{}:
	default: // already awake; it will pick up the newer payload
	}
}

// queueEvent hands the connection a message that is *not* a snapshot.
//
// The single slot above is right for a snapshot and wrong for an event, and
// panel notifications went through it. A snapshot is absolute -- the newest one
// contains every older one, so replacing is free -- while "your notes for this
// project changed, fetch them again" is a fact about a moment. Replacing it
// with anything drops it, and the poller broadcasts a snapshot every two
// seconds, so the window is not theoretical: a note saved in one browser did
// not appear in another until something unrelated woke it.
//
// A queue rather than a second single slot, and deduplicated by payload rather
// than bounded by a number. These messages carry no content -- they say what
// changed and the panel that cares fetches it -- so two identical ones are one,
// and the queue's length is the number of distinct (project, kind) pairs with
// an unsent notification. That is small for the same reason the message is
// small, and it cannot grow without bound however hard a writer stalls.
//
// What this must not become is un-coalesced: the whole reason snapshots go
// through a slot is that "a viewer whose socket cannot take another byte must
// not be able to hold every other viewer's state update behind it", which was
// measured at 2.2 seconds per stalled viewer.
func (c *Conn) queueEvent(payload []byte) {
	key := string(payload)
	c.stateMu.Lock()
	if c.eventSeen == nil {
		c.eventSeen = map[string]bool{}
	}
	if !c.eventSeen[key] {
		c.eventSeen[key] = true
		c.eventPending = append(c.eventPending, payload)
	}
	c.stateMu.Unlock()
	select {
	case c.stateWake <- struct{}{}:
	default: // already awake; it will pick these up too
	}
}

// takePending removes everything waiting to be written.
//
// Split out so the queueing can be tested without a socket: what matters is
// that an event queued before a snapshot survives it, and that is a property of
// these three functions rather than of the network.
func (c *Conn) takePending() (events [][]byte, snapshot []byte) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	events, snapshot = c.eventPending, c.statePending
	c.eventPending, c.eventSeen, c.statePending = nil, nil, nil
	return events, snapshot
}

// stateWriter is the only goroutine that writes queued snapshots.
func (c *Conn) stateWriter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stateWake:
			events, payload := c.takePending()
			// Events first: a snapshot is the world as it is, and a
			// notification is a reason to go and read something the snapshot
			// does not carry. Either order is defensible; this one means a
			// viewer never sees "everything is current" a beat before being
			// told it is not.
			for _, e := range events {
				c.sendRaw(e)
			}
			if payload != nil {
				c.sendRaw(payload)
			}
		}
	}
}

// clientIDFrom reads the viewer's own identity, or mints one.
//
// The browser supplies this so that it survives a reconnect: grid ownership is
// held by a client id, and a viewer that comes back with a new one is a
// stranger to the arbitration. See the frontend for why it is per tab.
//
// Client-supplied and therefore not trusted for anything, which is fine
// because it grants nothing: any authenticated viewer can already press "take
// control". The validation is only to keep a hostile or broken value out of
// logs and comparisons.
func clientIDFrom(r *http.Request) string {
	got := r.URL.Query().Get("client")
	if len(got) < 8 || len(got) > 64 {
		return id.New()
	}
	for _, c := range got {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return id.New()
		}
	}
	return got
}

// ServeHTTP upgrades the request and serves the connection until it closes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  h.OriginPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept has already written a response.
		return
	}
	// Left at the library's 32 KiB, a paste closed the page's only socket. See
	// maxInboundMessage.
	c.SetReadLimit(readLimit)

	conn := &Conn{
		h:         h,
		ws:        c,
		clientID:  clientIDFrom(r),
		stateWake: make(chan struct{}, 1),
		streams:   map[uint32]*stream{},
		byID:      map[string]*stream{},
	}
	defer conn.closeAll()

	if h.Hub != nil {
		h.Hub.add(conn)
		defer h.Hub.remove(conn)
	}

	// Tell the new client what the world looks like, before anything happens
	// in it.
	//
	// The state message was only ever sent when something changed, and the
	// frontend has no other source for it — so a panel opened while everything
	// was quiet rendered an empty page and stayed that way until some session
	// happened to do something. Every check missed it because they all keep
	// something moving: an htop, a bell, a flood. The one that did not, and
	// that had never been run to completion, opened a browser onto two dozen
	// sleeping sessions and saw thirty-one bytes of body.
	//
	// Which is the case the panel is *for*. Coming back in the morning to see
	// which agents finished overnight is precisely the moment when nothing is
	// changing.
	if h.Snapshot != nil {
		if payload := h.Snapshot(r.Context()); len(payload) > 0 {
			conn.sendRaw(payload)
		}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go conn.keepalive(ctx)
	go conn.stateWriter(ctx)
	go h.revalidate(ctx, cancel, r)

	if err := conn.readLoop(ctx); err != nil &&
		websocket.CloseStatus(err) == -1 &&
		!errors.Is(err, context.Canceled) {
		h.logger().Debug("websocket ended", "err", err)
	}
	_ = c.CloseNow()
}

func (h *Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// revalidateInterval is how often a live connection re-checks its session.
//
// A socket is authorised once, at the handshake, and then lives for hours. So
// signing out, a session expiring, an administrator revoking one, and changing
// the password — which deletes every other browser's session precisely so that
// whoever had the old password stops having access — all left the *terminals*
// those browsers already had open streaming happily. Measured: after the
// session row was deleted, typing still reached the shell.
//
// Five seconds bounds how long that lasts. The cost is two indexed reads per
// open browser per interval — this panel has a handful of viewers, not a
// handful of thousands — and what is being bounded is terminal access after
// the access was revoked. Thirty seconds was the first number here and it is
// too long for the case that matters: somebody changing their password
// because it leaked, while whoever leaked it still has a socket open.
const revalidateInterval = 5 * time.Second

// revalidate closes the connection once its session stops being valid.
//
// The original request is what gets re-checked, which is the point: its cookie
// does not change, the row behind it does.
func (h *Handler) revalidate(ctx context.Context, cancel context.CancelFunc, r *http.Request) {
	if h.StillAuthorized == nil {
		return
	}
	every := h.RevalidateEvery
	if every <= 0 {
		every = revalidateInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !h.StillAuthorized(r) {
				h.logger().Info("closing a websocket whose session is no longer valid")
				cancel()
				return
			}
		}
	}
}

// keepalive pings until the connection goes away.
func (c *Conn) keepalive(ctx context.Context) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// errMessageTooBig is one refused message, which is not the end of the
// connection. See readMessage.
var errMessageTooBig = errors.New("ws: message over the inbound limit")

// readMessage reads one message, refusing an oversized one by itself.
//
// coder/websocket's own limit answers a message over it by closing the
// connection with StatusMessageTooBig, and this connection carries the whole
// page, so "too big" has to be sayable without taking every terminal with it.
// One byte past the bound is read to detect it; the rest is drained, because
// the message is still in the stream and the next Reader would otherwise start
// in the middle of it.
func (c *Conn) readMessage(ctx context.Context) (websocket.MessageType, []byte, error) {
	typ, r, err := c.ws.Reader(ctx)
	if err != nil {
		return 0, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInboundMessage+1))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > maxInboundMessage {
		if _, err := io.Copy(io.Discard, r); err != nil {
			return 0, nil, err
		}
		return typ, nil, errMessageTooBig
	}
	return typ, data, nil
}

// readLoop handles messages from the browser.
func (c *Conn) readLoop(ctx context.Context) error {
	for {
		typ, data, err := c.readMessage(ctx)
		if errors.Is(err, errMessageTooBig) {
			// Said out loud rather than dropped: the paste vanished, and a
			// terminal that silently ate one is worse than one that says so.
			c.sendError("", "that message was too large to send at once")
			continue
		}
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
			c.handleBinary(data)
		case websocket.MessageText:
			var msg ClientMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				c.sendError("", "malformed message")
				continue
			}
			c.handleControl(ctx, msg)
		}
	}
}

// handleBinary routes terminal input to its session.
func (c *Conn) handleBinary(frame []byte) {
	ref, payload, err := DecodeData(frame)
	if err != nil {
		return
	}
	c.mu.Lock()
	s := c.streams[ref]
	c.mu.Unlock()
	if s == nil {
		return
	}
	// Input does not claim the grid: see session.Live.Write for why. A viewer
	// answering "y" from a phone must not reflow the desktop it is shared with.
	if _, err := s.live.Write(c.clientID, payload); err != nil {
		c.sendError(s.sessionID, "write failed: "+err.Error())
	}
}

func (c *Conn) handleControl(ctx context.Context, msg ClientMessage) {
	switch msg.Type {
	case MsgPing:
		c.sendJSON(ServerMessage{Type: MsgPong})

	case MsgSubscribe:
		if err := c.subscribe(ctx, msg.SessionID, msg.Cols, msg.Rows); err != nil {
			c.sendError(msg.SessionID, err.Error())
		}

	case MsgUnsubscribe:
		c.unsubscribe(msg.SessionID)

	case MsgPaste:
		// Only for a session this connection is actually watching, same as
		// input: the session id arrives from the client and nothing else here
		// would check it.
		c.mu.Lock()
		s := c.byID[msg.SessionID]
		c.mu.Unlock()
		if s == nil || msg.Text == "" {
			return
		}
		if err := c.h.Manager.Paste(ctx, msg.SessionID, msg.Text); err != nil {
			c.sendError(msg.SessionID, "paste failed: "+err.Error())
			return
		}
		if msg.Submit {
			if _, err := s.live.Write(c.clientID, []byte("\r")); err != nil {
				c.sendError(msg.SessionID, "write failed: "+err.Error())
			}
		}

	case MsgResize, MsgTakeControl:
		c.mu.Lock()
		s := c.byID[msg.SessionID]
		c.mu.Unlock()
		if s == nil {
			return
		}
		var err error
		if msg.Type == MsgTakeControl {
			err = s.live.TakeControl(c.clientID, msg.Cols, msg.Rows)
		} else {
			err = s.live.Resize(c.clientID, msg.Cols, msg.Rows)
		}
		if err != nil {
			c.sendError(msg.SessionID, err.Error())
			return
		}
		cols, rows := s.live.Size()
		if rerr := c.h.Resolve.RecordSize(ctx, msg.SessionID, cols, rows); rerr != nil {
			c.h.logger().Warn("persist size", "session", msg.SessionID, "err", rerr)
		}

	default:
		// A type this server does not know.
		//
		// Ignoring it silently made a client sending a stale or misspelled type
		// look like one whose messages were being handled, which is the same
		// failure the error frame itself had: the client had no case for
		// `error` and dropped all six senders on the floor. Answering here is
		// only worth anything because that end now shows them.
		//
		// Truncated because msg.Type arrives from the client, is unbounded, and
		// this sends it back to be rendered. React escapes it, so the hazard is
		// length rather than markup -- a megabyte of JSON returning as a banner.
		t := msg.Type
		if len(t) > 40 {
			t = t[:40] + "…"
		}
		c.sendError(msg.SessionID, "unknown message type: "+t)
	}
}

// subscribe attaches (if needed), registers a viewer and starts pumping its
// events onto the socket.
func (c *Conn) subscribe(ctx context.Context, sessionID string, cols, rows int) error {
	if sessionID == "" {
		return errors.New("subscribe: missing sessionId")
	}
	c.mu.Lock()
	if _, dup := c.byID[sessionID]; dup {
		c.mu.Unlock()
		return nil // already subscribed; treat as idempotent
	}
	c.mu.Unlock()

	tmuxName, storedCols, storedRows, err := c.h.Resolve.Resolve(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if storedCols > 0 {
		cols, rows = storedCols, storedRows
	}

	live, err := c.h.Manager.Attach(ctx, sessionID, tmuxName, cols, rows)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	sub, replay := live.Subscribe(c.clientID)

	c.mu.Lock()
	c.nextRef++
	ref := c.nextRef
	sctx, cancel := context.WithCancel(ctx)
	s := &stream{ref: ref, sessionID: sessionID, live: live, sub: sub, cancel: cancel}
	c.streams[ref] = s
	c.byID[sessionID] = s
	c.mu.Unlock()

	gridCols, gridRows := live.Size()
	c.sendJSON(ServerMessage{
		Type: MsgSubscribed, SessionID: sessionID, Ref: ref,
		Cols: gridCols, Rows: gridRows,
		Controlling: live.Controller() == c.clientID,
	})

	// Replay before any live event reaches the socket. Subscribe took the
	// snapshot under the same lock that registered the subscriber, so the two
	// join up exactly with nothing lost or repeated.
	if len(replay) > 0 {
		c.sendBinary(EncodeReplay(ref, replay))
	}

	go c.pumpStream(sctx, s)
	return nil
}

// pumpStream forwards one session's events to the browser.
func (c *Conn) pumpStream(ctx context.Context, s *stream) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.sub.Events:
			if !ok {
				// Deregister first, and only then tell the client.
				//
				// The client answers "dropped" by resubscribing, and subscribe
				// short-circuits when this session is already registered on
				// this connection. Leaving the entry in place meant that
				// resubscribe was silently accepted and ignored: no snapshot,
				// no new ref, no further output. The terminal stayed frozen
				// until the page was reloaded — which is exactly the state the
				// message exists to get out of.
				//
				// The order matters and is not incidental. Doing it here means
				// deregistration has already happened before the client can
				// possibly have heard about the drop, so no resubscribe can
				// race ahead of it.
				c.deregister(s)
				if s.sub.Dropped() {
					// Say why it went quiet, so the viewer resubscribes and
					// replays rather than sitting on a dead terminal.
					c.sendJSON(ServerMessage{Type: MsgDropped, SessionID: s.sessionID, Ref: s.ref})
				} else {
					c.sendJSON(ServerMessage{Type: MsgExit, SessionID: s.sessionID, Ref: s.ref})
				}
				return
			}
			switch ev.Kind {
			case session.EventOutput:
				c.sendBinary(EncodeData(s.ref, ev.Data))
			case session.EventSize:
				c.sendJSON(ServerMessage{
					Type: MsgSize, SessionID: s.sessionID, Ref: s.ref,
					Cols: ev.Cols, Rows: ev.Rows,
					Controlling: s.live.Controller() == c.clientID,
				})
			case session.EventClipboard:
				c.sendJSON(ServerMessage{Type: MsgClipboard, SessionID: s.sessionID, Text: ev.Text})
			case session.EventTitle:
				c.sendJSON(ServerMessage{Type: MsgTitle, SessionID: s.sessionID, Text: ev.Text})
			case session.EventExit:
				c.sendJSON(ServerMessage{Type: MsgExit, SessionID: s.sessionID, Ref: s.ref})
			}
		}
	}
}

// deregister forgets a stream whose subscriber has ended.
//
// Only if it is still the current one for that session: a resubscribe may
// already have replaced it, and deleting by key would then unregister the live
// stream and leave the connection unable to reach a session it is watching.
func (c *Conn) deregister(s *stream) {
	c.mu.Lock()
	if c.byID[s.sessionID] == s {
		delete(c.byID, s.sessionID)
	}
	if c.streams[s.ref] == s {
		delete(c.streams, s.ref)
	}
	c.mu.Unlock()
	// Releases this stream's entry in the connection context's child list;
	// without it every dropped viewer leaves one behind for the life of the
	// connection. Safe from inside the pump, which returns immediately after.
	s.cancel()
	// A no-op for a subscriber that was dropped by the broadcaster, which has
	// already removed and closed it; needed for the ordinary end-of-session
	// case so the attachment does not keep a reference to a dead viewer.
	s.live.Unsubscribe(s.sub)
}

func (c *Conn) unsubscribe(sessionID string) {
	c.mu.Lock()
	s := c.byID[sessionID]
	if s != nil {
		delete(c.byID, sessionID)
		delete(c.streams, s.ref)
	}
	c.mu.Unlock()
	if s != nil {
		s.cancel()
		s.live.Unsubscribe(s.sub)
	}
}

func (c *Conn) closeAll() {
	c.mu.Lock()
	all := make([]*stream, 0, len(c.streams))
	for _, s := range c.streams {
		all = append(all, s)
	}
	c.streams = map[uint32]*stream{}
	c.byID = map[string]*stream{}
	c.mu.Unlock()
	for _, s := range all {
		s.cancel()
		s.live.Unsubscribe(s.sub)
	}
}

// ─── writing ──────────────────────────────────────────────────────────────

func (c *Conn) sendBinary(b []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		_ = c.ws.CloseNow()
	}
}

func (c *Conn) sendJSON(msg ServerMessage) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		_ = c.ws.CloseNow()
	}
}

// sendRaw writes a pre-marshalled JSON payload. Used by the hub, which builds
// one message and sends the same bytes to every connection.
func (c *Conn) sendRaw(payload []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, payload); err != nil {
		_ = c.ws.CloseNow()
	}
}

func (c *Conn) sendError(sessionID, message string) {
	c.sendJSON(ServerMessage{Type: MsgError, SessionID: sessionID, Message: message})
}
