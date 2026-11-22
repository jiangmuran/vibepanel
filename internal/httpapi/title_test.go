package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/agylog"
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
		name       string
		info       tmux.Info
		ptyTitle   string
		agentTitle string
		scratch    bool
		want       string
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
			name:       "an agy conversation title beats the command when no OSC arrived",
			info:       tmux.Info{Title: hostname(), Command: "agy", Path: "/w/repo"},
			agentTitle: "Fix Antigravity Auto-Rename Issue",
			want:       "Fix Antigravity Auto-Rename Issue",
		},
		{
			name:       "an OSC title still beats the disk annotation if agy ever grows one",
			info:       tmux.Info{Title: hostname(), Command: "agy", Path: "/w/repo"},
			ptyTitle:   "live terminal title",
			agentTitle: "older annotation",
			want:       "live terminal title",
		},
		{
			name:     "a PTY title that is only the hostname names nothing",
			info:     tmux.Info{Title: hostname(), Command: "bash", Path: "/w/repo"},
			ptyTitle: hostname(),
			want:     "repo",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveTitle(tc.info, tc.ptyTitle, tc.agentTitle, tc.scratch); got != tc.want {
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

type stubAgyProc struct {
	comm map[int]string
	fds  map[int][]string
}

func (s stubAgyProc) Children(int) []int         { return nil }
func (s stubAgyProc) Comm(pid int) string        { return s.comm[pid] }
func (s stubAgyProc) FDTargets(pid int) []string { return s.fds[pid] }

// buildFakeAgy compiles a tiny helper binary named "agy" so tmux reports
// pane_current_command == "agy" and /proc/<pid>/comm == "agy" while the binary
// holds a presence lock open, matching the real Antigravity CLI.
func buildFakeAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const prog = `package main
import (
	"os"
	"time"
)
func main() {
	f, err := os.OpenFile(os.Args[1], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	defer f.Close()
	for {
		if _, err := os.Stat(os.Args[2]); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "agy")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake agy: %v\n%s", err, out)
	}
	return bin
}

// End-to-end against real tmux and /proc (where /proc exists): an agy pane
// starts out named "agy", picks up the conversation title written to
// annotations/<id>.pbtxt without emitting any OSC sequence, keeps a user's
// manual rename untouched, and preserves the earned title after agy exits.
func TestAnAgyConversationTitleNamesTheSessionAndSurvivesExit(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("/proc is not mounted on this host")
	}
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)

	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "presence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(appDir, "annotations"), 0o755); err != nil {
		t.Fatal(err)
	}
	const cid = "461fc59a-af39-44c7-a5c6-0a65669b6570"
	lockPath := filepath.Join(appDir, "presence", cid+".lock")
	annotPath := filepath.Join(appDir, "annotations", cid+".pbtxt")
	stopFile := filepath.Join(appDir, "stop")

	bin := buildFakeAgy(t)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["`+bin+`","`+lockPath+`","`+stopFile+`"]}`)

	waitTitle := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
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
			if last == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("session title = %q, want %q", last, want)
	}

	// Before agy writes an annotation, the row falls back to the command name.
	waitTitle("agy")

	// First prompt named: annotations/<id>.pbtxt appears, sidebar updates.
	if err := os.WriteFile(annotPath, []byte(`title:"Fix Antigravity Auto-Rename Issue"`), 0o600); err != nil {
		t.Fatal(err)
	}
	waitTitle("Fix Antigravity Auto-Rename Issue")

	// agy exits under remain-on-exit: /proc/<pid> disappears, and the finished
	// pane keeps its conversation title rather than reverting to "agy".
	if err := os.WriteFile(stopFile, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if rec.Exited {
			if rec.Title != "Fix Antigravity Auto-Rename Issue" {
				t.Fatalf("exited agy title = %q, want conversation title preserved", rec.Title)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("fake agy did not exit before deadline")
}

// Where /proc does not exist (macOS), agy's camelCase hook payload carries
// transcriptPath, and the poller resolves annotations/<id>.pbtxt from that;
// once renamed by hand, neither route overwrites the user's title.
func TestAnAgyHookTranscriptNamesTheSessionWithoutProc(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.agyLogs = agylog.Watcher{Proc: stubAgyProc{}}
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)

	appDir := t.TempDir()
	annotDir := filepath.Join(appDir, "annotations")
	if err := os.MkdirAll(annotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cid = "16d19f51-d04c-4d2d-a786-7184a19fb654"
	annotPath := filepath.Join(annotDir, cid+".pbtxt")
	if err := os.WriteFile(annotPath, []byte(`title:"Master's Thesis Annual Report"`), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(appDir, "brain", cid, ".system_generated", "logs", "transcript.jsonl")

	bin := buildFakeAgy(t)
	lockPath := filepath.Join(appDir, "unused.lock")
	stopFile := filepath.Join(appDir, "never")
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["`+bin+`","`+lockPath+`","`+stopFile+`"]}`)
	t.Cleanup(func() { _ = os.WriteFile(stopFile, []byte("1"), 0o600) })

	root, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"conversationId":"` + cid + `","hookEventName":"PreInvocation","transcriptPath":` +
		`"` + strings.ReplaceAll(transcript, `\`, `\\`) + `"}`
	req, err := http.NewRequest(http.MethodPost,
		ts.URL+"/api/hook/state?sessionId="+sess.ID+"&state=working",
		strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+root)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("hook status = %d, want 204", res.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, _ := srv.DB.GetSession(ctx, sess.ID)
		if rec.Title == "Master's Thesis Annual Report" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	rec, _ := srv.DB.GetSession(ctx, sess.ID)
	if rec.Title != "Master's Thesis Annual Report" {
		t.Fatalf("title via hook transcript = %q, want %q", rec.Title, "Master's Thesis Annual Report")
	}

	// Rename manually, then change the annotation on disk: manual wins.
	if err := srv.DB.SetSessionTitle(ctx, sess.ID, "my manual name", store.TitleManual); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(annotPath, []byte(`title:"Something Else"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rec, _ = srv.DB.GetSession(ctx, sess.ID)
	if rec.Title != "my manual name" {
		t.Fatalf("manual title overwritten: %q", rec.Title)
	}
}
