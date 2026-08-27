package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// A title sent through tmux's passthrough has to name the session.
//
// This is the shape the panel could not see. `#{pane_title}` is what the poller
// reads, and a program that has noticed $TMUX does not set it: it wraps its OSC
// in the passthrough DCS so the sequence reaches the terminal a human is looking
// at, and tmux forwards those bytes to its client — the panel — without
// touching pane_title. Measured on a real tmux 3.6, client attached:
//
//	printf '\033]2;X\007'                        pane_title becomes X
//	printf '\033Ptmux;\033\033]2;X\007\033\\'    pane_title unchanged; the
//	                                             client PTY gets ESC]2;X BEL
//
// The panel parsed that title in the OSC scanner, bounded it, and broadcast it
// to the browser as a title event that no component subscribes to. Nothing
// wrote it anywhere, so the session kept the name of the directory it sat in
// and renaming from inside the process did nothing at all.
//
// Codex sends its OSC 9 and OSC 52 through passthrough exactly like this — the
// wrapped forms are literals in the binary — so this is not a hypothetical
// program.
func TestATitleSentThroughPassthroughNamesTheSession(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)

	// \033Ptmux; ESC-doubled OSC 2 ; ST. Written with octal escapes so this
	// file holds no raw control characters of its own.
	const emit = `printf '\\033Ptmux;\\033\\033]2;porting the parser\\007\\033\\\\'`
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","sleep 0.6; `+emit+`; exec sleep 30"]}`)

	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		last = rec.Title
		if last == "porting the parser" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Say which half failed, because they fail for different reasons.
	info, err := srv.Tmux.Get(ctx, "vp_"+strings.TrimPrefix(sess.TmuxName, "vp_"))
	if err == nil && info.Title == "porting the parser" {
		t.Fatalf("pane_title carries the title and the row says %q, so this tmux "+
			"stopped passing the sequence through and the test is now measuring "+
			"something else", last)
	}
	if live, ok := srv.Manager.Get(sess.ID); ok && live.Title() == "" {
		t.Fatalf("the title never arrived on the panel's PTY at all (row title %q); "+
			"either allow-passthrough is off or tmux stopped forwarding it", last)
	}
	t.Fatalf("the title reached the panel's PTY and the session is still called %q", last)
}

// The precedence, without a tmux server: what the poller does with each source.
func TestDeriveTitlePrefersWhatTmuxSawAndFallsBackToThePTY(t *testing.T) {
	for _, tc := range []struct {
		name     string
		info     tmux.Info
		ptyTitle string
		scratch  bool
		want     string
	}{
		{
			name: "pane_title wins when a program set it",
			info: tmux.Info{Title: "refactor", Command: "claude", Path: "/w/repo"},
			// A stale PTY title must not beat the live one.
			ptyTitle: "something older",
			want:     "refactor",
		},
		{
			name:     "the PTY title is used when pane_title is the hostname",
			info:     tmux.Info{Title: hostname(), Command: "codex", Path: "/w/repo"},
			ptyTitle: "porting the parser",
			want:     "porting the parser",
		},
		{
			name: "without either, the command still names it",
			info: tmux.Info{Title: hostname(), Command: "codex", Path: "/w/repo"},
			want: "codex",
		},
		{
			name:     "a PTY title beats the command, which every codex session shares",
			info:     tmux.Info{Title: hostname(), Command: "codex", Path: "/w/repo"},
			ptyTitle: "reviewing the diff",
			want:     "reviewing the diff",
		},
		{
			name:     "a PTY title that is only the hostname names nothing",
			info:     tmux.Info{Title: hostname(), Command: "bash", Path: "/w/repo"},
			ptyTitle: hostname(),
			want:     "repo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveTitle(tc.info, tc.ptyTitle, tc.scratch); got != tc.want {
				t.Errorf("deriveTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// A name the user typed survives a title arriving on the PTY, the same way it
// survives one arriving in pane_title. The second route must not be a second
// way to lose your own name for a tab.
func TestAPassthroughTitleDoesNotStompAManualRename(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	const emit = `printf '\\033Ptmux;\\033\\033]2;whatever the agent says\\007\\033\\\\'`
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"my important task",`+
			`"command":["sh","-c","sleep 0.6; `+emit+`; exec sleep 30"]}`)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	rec, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec.Title != "my important task" {
		t.Errorf("title = %q; a title from the pane took the name the user typed", rec.Title)
	}
	// And the test is only worth anything if the title did arrive.
	if live, ok := srv.Manager.Get(sess.ID); !ok || live.Title() != "whatever the agent says" {
		t.Error("no title reached the PTY, so this proves nothing about ignoring one")
	}
}
