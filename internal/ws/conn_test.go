package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// stubResolver stands in for the store: nothing here subscribes to a session.
type stubResolver struct{}

func (stubResolver) Resolve(context.Context, string) (string, int, int, error) {
	return "", 0, 0, context.Canceled
}
func (stubResolver) RecordSize(context.Context, string, int, int) error { return nil }

// A connection does not outlive the session that opened it.
//
// Authorisation happens once, at the handshake, and then the socket lives for
// hours. Signing out, a session expiring, an administrator revoking one, and
// changing the password — which deletes every other browser's session for the
// express purpose of cutting off whoever had the old one — all left the
// terminals those browsers already had open streaming, and still accepting
// keystrokes. Measured before this existed: after the session row was deleted,
// typing still reached the shell.
func TestAConnectionClosesWhenItsSessionStopsBeingValid(t *testing.T) {
	valid := atomic.Bool{}
	valid.Store(true)

	h := &Handler{
		Manager:         session.NewManager(nil, 1<<10),
		Resolve:         stubResolver{},
		RevalidateEvery: 50 * time.Millisecond,
		StillAuthorized: func(*http.Request) bool { return valid.Load() },
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// One read, in a goroutine, never cancelled.
	//
	// The first version of this checked liveness by reading with a short
	// context and expecting a deadline error. In coder/websocket a read whose
	// context expires *closes the connection* — so the liveness check killed
	// the thing it was checking, every later read returned an error, and the
	// test passed just as happily with the revalidation removed. It was
	// measuring its own destruction.
	read := make(chan error, 1)
	go func() {
		_, _, rerr := c.Read(ctx)
		read <- rerr
	}()

	select {
	case rerr := <-read:
		t.Fatalf("the connection ended while its session was still valid: %v", rerr)
	case <-time.After(400 * time.Millisecond):
		// Several revalidation intervals with nothing happening: correct.
	}

	valid.Store(false)

	select {
	case rerr := <-read:
		if errors.Is(rerr, context.DeadlineExceeded) {
			t.Fatalf("the test's own deadline expired rather than the connection closing")
		}
		return // closed, which is the point
	case <-time.After(5 * time.Second):
	}
	t.Error("the socket was still open five seconds after its session stopped being valid; a " +
		"signed-out browser keeps its terminals")
}

// Browsers arriving and leaving while state is being broadcast.
//
// The hub keeps a set of connections and coalesces broadcasts behind a timer.
// A set mutated from HTTP handlers, a timer firing from its own goroutine, and
// sockets closing whenever a laptop lid does — the three of them meet in
// Broadcast, and nothing had ever run them together.
func TestConcurrentHubTraffic(t *testing.T) {
	hub := NewHub()
	h := &Handler{
		Manager: session.NewManager(nil, 1<<10),
		Resolve: stubResolver{},
		Hub:     hub,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	stop := make(chan struct{})
	time.AfterFunc(900*time.Millisecond, func() { close(stop) })
	var wg sync.WaitGroup

	// Clients that connect, read a little and go away again.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				c, _, err := websocket.Dial(ctx, url, nil)
				if err != nil {
					cancel()
					continue
				}
				read := make(chan struct{})
				go func() {
					defer close(read)
					for {
						if _, _, rerr := c.Read(ctx); rerr != nil {
							return
						}
					}
				}()
				time.Sleep(20 * time.Millisecond)
				c.CloseNow()
				<-read
				cancel()
			}
		}()
	}

	// And a panel broadcasting state the whole time, from two directions.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload := []byte(`{"t":"state","sessions":[]}`)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if n == 0 {
					hub.Broadcast(payload)
				} else {
					hub.Notify(func() []byte { return payload })
				}
				_ = hub.Connections()
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("hub traffic did not settle; something is deadlocked")
	}
}

// TestAStalledViewerDoesNotHoldUpTheOthers pins the reason Broadcast queues
// instead of writing.
//
// Holding a connection's writeMu is what a closed TCP window looks like from
// the hub's side: that connection's own pump goroutine is inside a write that
// will not finish for up to writeTimeout, and sendRaw's first act is to wait
// for that mutex — with no timeout on the wait. Broadcasting through it made
// every other viewer wait too.
func TestAStalledViewerDoesNotHoldUpTheOthers(t *testing.T) {
	hub := NewHub()
	h := &Handler{
		Manager: session.NewManager(nil, 1<<10),
		Resolve: stubResolver{},
		Hub:     hub,
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < 2; i++ {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer c.CloseNow()
		// Keep reading, so nothing is stalled for an accidental reason.
		go func() {
			for {
				if _, _, rerr := c.Read(ctx); rerr != nil {
					return
				}
			}
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for hub.Connections() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 2 connections registered", hub.Connections())
		}
		time.Sleep(10 * time.Millisecond)
	}

	var victim *Conn
	hub.mu.RLock()
	for c := range hub.conns {
		victim = c
		break
	}
	hub.mu.RUnlock()
	if victim == nil {
		t.Fatal("no connection to stall")
	}

	victim.writeMu.Lock()
	defer victim.writeMu.Unlock()

	done := make(chan struct{})
	go func() {
		hub.Broadcast([]byte(`{"t":"state","projects":[],"sessions":[],"live":[]}`))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast is still blocked on a viewer whose socket will not " +
			"take a write; every other viewer is waiting behind it")
	}
}

