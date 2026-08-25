package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// debugChunks dumps every PTY chunk to stderr when VIBEPANEL_DEBUG_CHUNKS is
// set. Diagnosing "who wrote to this terminal" from the outside is guesswork;
// this makes it an observation.
var debugChunks = os.Getenv("VIBEPANEL_DEBUG_CHUNKS") != ""

func truncateForLog(b []byte) string {
	const max = 120
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

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
	// Empty means nobody owns it right now.
	controller string

	// lastController is who owned it before they went away, and it is what
	// makes "unowned" mean two different things.
	//
	// Empty: nobody has ever driven this session, so the first viewer to
	// arrive should have it. Set: somebody was driving it and their connection
	// ended, so the grid is frozen at their size and only they may pick it up
	// again without asking.
	//
	// Without the distinction, any subscribe during the owner's absence took
	// the grid. Measured: desktop owns 112x34, phone joins passive and
	// correctly does not steal it, desktop's tab closes, and then the phone
	// merely *reloads* — reflowing the agent to 46x34, which is the exact
	// outcome the freeze on unsubscribe exists to prevent. The freeze only ever
	// protected the instant of departure. The returning desktop then found a
	// 46-column view it did not own.
	lastController string

	scanner *oscScanner
	done    chan struct{}
	closed  bool

	// pumped is closed by the pump goroutine as it returns. close() waits on
	// it so that "the attachment has ended" also means "no further callbacks".
	pumped chan struct{}

	// reconfiguredAt is when the terminal was last set up — attached or
	// resized. See settleWindow.
	reconfiguredAt time.Time
}

// settleWindow is how long after reconfiguring the terminal the pump stops
// treating output as activity.
//
// Attaching makes tmux repaint the pane, and so does resizing it. Both are a
// redraw of content that was already there — the terminal being set up, not
// the session doing anything. Counting them as activity has two consequences,
// both bad: every session reads as "working" the moment the panel starts, and
// merely opening a session that was waiting for you clears the state that said
// so, because subscribing resizes the grid.
//
// A quarter of a second is comfortably longer than a repaint and short enough
// that real output is not lost: the worst case is a session that printed in
// that window reading as quiet a couple of seconds early.
const settleWindow = 250 * time.Millisecond

// Signals is what the pump observed in a slice of output. The manager hands it
// to whatever is deciding session state.
type Signals struct {
	SessionID string
	Bell      bool
	Titles    []string
	Bytes     int

	// Visible is true when the chunk contained something a person would see.
	//
	// Separate from Bytes because terminals emit a great deal that is not
	// output: mode changes, cursor moves, the re-initialisation tmux sends
	// after a capability query times out. Counting those as activity makes an
	// idle session look busy and clears the "waiting" state that matters most.
	Visible bool

	// Advanced is true when the chunk moved the screen forward rather than
	// redrawing it where it stood.
	//
	// A line feed, measured on this PTY over three seconds of steady state:
	// a spinner sends 480 bytes and none of them, an agent producing output
	// sends 430 bytes and twenty-two. It is what tells "went back to work"
	// apart from "is redrawing while it waits", which no timer can. See the
	// bell rule in Evaluate.
	Advanced bool
}

// Manager owns every live attachment.
type Manager struct {
	tmux     *tmux.Client
	ringSize int

	mu   sync.RWMutex
	live map[string]*Live

	// attaching records sessions an Attach is currently building, so a second
	// caller waits for the first instead of starting its own. See Attach.
	attaching map[string]chan struct{}

	// detachedWhileAttaching names sessions a Detach asked for while an Attach
	// was still building them. See Attach's install step.
	detachedWhileAttaching map[string]bool

	// OnSignals, if set, is called from the pump goroutine whenever a chunk
	// produced something worth acting on. It must not block.
	OnSignals func(Signals)

	// Log, if set, receives non-fatal problems.
	Log *slog.Logger
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log.Debug(fmt.Sprintf(format, args...))
	}
}

// NewManager returns a manager attaching sessions on the given tmux client.
func NewManager(tm *tmux.Client, ringSize int) *Manager {
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}
	return &Manager{
		tmux:                   tm,
		ringSize:               ringSize,
		live:                   map[string]*Live{},
		attaching:              map[string]chan struct{}{},
		detachedWhileAttaching: map[string]bool{},
	}
}

