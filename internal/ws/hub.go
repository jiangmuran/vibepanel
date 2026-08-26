package ws

import (
	"sync"
	"time"
)

// Hub tracks every open connection so state changes can be pushed to all of
// them.
//
// The panel's whole premise is that the page is a view, not the state: open it
// in three places and they agree. Polling gave that a two-second lag and a
// steady trickle of requests from every idle tab; pushing gives it immediately
// and costs nothing while nothing is happening.
type Hub struct {
	mu    sync.RWMutex
	conns map[*Conn]struct{}

	// coalesce collapses bursts of changes into one message. Creating a
	// session touches several rows, and the tmux poller can mark a dozen
	// sessions in the same tick; without this each one would be its own
	// full-state broadcast to every viewer.
	coalesceMu sync.Mutex
	pending    func() []byte
	timer      *time.Timer
}

// coalesceWindow is how long a change waits for company. Short enough to feel
// immediate, long enough to absorb a burst.
const coalesceWindow = 60 * time.Millisecond

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{conns: map[*Conn]struct{}{}} }

func (h *Hub) add(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

// Connections reports how many viewers are attached. Used by the settings page
// and by tests.
func (h *Hub) Connections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Broadcast sends a raw JSON payload to every connection immediately.
// BroadcastEvent sends a message that is not a snapshot to every connection.
//
// Separate from Broadcast because the two are queued differently: a snapshot
// replaces the one waiting, an event joins a queue beside it. Sending an event
// through Broadcast drops it whenever a snapshot arrives first, and the poller
// produces one every two seconds.
func (h *Hub) BroadcastEvent(payload []byte) {
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.queueEvent(payload)
	}
}

func (h *Hub) Broadcast(payload []byte) {
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	// queueState, not sendRaw: this loop is serial, and a viewer whose socket
	// cannot take another byte must not be able to hold every other viewer's
	// state update behind it. See queueState.
	for _, c := range conns {
		c.queueState(payload)
	}
}

// Notify schedules a broadcast of whatever build returns, coalescing with any
// other Notify inside the window.
//
// build is called once, at send time, rather than at each Notify — so a burst
// of ten changes produces one message carrying the state after all ten, not
// ten messages carrying ten intermediate states.
func (h *Hub) Notify(build func() []byte) {
	h.coalesceMu.Lock()
	defer h.coalesceMu.Unlock()

	h.pending = build
	if h.timer != nil {
		return // a flush is already scheduled
	}
	h.timer = time.AfterFunc(coalesceWindow, func() {
		h.coalesceMu.Lock()
		fn := h.pending
		h.pending = nil
		h.timer = nil
		h.coalesceMu.Unlock()
		if fn == nil {
			return
		}
		if payload := fn(); payload != nil {
			h.Broadcast(payload)
		}
	})
}