// A type this server does not know is answered, not ignored.
//
// The other half of a drift that ran three hops. The client's switch had no
// case for `error` and no default, so all six senders of that frame were
// dropped on the floor — you type into a terminal, the write fails
// server-side, the server says so, and nothing reaches the screen. That end is
// now pinned by the compiler: the switch has a case for every member of the
// union and a `never` default, so adding a member without handling it stops
// the build.
//
// This end had the mirror shape. Every ClientMessage type had a case and there
// was no default, so a stale or misspelled type from a client looked exactly
// like one being handled. Go offers no exhaustiveness check for that, and the
// answer is only worth sending because the other end finally shows it.
func TestAnUnknownMessageTypeIsAnsweredNotIgnored(t *testing.T) {
	h := &Handler{
		Manager:         session.NewManager(nil, 1<<10),
		Resolve:         stubResolver{},
		StillAuthorized: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Short per-read deadline, so that "nothing answered" fails as that
	// sentence rather than as the test's own ten-second timeout. Expiring a
	// read context closes the connection in coder/websocket -- the comment on
	// the test above is about being bitten by exactly that -- which is fine
	// only because every path out of here is a Fatalf.
	read := func(what string) ServerMessage {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
		defer rcancel()
		_, data, rerr := c.Read(rctx)
		if rerr != nil {
			if rctx.Err() != nil {
				t.Fatalf("nothing answered %s within three seconds: the server "+
					"ignored it silently, which is indistinguishable to a client "+
					"from having handled it", what)
			}
			t.Fatalf("read after %s: %v", what, rerr)
		}
		var msg ServerMessage
		if uerr := json.Unmarshal(data, &msg); uerr != nil {
			t.Fatalf("unmarshal %q: %v", data, uerr)
		}
		return msg
	}

	// Positive control. Without it a connection that answers nothing at all
	// would look like one correctly ignoring the unknown type below.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	if msg := read("ping"); msg.Type != MsgPong {
		t.Fatalf("a healthy connection did not answer ping with pong (%q), so this test proves nothing", msg.Type)
	}

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"t":"nudge","sessionId":"s1"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	msg := read("an unknown message type")
	if msg.Type != MsgError {
		t.Fatalf("an unknown message type was answered with %q, want %q; "+
			"a client sending a type this server does not know cannot tell that "+
			"from one it handles", msg.Type, MsgError)
	}
	if !strings.Contains(msg.Message, "nudge") {
		t.Errorf("the error does not name the type that caused it: %q", msg.Message)
	}
	if msg.SessionID != "s1" {
		t.Errorf("sessionId = %q, want it echoed so the viewer knows which terminal", msg.SessionID)
	}

	// The type arrives from the client, is unbounded, and comes back to be
	// rendered. React escapes it, so the hazard is length: without the cap a
	// megabyte of JSON returns as a banner.
	long := strings.Repeat("x", 5000)
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"t":"`+long+`"}`)); err != nil {
		t.Fatalf("write long: %v", err)
	}
	if msg := read("an overlong message type"); len(msg.Message) > 100 {
		t.Errorf("a %d-byte message type came back in a %d-byte error; it is echoed into the UI",
			len(long), len(msg.Message))
	}
}

// An event queued before a snapshot must survive it.
//
// The connection had one slot, `statePending`, and Broadcast put everything in
// it. That is right for a snapshot -- it is absolute, so the newest one
// contains every older one and replacing is free -- and silently wrong for a
// panel notification, which says "your notes for this project changed, fetch
// them again" and carries nothing. Replacing it drops the only copy.
//
// Not a narrow race: the poller broadcasts a snapshot every two seconds, and
// the window is however long the writer spends inside a network write. The
// symptom is a note saved in one browser not appearing in another until
// something unrelated woke it, which reads as the sync being flaky rather than
// as a message being dropped.
//
// Tested through takePending rather than through a socket because that is where
// the property lives. What a stalled writer does is already measured elsewhere.
func TestAnEventIsNotDroppedByASnapshot(t *testing.T) {
	c := &Conn{stateWake: make(chan struct{}, 1)}

	notes := []byte(`{"t":"panel","projectId":"p1","kind":"notes"}`)
	todos := []byte(`{"t":"panel","projectId":"p1","kind":"todos"}`)

	c.queueEvent(notes)
	c.queueState([]byte(`{"t":"state","n":1}`))
	c.queueEvent(todos)
	// The same notification twice is one notification: it carries no content,
	// so a second copy tells the viewer nothing the first did not.
	c.queueEvent(notes)
	c.queueState([]byte(`{"t":"state","n":2}`))

	events, snapshot := c.takePending()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (deduplicated): %q", len(events), events)
	}
	if string(events[0]) != string(notes) || string(events[1]) != string(todos) {
		t.Errorf("events came back as %q, want notes then todos", events)
	}
	// The snapshot slot must still coalesce. Turning it into a queue would give
	// back the behaviour it exists to avoid: one viewer whose socket cannot
	// take another byte held every other viewer's update behind it, measured at
	// 2.2 seconds each.
	if string(snapshot) != `{"t":"state","n":2}` {
		t.Errorf("snapshot is %q, want only the newest", snapshot)
	}

	events, snapshot = c.takePending()
	if len(events) != 0 || snapshot != nil {
		t.Errorf("a second drain returned %q and %q; the first did not clear", events, snapshot)
	}

	// And the dedup must not outlive the drain, or a note edited, delivered,
	// and edited again would announce itself once.
	c.queueEvent(notes)
	if events, _ = c.takePending(); len(events) != 1 {
		t.Errorf("the same event after a drain came back %d times, want 1", len(events))
	}
}

// A big paste fails as a paste, and never as the whole page.
//
// One socket carries every terminal, the state stream and the notifications,
// and coder/websocket answers a message over its read limit by closing the
// connection. At the library default of 32 KiB that made an ordinary paste --
// a build log, a diff, a file dump into an agent -- fatal to the page:
// measured at 32769 bytes in one frame, every terminal reset and replayed its
// ring, and the pasted text never reached the pty.
//
// Two halves, and the second is the one with no library behind it: a message
// that really is too big has to be refused *and* drained, or the next read
// starts in the middle of a frame and the connection dies anyway, one message
// later and from a distance.
func TestAnOversizedMessageCostsThatMessageAndNotTheSocket(t *testing.T) {
	h := &Handler{
		Manager:         session.NewManager(nil, 1<<10),
		Resolve:         stubResolver{},
		StillAuthorized: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Short per-read deadline, so that a socket that has been closed under us
	// fails as that sentence rather than as the test's own timeout.
	read := func(what string) ServerMessage {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		defer rcancel()
		_, data, rerr := c.Read(rctx)
		if rerr != nil {
			t.Fatalf("the connection did not answer after %s: %v", what, rerr)
		}
		var msg ServerMessage
		if uerr := json.Unmarshal(data, &msg); uerr != nil {
			t.Fatalf("unmarshal %q: %v", data, uerr)
		}
		return msg
	}
	alive := func(what string) {
		t.Helper()
		if werr := c.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`)); werr != nil {
			t.Fatalf("write ping after %s: %v", what, werr)
		}
		if msg := read(what); msg.Type != MsgPong {
			t.Fatalf("after %s the connection answered ping with %q, not pong", what, msg.Type)
		}
	}

	// Positive control. Without it, a connection that answers nothing at all
	// would look like one surviving everything below.
	alive("a healthy start")

	// A real paste: 40 KB is a short build log, and it was fatal.
	if werr := c.Write(ctx, websocket.MessageBinary,
		EncodeData(1, bytes.Repeat([]byte("x"), 40<<10))); werr != nil {
		t.Fatalf("write a 40KB frame: %v", werr)
	}
	alive("a 40KB paste")

	// Past the bound. Refused, said out loud, and drained -- and the ping
	// after it is what proves the drain, because an undrained message leaves
	// the next read starting mid-frame.
	if werr := c.Write(ctx, websocket.MessageBinary,
		EncodeData(1, bytes.Repeat([]byte("x"), maxInboundMessage+1))); werr != nil {
		t.Fatalf("write an oversized frame: %v", werr)
	}
	if msg := read("an oversized frame"); msg.Type != MsgError {
		t.Errorf("an oversized message was answered with %q, not an error; it vanished silently", msg.Type)
	}
	alive("an oversized frame")
}
