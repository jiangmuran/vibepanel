package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// ErrNotAttached means the session has no live PTY attachment.
var ErrNotAttached = errors.New("session: not attached")

// subscriberQueue is how many events a viewer may fall behind before it is
// dropped.
//
// Dropping rather than blocking is deliberate: the pump that reads the PTY
// feeds every viewer, so one browser on a bad connection must never be able to
// stall it — that would stall the agent behind it. A dropped viewer reconnects
// and replays from the ring buffer, which is exactly what the ring is for.
const subscriberQueue = 256

// Subscriber is one viewer of one session.
type Subscriber struct {
	Events chan Event

	// ClientID identifies the browser connection, so the manager can tell
	// whether an incoming resize comes from the viewer that owns the grid.
	ClientID string

	dropped atomic.Bool
}

// Dropped reports whether this subscriber fell too far behind and was cut off.
func (s *Subscriber) Dropped() bool { return s.dropped.Load() }

// Live is an attached session: a PTY running `tmux attach`, a replay buffer and
// the viewers watching it.
type Live struct {
	ID       string
	TmuxName string

	mu   sync.RWMutex
	ptmx *os.File
	cmd  *exec.Cmd
	ring *RingBuffer
	subs map[*Subscriber]struct{}

	cols, rows int

	// controller is the ClientID whose viewport currently defines the grid.
	// Empty means the next viewer to interact takes it.
	controller string

	scanner *oscScanner
	done    chan struct{}
	closed  bool
}

// Signals is what the pump observed in a slice of output. The manager hands it
// to whatever is deciding session state.
type Signals struct {
	SessionID string
	Bell      bool
	Titles    []string
	Bytes     int
}

// Manager owns every live attachment.
type Manager struct {
	tmux     *tmux.Client
	ringSize int

	mu   sync.RWMutex
	live map[string]*Live

	// OnSignals, if set, is called from the pump goroutine whenever a chunk
	// produced something worth acting on. It must not block.
	OnSignals func(Signals)
}

// NewManager returns a manager attaching sessions on the given tmux client.
func NewManager(tm *tmux.Client, ringSize int) *Manager {
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}
	return &Manager{tmux: tm, ringSize: ringSize, live: map[string]*Live{}}
}

// Attach starts a PTY running `tmux attach` for a session, if one is not
// already running. Attaching twice is a no-op, which lets callers treat it as
// "make sure this is live".
func (m *Manager) Attach(ctx context.Context, sessionID, tmuxName string, cols, rows int) (*Live, error) {
	m.mu.Lock()
	if l, ok := m.live[sessionID]; ok {
		m.mu.Unlock()
		return l, nil
	}
	m.mu.Unlock()

	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}

	exists, err := m.tmux.Has(ctx, tmuxName)
	if err != nil {
		return nil, fmt.Errorf("session: check %s: %w", tmuxName, err)
	}
	if !exists {
		return nil, fmt.Errorf("session: tmux session %s does not exist", tmuxName)
	}

	// A plain `attach`, not `attach -d`: detaching other clients would be
	// pointless (we are the only one) and actively harmful if a human is
	// debugging the same socket from a shell.
	args := append([]string{}, m.tmux.AttachArgs(tmuxName)...)
	cmd := exec.Command(m.tmux.Bin, args...)

	// TERM describes what tmux may send *us*. xterm.js is what actually renders
	// it, and it speaks xterm-256color; combined with the config's
	// `terminal-features ",*:RGB"` this is what allows 24-bit colour through.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("session: start pty for %s: %w", tmuxName, err)
	}

	l := &Live{
		ID:       sessionID,
		TmuxName: tmuxName,
		ptmx:     ptmx,
		cmd:      cmd,
		ring:     NewRingBuffer(m.ringSize),
		subs:     map[*Subscriber]struct{}{},
		cols:     cols,
		rows:     rows,
		scanner:  newOSCScanner(),
		done:     make(chan struct{}),
	}

	m.mu.Lock()
	m.live[sessionID] = l
	m.mu.Unlock()

	go m.pump(l)
	return l, nil
}

// pump reads the PTY until it ends, feeding the ring, the scanner and every
// viewer.
func (m *Manager) pump(l *Live) {
	defer func() {
		l.broadcast(Event{Kind: EventExit})

		// Deregister before closing, so that Done() firing is a promise the
		// manager has already forgotten this session. The other order leaves a
		// window where a caller woken by Done() still sees it listed as live —
		// which is exactly long enough for a reconnect to attach to a corpse.
		m.mu.Lock()
		if m.live[l.ID] == l {
			delete(m.live, l.ID)
		}
		m.mu.Unlock()

		l.close()
		_ = l.cmd.Wait()
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := l.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// The ring is written before the broadcast so that a viewer
			// connecting at this instant either sees the chunk in its replay or
			// receives it live — never neither.
			l.mu.RLock()
			ring, scanner := l.ring, l.scanner
			l.mu.RUnlock()
			ring.Write(chunk)

			l.mu.Lock()
			scanner.feed(chunk)
			bell, clips, titles := scanner.drain()
			l.mu.Unlock()

			// Copy before handing to subscribers: buf is reused on the next
			// read, and a viewer's queue may hold the slice past that point.
			out := make([]byte, n)
			copy(out, chunk)
			l.broadcast(Event{Kind: EventOutput, Data: out})

			for _, c := range clips {
				l.broadcast(Event{Kind: EventClipboard, Text: c})
			}
			for _, t := range titles {
				l.broadcast(Event{Kind: EventTitle, Text: t})
			}
			if m.OnSignals != nil {
				m.OnSignals(Signals{SessionID: l.ID, Bell: bell, Titles: titles, Bytes: n})
			}
		}
		if err != nil {
			// EIO on a closed PTY is the normal way this ends: the tmux client
			// exited because the session was killed or the panel detached.
			return
		}
	}
}