// Attach starts a PTY running `tmux attach` for a session, if one is not
// already running. Attaching twice is a no-op, which lets callers treat it as
// "make sure this is live".
func (m *Manager) Attach(ctx context.Context, sessionID, tmuxName string, cols, rows int) (*Live, error) {
	// Claim the right to attach this session, or wait for whoever holds it.
	//
	// Checking the map and releasing the lock is not enough. What follows takes
	// a hundred milliseconds or more — capture-pane, then starting a PTY — and
	// two callers can walk through that window together, each starting a tmux
	// client and only one of them ending up in the map. The other client stays
	// attached with nothing owning it.
	//
	// That breaks the assumption the whole size arbitration rests on. With
	// `window-size latest`, the grid follows whichever client resized most
	// recently; a second client the panel has forgotten about turns every
	// resize into a fight and reflows the pane under a running TUI. And the two
	// callers are not hypothetical: the poller attaches every live session
	// while a subscribe attaches on demand.
	for {
		m.mu.Lock()
		if l, ok := m.live[sessionID]; ok {
			m.mu.Unlock()
			return l, nil
		}
		if wait, ok := m.attaching[sessionID]; ok {
			m.mu.Unlock()
			select {
			case <-wait:
				// Whoever held it is done, one way or the other. Round again:
				// either the attachment is in the map now, or it failed and
				// this caller takes its turn.
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		m.attaching[sessionID] = make(chan struct{})
		m.mu.Unlock()
		break
	}
	defer m.releaseAttach(sessionID)

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

	// The ring starts empty, and that is fine: attaching makes tmux repaint the
	// visible screen, and the repaint goes through the pump into the ring like
	// any other output. A viewer subscribing at any point afterwards is filled
	// from it.
	//
	// This used to be primed with `capture-pane -S - -E -1`, the history above
	// the visible screen, on the reasoning that a panel restart empties the
	// ring and the first person to open a session would otherwise see a blank
	// terminal. Both halves were wrong. The repaint already covers the blank
	// terminal. And the history could never be seen: tmux's attach begins with
	// `ESC[?1049h`, so everything after it is drawn on the alternate screen,
	// which by definition has no scrollback. The primed lines went into the
	// normal buffer and were covered a millisecond later.
	//
	// Deleting the priming changed nothing measurable — same rendered screen,
	// same absent scrollback, same green restart check. Scrolling back through
	// a session is tmux's copy-mode, not the browser's scrollbar. Anyone who
	// wants it to be the browser's scrollbar wants
	// `terminal-overrides ',*:smcup@:rmcup@'` in vibepanel.conf, which keeps
	// tmux out of the alternate screen — and inherits its known cost, every
	// full redraw landing in the scrollback as another copy of the screen.
	ring := NewRingBuffer(m.ringSize)

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
		ID:             sessionID,
		TmuxName:       tmuxName,
		ptmx:           ptmx,
		cmd:            cmd,
		ring:           ring,
		subs:           map[*Subscriber]struct{}{},
		cols:           cols,
		rows:           rows,
		scanner:        newOSCScanner(),
		done:           make(chan struct{}),
		pumped:         make(chan struct{}),
		reconfiguredAt: time.Now(),
	}

	// Install, unless somebody asked for this session to go while it was being
	// built.
	//
	// The claim above stops two Attaches racing each other; it does nothing
	// about a Detach arriving in the middle of one. That Detach found nothing
	// in the map — the attachment did not exist yet — and returned, and then
	// this line put a live attachment into a map the caller had just been told
	// was empty. Nothing could close it afterwards, so it stayed attached to
	// tmux for the life of the process: a second client on a session that is
	// supposed to have exactly one, which is the assumption `window-size
	// latest` rests on.
	//
	// Found by attaching and detaching the same names from eight goroutines and
	// then asking tmux what was still connected after DetachAll. Whoever asked
	// for the session to be gone wins.
	m.mu.Lock()
	if m.detachedWhileAttaching[sessionID] {
		delete(m.detachedWhileAttaching, sessionID)
		m.mu.Unlock()
		l.close()
		return nil, ErrNotAttached
	}
	m.live[sessionID] = l
	m.mu.Unlock()

	go m.pump(l)
	return l, nil
}

// releaseAttach hands the claim back and wakes anyone waiting on it.
func (m *Manager) releaseAttach(sessionID string) {
	m.mu.Lock()
	ch := m.attaching[sessionID]
	delete(m.attaching, sessionID)
	// The note only applies to the attempt that was running when it was left.
	// Keeping it would make the *next* Attach throw away a good attachment.
	delete(m.detachedWhileAttaching, sessionID)
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// pump reads the PTY until it ends, feeding the ring, the scanner and every
// viewer.
func (m *Manager) pump(l *Live) {
	defer close(l.pumped)
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

		l.closeFromPump()
		_ = l.cmd.Wait()
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := l.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// Copy before handing to subscribers: buf is reused on the next
			// read, and a viewer's queue may hold the slice past that point.
			out := make([]byte, n)
			copy(out, chunk)

			// The ring and the broadcast happen under one lock, and Subscribe
			// takes its snapshot under the same one.
			//
			// Writing the ring first and broadcasting after left a window: a
			// viewer registering between the two got the chunk in its replay
			// *and* then live, and printed it twice. The comment here used to
			// say that was the deliberate choice, on the grounds that the other
			// order would lose it instead — but the choice was between two
			// kinds of visible corruption, and it was never necessary. Holding
			// the lock across both makes a subscribe either entirely before
			// (snapshot without the chunk, then the broadcast reaches it) or
			// entirely after (snapshot with the chunk, and the broadcast has
			// already been to everyone who was registered).
			var bell bool
			var clips, titles []string
			var cols, rows int
			l.mu.Lock()
			l.ring.Write(chunk)
			l.scanner.feed(chunk)
			bell, clips, titles = l.scanner.drain()
			cols, rows = l.cols, l.rows
			l.broadcastLocked(Event{Kind: EventOutput, Data: out})
			for _, c := range clips {
				l.broadcastLocked(Event{Kind: EventClipboard, Text: c})
			}
			for _, t := range titles {
				l.broadcastLocked(Event{Kind: EventTitle, Text: t})
			}
			l.mu.Unlock()

			// Writing to the PTY stays outside the lock: it is I/O, and the
			// only thing on the other end of it is tmux.
			if reply := terminalQueryReplies(chunk, cols, rows); len(reply) > 0 {
				if debugChunks {
					fmt.Fprintf(os.Stderr, "[reply] %s %s %q\n",
						time.Now().Format("15:04:05.000"), l.TmuxName, reply)
				}
				if _, werr := l.ptmx.Write(reply); werr != nil {
					m.logf("reply to terminal query on %s: %v", l.TmuxName, werr)
				}
			}
			if debugChunks {
				fmt.Fprintf(os.Stderr, "[chunk] %s %s n=%d %q\n",
					time.Now().Format("15:04:05.000"), l.TmuxName, n, truncateForLog(chunk))
			}
			if m.OnSignals != nil {
				// A bell is always reported: it is an event, not a redraw, and
				// tmux does not manufacture one while repainting.
				l.mu.RLock()
				since := time.Since(l.reconfiguredAt)
				l.mu.RUnlock()
				settled := since > settleWindow
				m.OnSignals(Signals{
					SessionID: l.ID, Bell: bell, Titles: titles,
					Bytes:    n,
					Visible:  settled && hasPrintable(chunk),
					Advanced: settled && bytes.Contains(chunk, []byte("\n")),
				})
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

// LiveIDs lists the sessions currently attached, in a stable order.
//
// Sorted because a caller compares the result against the previous one. The
// state snapshot embeds this list, and the poller broadcasts only when the
// serialised snapshot differs from the last one it sent. Ranging over a map
// gives a different order on every call, so that comparison found a difference
// on every tick, and an idle panel with two or more attached sessions pushed a
// full state snapshot to every viewer every two seconds. Nothing looked
// broken — it just quietly stopped being a push and went back to being a poll.
func (m *Manager) LiveIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Paste delivers a block of text to a session as a paste.
//
// Deliberately not Live.Write: writing goes into the PTY as raw bytes, which
// makes a multi-line block arrive as line after line of typing. See
// tmux.Client.Paste for why that has to be tmux's job rather than this
// package's.
func (m *Manager) Paste(ctx context.Context, sessionID, text string) error {
	l, ok := m.Get(sessionID)
	if !ok {
		return ErrNotAttached
	}
	return m.tmux.Paste(ctx, l.TmuxName, text)
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
	} else if _, building := m.attaching[sessionID]; building {
		// Nothing to close yet, but somebody is making one. Leave a note so it
		// throws away what it built instead of installing it.
		m.detachedWhileAttaching[sessionID] = true
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
	// Same as Detach, for every attachment currently being built. This is the
	// shutdown path, so an Attach that installs itself after it has run leaves
	// a tmux client behind with the process that owned it already gone.
	for id := range m.attaching {
		m.detachedWhileAttaching[id] = true
	}
	m.mu.Unlock()

	// In parallel, because each close waits two seconds and they have nothing
	// to do with each other.
	//
	// A tmux client does not exit when its PTY is closed, so close() falls
	// through to the timer that kills it — "give the client a moment to exit on
	// its own" is not the exception here, it is every time. Serially that is
	// two seconds per session: measured 2025ms for one, 8030ms for four,
	// 16033ms for eight.
	//
	// The unit file sets TimeoutStopSec=20 and says "stopping the panel must
	// not wait on anything". At eleven sessions it waits longer than that and
	// systemd SIGKILLs the panel — on a setup built for a couple of dozen
	// agents, which is what this panel is for. The tmux sessions survive that
	// (KillMode=process), but nothing else about a shutdown does: no final
	// state written, and a unit that reports failed because 137 is not the
	// SuccessExitStatus it was told to expect.
	//
	// In parallel it is two seconds whatever the count.
	var wg sync.WaitGroup
	for _, l := range all {
		wg.Add(1)
		go func(l *Live) {
			defer wg.Done()
			l.close()
		}(l)
	}
	wg.Wait()
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

	// A session nobody has ever driven goes to whoever opened it. Without this
	// the first viewer is told it is passive, renders at the stored grid and
	// scales it into a corner of the window — the session never fits the screen
	// it is being looked at on, and the only way out is a "take control" button
	// the user has no reason to think they need.
	//
	// A session whose driver stepped away is different: it stays frozen at
	// their grid until they come back, or until somebody else deliberately
	// takes it. See lastController.
	if l.controller == "" && (l.lastController == "" || l.lastController == sub.ClientID) {
		l.controller = sub.ClientID
		l.lastController = ""
	}
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

	// A viewer leaving gives up control, but the grid is deliberately left
	// unowned rather than handed to whoever else happens to be watching.
	//
	// Most disconnects are temporary — a reload, a phone locking, a flaky
	// network. Handing the grid over on each one means refreshing your own
	// page gives it to the phone glancing at the session from across the room,
	// which immediately reflows a 147-column agent view down to 13 and leaves
	// the returning desktop stuck watching it. Freezing the grid instead keeps
	// it exactly as it was, and the returning viewer reclaims it on subscribe.
	//
	// The cost is that a lone remaining viewer keeps scaling until it taps
	// "take control" once. That is a deliberate, visible action rather than a
	// surprise.
	if l.controller == sub.ClientID {
		l.controller = ""
		l.lastController = sub.ClientID
	}
}

// broadcast delivers an event to every viewer, dropping any that is too far
// behind.
func (l *Live) broadcast(ev Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.broadcastLocked(ev)
}

// broadcastLocked is broadcast for callers already holding l.mu — the pump,
// which has to write the ring and deliver in one critical section.
func (l *Live) broadcastLocked(ev Event) {
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

// Write sends input to the session. It does NOT change who owns the grid.
//
// Input and control are deliberately separate. Two reasons:
//
//  1. Not every byte a terminal sends is a person typing. xterm answers device
//     attribute queries, reports focus changes and acknowledges bracketed
//     paste, all through the same channel as keystrokes. Treating writes as
//     intent meant a viewer claimed the grid merely by loading the page —
//     which is exactly how this was found.
//
//  2. Even for real keystrokes it is the wrong behaviour. Glancing at a
//     session on a phone and answering "y" to a prompt is the mobile use case
//     this panel exists for; resizing the grid to 45 columns underneath the
//     desktop that is mid-edit is not what that person asked for.
//
// So a passive viewer can type freely, and taking the grid is an explicit act.
// See TakeControl.
func (l *Live) Write(clientID string, p []byte) (int, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return 0, ErrNotAttached
	}
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

// Resize sets the grid, if the caller owns it.
//
// A resize from a viewer that does not own the grid is ignored rather than
// rejected: a browser window being dragged is not an error, it just does not
// get to reflow a session somebody else is using. Those viewers scale instead.
func (l *Live) Resize(clientID string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("session: invalid size %dx%d", cols, rows)
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrNotAttached
	}
	// Only the owner may move the grid — including when it is unowned. An
	// unowned grid is one whose owner stepped away; letting the next window
	// resize claim it means a phone rotating in someone's pocket takes over a
	// session the desktop is about to come back to. Claiming happens on
	// subscribe (a viewer arriving) or through TakeControl (a deliberate tap),
	// both of which are things a person did on purpose.
	if l.controller != clientID {
		l.mu.Unlock()
		return nil // not an error: a passive viewer resizing its own window
	}
	if l.cols == cols && l.rows == rows {
		l.mu.Unlock()
		return nil
	}
	l.cols, l.rows = cols, rows
	// A resize makes tmux repaint the pane. That redraw is not the session
	// producing output, and treating it as such clears the very state the user
	// opened the session to act on.
	l.reconfiguredAt = time.Now()

	// The ioctl happens with the lock still held, and close() closes the PTY
	// with it held too.
	//
	// Every other user of l.ptmx copies the pointer, releases the lock and then
	// does its I/O — right for Write, whose write can block and must not stall
	// the pump or the other viewers, and safe there because os.File refcounts
	// reads and writes against Close. It is not safe here: pty.Setsize needs
	// the raw descriptor, and File.Fd() is valid only until Close is called.
	// A resize arriving as a session detaches therefore raced the descriptor
	// being destroyed, and a recycled fd would have taken the ioctl.
	//
	// Holding the lock across an ioctl costs microseconds and cannot block;
	// holding it across a write could block forever, which is why that one is
	// still outside.
	err := pty.Setsize(l.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err == nil {
		l.broadcastLocked(Event{Kind: EventSize, Cols: cols, Rows: rows})
	}
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("session: resize: %w", err)
	}
	return nil
}

// TakeControl transfers grid ownership to a viewer, then applies its size.
func (l *Live) TakeControl(clientID string, cols, rows int) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrNotAttached
	}
	moved := l.controller != clientID
	l.controller = clientID
	sameSize := l.cols == cols && l.rows == rows
	l.mu.Unlock()

	if err := l.Resize(clientID, cols, rows); err != nil {
		return err
	}

	// Ownership can move without the grid moving — two windows the same size,
	// or one that has been through a layout change and back. Resize has nothing
	// to broadcast then, and EventSize is the only thing that tells a viewer
	// whether it is the controller, so without this nobody learns anything:
	// the new owner's interface still offers to take a grid it already holds,
	// and the previous owner goes on believing its window drives the session
	// while its resizes are silently ignored.
	if moved && sameSize {
		cols, rows = l.Size()
		l.broadcast(Event{Kind: EventSize, Cols: cols, Rows: rows})
	}
	return nil
}

func (l *Live) awaitPump() {
	select {
	case <-l.pumped:
	case <-time.After(pumpDrain):
	}
}

// Done is closed once the attachment has ended.
func (l *Live) Done() <-chan struct{} { return l.done }

// close tears the attachment down exactly once.
// pumpDrain bounds how long close() waits for the pump goroutine.
//
// Closing the PTY unblocks its pending read, so the wait should be short. It
// was not: the pump's cleanup called close(), which waited on the channel that
// same goroutine closes afterwards, so every teardown ran this bound to the
// end. See closeFromPump.
//
// The bound stays, for what it was written for: shutdown detaches every
// session, and one PTY whose read somehow does not unblock should cost that
// session its final chunk rather than hang the process.
const pumpDrain = 2 * time.Second

func (l *Live) close() { l.closeInternal(true) }

// closeFromPump is close() as called by the pump's own goroutine.
//
// It must not wait for the pump, because it is the pump. The pump's deferred
// cleanup called close(), close() waited on l.pumped, and l.pumped is closed by
// a later defer in that same goroutine — so it waited for itself and was
// released only by the pumpDrain timeout.
//
// Two seconds, on every single attachment teardown. It looked like the killer
// timer below, which is also two seconds, so the arithmetic came out right
// against the wrong cause: detaching one session measured 2015ms, deleting a
// project with five 10029ms, and shutting down with eight sessions 16033ms.
// The comment on pumpDrain said the wait was "immediate in practice"; it was
// never once immediate.
func (l *Live) closeFromPump() { l.closeInternal(false) }

func (l *Live) closeInternal(waitForPump bool) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		// Already closing elsewhere. Wait anyway: every caller is entitled to
		// the same guarantee, not just whichever one got here first.
		if waitForPump {
			l.awaitPump()
		}
		return
	}
	l.closed = true
	for sub := range l.subs {
		delete(l.subs, sub)
		close(sub.Events)
	}
	// Closed under the lock, so a resize cannot be part way through an ioctl
	// on this descriptor. See Resize.
	_ = l.ptmx.Close()
	l.mu.Unlock()

	close(l.done)

	// Wait for the pump to return before reporting the attachment ended.
	//
	// Without this, a chunk read just before the close is still in flight and
	// reaches OnSignals afterwards. The detector then rebuilds a tracker for a
	// session the caller has already deleted, and holds it for the life of the
	// process — and output is broadcast for a session nothing can reach any
	// more. Callers are entitled to "no more callbacks after Detach returns";
	// this is what makes that true.
	if waitForPump {
		l.awaitPump()
	}

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
