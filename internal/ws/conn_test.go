package ws

import (
	"context"
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