// Get returns the live attachment for a session, if any.
func (m *Manager) Get(sessionID string) (*Live, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.live[sessionID]
	return l, ok
}

// LiveIDs lists the sessions currently attached.
func (m *Manager) LiveIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	return ids
}

// Detach ends the attachment without touching the tmux session.
//
// This is what shutdown calls. The distinction from Kill is the whole point of
// the architecture: detaching leaves every process running.
func (m *Manager) Detach(sessionID string) {
	m.mu.Lock()
	l, ok := m.live[sessionID]
	if ok {
		delete(m.live, sessionID)
	}
	m.mu.Unlock()
	if ok {
		l.close()
	}
}

// DetachAll ends every attachment. Sessions keep running.
func (m *Manager) DetachAll() {
	m.mu.Lock()
	all := make([]*Live, 0, len(m.live))
	for _, l := range m.live {
		all = append(all, l)
	}
	m.live = map[string]*Live{}
	m.mu.Unlock()
	for _, l := range all {
		l.close()
	}
}

// ─── Live ─────────────────────────────────────────────────────────────────

// Subscribe registers a viewer and returns it along with the replay buffer it
// should render before processing live events.
//
// The snapshot is taken under the same lock that registers the subscriber, so
// no output can slip between the two and be lost.
func (l *Live) Subscribe(clientID string) (*Subscriber, []byte) {
	sub := &Subscriber{Events: make(chan Event, subscriberQueue), ClientID: clientID}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		close(sub.Events)
		return sub, nil
	}
	replay := l.ring.Snapshot()
	l.subs[sub] = struct{}{}
	cols, rows := l.cols, l.rows

	// Tell the new viewer the authoritative grid immediately; without it a
	// passive viewer does not know what to scale to and renders at its own
	// size for one frame.
	select {
	case sub.Events <- Event{Kind: EventSize, Cols: cols, Rows: rows}:
	default:
	}
	return sub, replay
}

// Unsubscribe removes a viewer.
func (l *Live) Unsubscribe(sub *Subscriber) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.subs[sub]; !ok {
		return
	}
	delete(l.subs, sub)
	close(sub.Events)

	// A viewer leaving gives up control, so the next one to interact can size
	// the session without having to ask.
	if l.controller == sub.ClientID {
		l.controller = ""
	}
}

// broadcast delivers an event to every viewer, dropping any that is too far
// behind.
func (l *Live) broadcast(ev Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for sub := range l.subs {
		select {
		case sub.Events <- ev:
		default:
			// Full queue: this viewer cannot keep up. Cut it loose rather than
			// slowing the pump; it will reconnect and replay.
			sub.dropped.Store(true)
			delete(l.subs, sub)
			close(sub.Events)
		}
	}
}

// Write sends input to the session and makes the sender the controller.
//
// Typing is the clearest possible statement of "I am the one using this", so it
// transfers control without a separate gesture. On a phone, starting to type is
// exactly when the grid should become phone-sized.
func (l *Live) Write(clientID string, p []byte) (int, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return 0, ErrNotAttached
	}
	l.controller = clientID
	ptmx := l.ptmx
	l.mu.Unlock()
	return ptmx.Write(p)
}

// Size returns the authoritative grid.
func (l *Live) Size() (cols, rows int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cols, l.rows
}

// Controller returns the client that currently owns the grid size.
func (l *Live) Controller() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.controller
}

// Resize sets the grid, if the caller is allowed to.
//
// Only the controlling viewer may resize. Without that rule, a phone opening a
// session that a desktop is mid-edit in would reflow the agent's TUI down to 45
// columns — the failure this whole arbitration exists to prevent. Passive
// viewers scale instead, and take control explicitly with TakeControl.
func (l *Live) Resize(clientID string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("session: invalid size %dx%d", cols, rows)
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrNotAttached
	}
	if l.controller != "" && l.controller != clientID {
		l.mu.Unlock()
		return nil // not an error: a passive viewer resizing its own window
	}
	l.controller = clientID
	if l.cols == cols && l.rows == rows {
		l.mu.Unlock()
		return nil
	}
	l.cols, l.rows = cols, rows
	ptmx := l.ptmx
	l.mu.Unlock()

	// Resizing our PTY is enough: tmux sizes the window to its most recently
	// active client, and we are the only client it has.
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("session: resize: %w", err)
	}
	l.broadcast(Event{Kind: EventSize, Cols: cols, Rows: rows})
	return nil
}

// TakeControl transfers grid ownership to a viewer, then applies its size.
func (l *Live) TakeControl(clientID string, cols, rows int) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrNotAttached
	}
	l.controller = clientID
	l.mu.Unlock()
	return l.Resize(clientID, cols, rows)
}

// Replay returns the current replay buffer without subscribing.
func (l *Live) Replay() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ring.Snapshot()
}

// Subscribers reports how many viewers are attached.
func (l *Live) Subscribers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.subs)
}

// Done is closed once the attachment has ended.
func (l *Live) Done() <-chan struct{} { return l.done }

// close tears the attachment down exactly once.
func (l *Live) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	for sub := range l.subs {
		delete(l.subs, sub)
		close(sub.Events)
	}
	ptmx := l.ptmx
	l.mu.Unlock()

	_ = ptmx.Close()
	close(l.done)

	// Give the tmux client a moment to exit on its own after its PTY closed.
	// Killing it immediately is usually unnecessary and occasionally races with
	// tmux writing its final state.
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		<-timer.C
		if l.cmd.Process != nil {
			_ = l.cmd.Process.Kill()
		}
	}()
}
