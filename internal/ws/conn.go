package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	mu      sync.Mutex
	streams map[uint32]*stream
	byID    map[string]*stream
	nextRef uint32
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

	conn := &Conn{
		h:        h,
		ws:       c,
		clientID: id.New(),
		streams:  map[uint32]*stream{},
		byID:     map[string]*stream{},
	}
	defer conn.closeAll()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go conn.keepalive(ctx)

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

// readLoop handles messages from the browser.
func (c *Conn) readLoop(ctx context.Context) error {
	for {
		typ, data, err := c.ws.Read(ctx)
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
	// Writing marks this viewer as the controller; see session.Live.Write.
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
				if s.sub.Dropped() {
					// Tell the client why it went quiet so it can resubscribe
					// and replay, rather than sitting on a dead terminal.
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

func (c *Conn) sendError(sessionID, message string) {
	c.sendJSON(ServerMessage{Type: MsgError, SessionID: sessionID, Message: message})
}
