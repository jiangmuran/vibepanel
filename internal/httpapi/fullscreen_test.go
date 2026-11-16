package httpapi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// A full-screen program starting and ending in a watched pane reaches the
// flag in well under the poller's two seconds.
//
// The flag decides who owns the wheel in the browser, and the window this
// closes is the one after `less` exits, when the wheel still sent arrows and a
// shell put its previous command on the prompt line. The poller is not running
// here at all -- the longest poll interval there is -- so the probe that output
// schedules is the only thing that can move the flag, and taking it out is a
// red test rather than a slower green one.
//
// The second session is the bound: output on a session nobody is looking at
// asks tmux nothing.
func TestAFullScreenProgramReachesTheFlagWithoutWaitingForThePoll(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	shell := `{"projectId":"` + project.ID + `","command":["sh","-c","PS1=; export PS1; exec sh"]}`
	watched := postJSON[store.Session](t, ts, "/api/sessions", shell)
	unwatched := postJSON[store.Session](t, ts, "/api/sessions", shell)

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", wsDialOptions(t, ts))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	b, _ := json.Marshal(ws.ClientMessage{Type: ws.MsgSubscribe, SessionID: watched.ID, Cols: 80, Rows: 24})
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Read everything, so the viewer is never the one that fell behind.
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()
	live, ok := srv.Manager.Get(watched.ID)
	for deadline := time.Now().Add(10 * time.Second); !ok || !live.Watched(); {
		if time.Now().After(deadline) {
			t.Fatal("the viewer never subscribed")
		}
		time.Sleep(20 * time.Millisecond)
		live, ok = srv.Manager.Get(watched.ID)
	}

	full := func(id string) bool { return slices.Contains(fullscreenNow(&srv.fullscreen), id) }
	within := func(what, id string, want bool) {
		t.Helper()
		start := time.Now()
		for !(full(id) == want) {
			if time.Since(start) > 500*time.Millisecond {
				t.Fatalf("%s: the flag was still %v after 500ms; the poller's two seconds are the only way it moves", what, !want)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Logf("%s: %v", what, time.Since(start).Round(time.Millisecond))
	}

	keys := func(name, line string) {
		t.Helper()
		if err := srv.Tmux.Keys(ctx, name, line, "Enter"); err != nil {
			t.Fatalf("send-keys: %v", err)
		}
	}
	keys(watched.TmuxName, `printf '\033[?1049hFULL'; read x; printf '\033[?1049l'`)
	within("entering the alternate screen", watched.ID, true)
	keys(watched.TmuxName, "")
	within("leaving it", watched.ID, false)

	// A program printing without pause is a burst that never ends. It is
	// asked about at most every fsProbeGap, not once per chunk.
	srv.fsMu.Lock()
	before := srv.fsAsked
	srv.fsMu.Unlock()
	keys(watched.TmuxName, `i=0; while [ $i -lt 300 ]; do echo tick $i; i=$((i+1)); sleep 0.005; done`)
	time.Sleep(1500 * time.Millisecond)
	srv.fsMu.Lock()
	asked := srv.fsAsked - before
	srv.fsMu.Unlock()
	if limit := int(1500*time.Millisecond/fsProbeGap) + 1; asked > limit || asked == 0 {
		t.Errorf("1.5s of steady output asked tmux %d times; want between 1 and %d", asked, limit)
	}

	keys(unwatched.TmuxName, `printf '\033[?1049hFULL'; sleep 30`)
	time.Sleep(700 * time.Millisecond)
	srv.fsMu.Lock()
	_, probed := srv.fsProbe[unwatched.ID]
	srv.fsMu.Unlock()
	if probed || full(unwatched.ID) {
		t.Errorf("a session nobody is watching was asked about (probed %v, flagged %v)", probed, full(unwatched.ID))
	}
}

// The poller's answer and a probe's are merged by when each asked tmux, so a
// poll that listed the panes just before `vim` exited cannot put the flag back
// for two seconds.
func TestTheNewerAnswerAboutAPaneWins(t *testing.T) {
	s := &Server{}
	listed := time.Now()
	s.fsProbe = map[string]*fsProbeState{
		// Asked after the list: it is newer.
		"left":    {seen: true, at: listed.Add(time.Millisecond), on: false},
		"entered": {seen: true, at: listed.Add(time.Millisecond), on: true},
		// Asked before the list: the list is newer.
		"stale": {seen: true, at: listed.Add(-time.Millisecond), on: true},
	}
	s.storeFullscreen([]string{"left", "polled"}, listed)
	got := fullscreenNow(&s.fullscreen)
	slices.Sort(got)
	if want := []string{"entered", "polled"}; !slices.Equal(got, want) {
		t.Errorf("fullscreen = %v, want %v", got, want)
	}
	if _, kept := s.fsProbe["stale"]; kept {
		t.Error("an answer the list superseded is still held")
	}
}

// A probe tmux did not answer changes nothing. "No answer" read as "not full
// screen" would hand a running vim's wheel back to the panel's buffer --
// issue #19 again -- on any slow fork.
func TestAProbeWithoutAnAnswerLeavesTheFlag(t *testing.T) {
	tm := tmux.New("vibepanel-no-such-socket", t.TempDir())
	tm.Bin = "/bin/false"
	s := &Server{Tmux: tm}
	s.fullscreen.Store(&[]string{"vim"})
	s.runFullscreenProbe("vim", "vp_vim")
	if got := fullscreenNow(&s.fullscreen); !slices.Equal(got, []string{"vim"}) {
		t.Errorf("fullscreen = %v after a failed probe, want it left at [vim]", got)
	}
}
