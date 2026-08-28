package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// ─── the event log cannot stall a state update ────────────────────────────

// The property the whole design of events.go exists to have.
//
// pollOnce runs against every session every two seconds and is what keeps the
// panel's idea of what is running current. Anything hung off a state change
// that can *wait* -- a database under a lock, a full disk, a sweep in progress
// -- would stop that, and the panel would be a list of stale triangles with
// nothing anywhere saying so.
//
// So the producer side is a non-blocking send onto a bounded queue and nothing
// else. This fills the queue with nobody draining it and then records another
// transition: it has to return, and the event has to be counted as dropped
// rather than waited for. A blocking send here deadlocks the test, which is the
// failure it is meant to be.
func TestRecordingAStateChangeCannotStallTheStateUpdate(t *testing.T) {
	s := &Server{}
	ch := s.eventChan()
	row := store.Session{ID: "s1", ProjectID: "p1", State: session.StateWorking,
		StateChangedAt: time.Now().Add(-time.Minute).Unix()}

	// The queue is a memory bound as well as a backpressure one: it is held for
	// as long as the drain is behind, and "bounded" at a million entries is not
	// bounded in the way that matters on a machine whose disk has filled.
	if cap(ch) > 4096 {
		t.Fatalf("the event queue holds %d entries; the bound is what stops a drain that has "+
			"fallen behind from becoming a memory problem instead of a dropped row", cap(ch))
	}

	// Fill it. Nothing is draining, so every further send must be dropped.
	for len(ch) < cap(ch) {
		s.noteTransition(row, session.StateWaiting, time.Now())
	}
	before := s.EventsDropped()

	done := make(chan struct{})
	go func() {
		s.noteTransition(row, session.StateDone, time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recording a transition blocked on a full queue; the poller would have " +
			"stopped keeping the panel's idea of every session current")
	}
	if s.EventsDropped() <= before {
		t.Errorf("a transition onto a full queue was not counted as dropped: %d then %d",
			before, s.EventsDropped())
	}
}

// Every path that changes a state records it, by construction rather than by
// care.
//
// There were five call sites when this was written and two of them -- the hook
// and the manual override -- are the ones the poller would never notice,
// because by the time it looks the detector already agrees with the row. So the
// rule is that this package calls store.SetSessionState exactly once, inside
// the helper that also records, and this test is what says so. A sixth handler
// that writes a state has to go through it or fail here.
func TestOnlyOnePlaceInThisPackageWritesASessionState(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(e.Name())
		if rerr != nil {
			t.Fatal(rerr)
		}
		if n := strings.Count(string(src), "s.DB.SetSessionState("); n > 0 {
			found[e.Name()] = n
		}
	}
	if len(found) != 1 || found["events.go"] != 1 {
		t.Errorf("store.SetSessionState is called from %v; it must be called exactly once, "+
			"from setSessionState in events.go, or the next handler that changes a state "+
			"will be the one that forgets to record the transition", found)
	}
}

// The queue is drained, and it is Poll that starts the drain.
//
// The producer side being non-blocking is only half of it: an event that is
// enqueued and never written is a trend that is quietly empty, and nothing on a
// wall would say so. This runs the real loop and waits for the row to land.
func TestThePollerDrainsTheEventLog(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Poll(ctx)

	srv.noteTransition(store.Session{
		ID: "s1", ProjectID: "p1", State: session.StateWorking,
		StateChangedAt: time.Now().Add(-time.Minute).Unix(),
	}, session.StateDone, time.Now())

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := srv.DB.CountSessionEvents(ctx, 0, store.EventScope{})
		if err == nil && got.Finished == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a queued transition never reached the log; nothing starts the drain")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A burst is one transaction, and every row in it still lands.
//
// The batching is a contention control -- SQLite takes one write lock for the
// whole database and the poller is already using it -- so what has to be true
// is that gathering does not lose anything. Fifty transitions queued at once
// have to arrive as fifty rows.
func TestABurstOfTransitionsIsWrittenWhole(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Poll(ctx)

	const n = 50
	for i := 0; i < n; i++ {
		srv.noteTransition(store.Session{
			ID: "s" + strconv.Itoa(i), ProjectID: "p1", State: session.StateWorking,
			StateChangedAt: time.Now().Add(-time.Minute).Unix(),
		}, session.StateDone, time.Now())
	}
	if dropped := srv.EventsDropped(); dropped > 0 {
		t.Fatalf("%d of %d transitions were dropped; the queue is meant to swallow a burst "+
			"this size without losing any", dropped, n)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := srv.DB.CountSessionEvents(ctx, 0, store.EventScope{})
		if err == nil && got.Finished == n {
			return
		}
		if time.Now().After(deadline) {
			got, _ := srv.DB.CountSessionEvents(ctx, 0, store.EventScope{})
			t.Fatalf("%d of %d transitions reached the log", got.Finished, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A write that changes nothing records nothing.
//
// The poller calls this against every session every two seconds, so recording
// on the *state* rather than on the *transition* is one row per session per
// tick -- forty thousand rows a day per session, and a chart of "how many
// sessions finished this hour" that counts polls.
func TestAStateThatDidNotChangeIsNotATransition(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"p"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"one","command":[]}`)
	row, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := srv.EventsDropped()
	drained := len(srv.eventChan())
	if err := srv.setSessionState(ctx, row, row.State, row.StateSource); err != nil {
		t.Fatal(err)
	}
	if len(srv.eventChan()) != drained || srv.EventsDropped() != before {
		t.Errorf("writing the state a session already had queued a transition; the poller "+
			"does this to every session every two seconds (queue %d -> %d)",
			drained, len(srv.eventChan()))
	}
}

// The log is swept, and the sweep is what keeps a wall that has been up for a
// month from carrying a year of rows.
func TestTheEventLogIsSweptBackToItsRetention(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -400).Unix()
	recent := time.Now().Add(-time.Hour).Unix()
	for _, at := range []int64{old, recent} {
		if err := srv.DB.RecordSessionEvent(ctx, store.SessionEvent{
			At: at, SessionID: "s1", ProjectID: "p1",
			From: session.StateWorking, To: session.StateDone,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv.sweepEvents(ctx)

	rows, err := srv.DB.RecentSessionEvents(ctx, 0, 10, store.EventScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].At != recent {
		t.Fatalf("after a sweep the log holds %d rows (%v); a 400-day-old one should be gone",
			len(rows), rows)
	}
}

// A state the panel does not recognise never reaches the log.
//
// Red line 6's reasoning one table over: an unknown state in here is a row
// every future series miscounts, silently, with nothing on a wall to say so.
func TestTheLogRefusesAStateNothingRecognises(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts
	err := srv.DB.RecordSessionEvent(context.Background(), store.SessionEvent{
		At: time.Now().Unix(), SessionID: "s1", ProjectID: "p1",
		From: session.StateWorking, To: session.State("thinking"),
	})
	if err == nil {
		t.Fatal("the log accepted a state that is not in the enum")
	}
}

// ─── what a board asks for is what it gets ────────────────────────────────

// A board with no repository widget makes the dashboard read no repository, and
// a board with no flow widget makes it read no event log.
//
// A cost decision rather than a permission one -- every section is computable
// for every link -- but it is the one that decides whether a wall polling every
// two seconds keeps a `git log` alive per project.
func TestABoardOnlyPullsTheSectionsItDraws(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, tc := range []struct {
		name                         string
		board                        string
		wantRepo, wantFlow, wantFeed bool
	}{
		{"a board of one count", `{"grid":12,"widgets":[{"kind":"states","span":12}]}`,
			false, false, false},
		{"a production board", `{"grid":12,"widgets":[{"kind":"output","span":12}]}`,
			true, false, false},
		{"a board that draws the day", `{"grid":12,"widgets":[{"kind":"flow","span":12}]}`,
			false, true, false},
		{"a board with a feed", `{"grid":12,"widgets":[{"kind":"feed","span":12}]}`,
			false, false, true},
		// A metric pulls the section it comes out of. Without that, a board
		// whose one figure is "commits today" carries no repository section and
		// the figure is a dash forever.
		{"one production figure",
			`{"grid":12,"widgets":[{"kind":"bignumber","metric":"commitsToday","span":12}]}`,
			true, false, false},
		{"one figure out of the log",
			`{"grid":12,"widgets":[{"kind":"bignumber","metric":"avgWaitToday","span":12}]}`,
			false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := newShare(t, ts, `{"name":"wall","detail":"names","board":`+tc.board+`}`)
			_, body := shareGET(t, ts, link.Token)
			got := decodeDashboard(t, body)
			if (got.Repo != nil) != tc.wantRepo {
				t.Errorf("repo section present = %v, want %v", got.Repo != nil, tc.wantRepo)
			}
			if (got.Flow != nil) != tc.wantFlow {
				t.Errorf("flow section present = %v, want %v", got.Flow != nil, tc.wantFlow)
			}
			if (got.Feed != nil) != tc.wantFeed {
				t.Errorf("feed section present = %v, want %v", got.Feed != nil, tc.wantFeed)
			}
		})
	}
}

// ─── the repository half discloses counts and nothing else ────────────────

// makeRepo builds a small working tree with a recognisable filename, a
// recognisable commit subject and a recognisable branch.
func makeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "acme-holdings-payroll")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("payroll-secrets.txt", "one\ntwo\nthree\n")
	for _, args := range [][]string{
		{"init", "-q", "-b", "release-for-acme"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "Somebody Real"},
		{"add", "-A"},
		{"commit", "-q", "-m", "rotate the production keys for acme"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("cannot arrange a repository: %v: %s", err, out)
		}
	}
	return dir
}

// warmUp polls until the background read has landed, or gives up.
//
// The poll deliberately never waits for git -- that is the property the test
// below pins -- so a test that wants the numbers has to ask twice.
func warmUp(t *testing.T, ts *httptest.Server, token string) shareDashboard {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := shareGET(t, ts, token)
		got := decodeDashboard(t, body)
		if got.Repo != nil && got.Repo.Readable {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the repository section never became readable: %s", body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The first poll returns without having run anything.
//
// This is the whole of why a wall may carry a commit count at all. A dashboard
// polled every two seconds that read `git log` inline would be one process per
// project per poll, forever, on a machine whose agents are contending for the
// same .git directory. So the first ask is "not counted yet" and the refresh
// happens behind it.
func TestTheFirstPollDoesNotWaitForGit(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := makeRepo(t)
	postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"Acme payroll"}`)

	link := newShare(t, ts, `{"name":"wall","detail":"names",`+
		`"board":{"grid":12,"widgets":[{"kind":"output","span":12}]}}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Repo == nil {
		t.Fatal("a board with a production widget got no repository section")
	}
	if got.Repo.Readable {
		t.Error("the first poll came back with a repository already read; a wall polling " +
			"every two seconds must never be the thing that runs `git log`")
	}
	// And it does land, shortly, without anybody asking for it differently.
	warm := warmUp(t, ts, link.Token)
	if warm.Repo.Today.Commits != 1 {
		t.Errorf("counted %d commits today, want 1", warm.Repo.Today.Commits)
	}
	if warm.Repo.Repos != 1 {
		t.Errorf("found %d working trees, want 1", warm.Repo.Repos)
	}
}

// What the repository half may say, and the much longer list of what it may not.
//
// `git log --shortstat` is what produces these figures, and that is the
// disclosure decision rather than a parsing convenience: `--numstat` would carry
// every changed filename through this process on its way to a wall, and `%s`
// would carry the commit messages. A commit *count* is a number; a commit
// *subject* is prose from inside somebody's repository.
func TestTheRepositorySectionCarriesCountsAndNoText(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := makeRepo(t)
	postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"Acme payroll"}`)

	for _, detail := range []string{"counts", "names"} {
		link := newShare(t, ts, `{"name":"wall","detail":"`+detail+`",`+
			`"board":{"grid":12,"widgets":[{"kind":"output","span":6},`+
			`{"kind":"codechurn","span":6,"by":"lines","days":7},`+
			`{"kind":"repoprojects","span":12,"by":"commits"}]}}`)
		got := warmUp(t, ts, link.Token)
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)

		for _, secret := range []struct{ value, what string }{
			{dir, "the project's path"},
			{"acme-holdings-payroll", "a directory name"},
			{"payroll-secrets.txt", "a filename"},
			{"rotate the production keys for acme", "a commit subject"},
			{"Somebody Real", "an author"},
			{"release-for-acme", "a branch name"},
		} {
			if strings.Contains(text, secret.value) {
				t.Errorf("%s mode discloses %s (%q):\n%s", detail, secret.what, secret.value, text)
			}
		}
		// Counts *are* disclosed, in both modes: a commit count names nobody,
		// and withholding it would leave the board as empty as it was.
		if got.Repo.Today.Commits != 1 {
			t.Errorf("%s mode: %d commits, want 1", detail, got.Repo.Today.Commits)
		}
		if got.Repo.Today.Added <= 0 {
			t.Errorf("%s mode: %d lines added, want more than none", detail, got.Repo.Today.Added)
		}
		if len(got.Repo.ByProject) != 1 {
			t.Fatalf("%s mode: %d projects, want 1", detail, len(got.Repo.ByProject))
		}
		// The project's *name* still follows the detail mode, exactly like
		// every other group on this dashboard.
		if detail == "counts" && got.Repo.ByProject[0].Name != "" {
			t.Errorf("counts mode named a project in the repository section: %q",
				got.Repo.ByProject[0].Name)
		}
		if detail == "names" && got.Repo.ByProject[0].Name != "Acme payroll" {
			t.Errorf("names mode dropped the project name: %q", got.Repo.ByProject[0].Name)
		}
	}
}

// The four gates in front of the only outbound request a wall can cause.
//
// None of them is a default: an owner signed in has to have put a pull-request
// widget on a board, pointed the link at one project, set it to disclose names,
// and started the panel with a token in its environment. Any one of them
// missing and github.com is never reached.
func TestAWallReachesGitHubOnlyWhenEveryGateIsOpen(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := makeRepo(t)
	// A github.com remote, so the only thing left deciding is the gates.
	cmd := exec.Command("git", "remote", "add", "origin",
		"https://github.com/acme-holdings/payroll.git")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot add a remote: %v: %s", err, out)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)

	asked := make(chan struct{}, 32)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"open":{"totalCount":3,"nodes":[]},` +
			`"merged":{"nodes":[]}}}}`))
	}))
	t.Cleanup(fake.Close)
	srv.GitHub = git.Client{Endpoint: fake.URL, HTTP: fake.Client()}
	// Set once, before any poll. Replacing it per case races the background
	// refresh a previous case's poll started -- which is a hazard of the test
	// rather than of the panel, because nothing reassigns this field at runtime.
	// Short TTLs so a case that should ask, asks within the few polls below.
	srv.Git = git.Cache{WarmFor: time.Millisecond, GitHubFor: time.Millisecond}

	prBoard := `{"grid":12,"widgets":[{"kind":"prs","span":12}]}`
	plainBoard := `{"grid":12,"widgets":[{"kind":"output","span":12}]}`

	for _, tc := range []struct {
		name, body string
		token      bool
		wantAsk    bool
	}{
		{"no pull-request widget on the board",
			`{"name":"w","detail":"names","scope":"project","scopeId":"` + project.ID +
				`","board":` + plainBoard + `}`, true, false},
		{"a link that is not scoped to one project",
			`{"name":"w","detail":"names","board":` + prBoard + `}`, true, false},
		{"a link that discloses no names",
			`{"name":"w","detail":"counts","scope":"project","scopeId":"` + project.ID +
				`","board":` + prBoard + `}`, true, false},
		{"no token in the panel's environment",
			`{"name":"w","detail":"names","scope":"project","scopeId":"` + project.ID +
				`","board":` + prBoard + `}`, false, false},
		{"every gate open",
			`{"name":"w","detail":"names","scope":"project","scopeId":"` + project.ID +
				`","board":` + prBoard + `}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.token {
				t.Setenv("GITHUB_TOKEN", "test-token")
			} else {
				t.Setenv("GITHUB_TOKEN", "")
				t.Setenv("GH_TOKEN", "")
			}
			drain(asked)

			link := newShare(t, ts, tc.body)
			// Several polls: the request is made behind the poll, so "it never
			// asked" needs more than one chance to be wrong.
			for i := 0; i < 5; i++ {
				shareGET(t, ts, link.Token)
				time.Sleep(60 * time.Millisecond)
			}
			got := len(asked) > 0
			if got != tc.wantAsk {
				t.Errorf("reached github.com = %v, want %v", got, tc.wantAsk)
			}
		})
	}
}

// A fetch that has not finished, or that failed, is "not counted yet" and never
// a zero.
//
// "No pull requests are open" and "we could not ask" are different facts about
// a repository, and a wall showing the first when it means the second is the
// one failure this whole section's `readable` flag exists to prevent. There is
// nobody standing at the screen to tell them apart.
func TestPullRequestsThatCouldNotBeFetchedAreNotZero(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := makeRepo(t)
	cmd := exec.Command("git", "remote", "add", "origin",
		"https://github.com/acme-holdings/payroll.git")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot add a remote: %v: %s", err, out)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)

	// A GitHub that answers 200 with an errors array, which is how a renamed
	// repository and a token that cannot see it both arrive.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`))
	}))
	t.Cleanup(fake.Close)
	srv.GitHub = git.Client{Endpoint: fake.URL, HTTP: fake.Client()}
	srv.Git = git.Cache{WarmFor: time.Millisecond, GitHubFor: time.Millisecond}
	t.Setenv("GITHUB_TOKEN", "test-token")

	link := newShare(t, ts, `{"name":"w","detail":"names","scope":"project","scopeId":"`+
		project.ID+`","board":{"grid":12,"widgets":[{"kind":"prs","span":12}]}}`)
	for i := 0; i < 6; i++ {
		_, body := shareGET(t, ts, link.Token)
		got := decodeDashboard(t, body)
		if got.Repo == nil || got.Repo.PRs == nil {
			t.Fatalf("a board with a pull-request widget got no section: %s", body)
		}
		if got.Repo.PRs.Readable {
			t.Fatalf("a fetch that failed was reported as readable, with %d open",
				got.Repo.PRs.Open)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// ─── the day, as the log recorded it ──────────────────────────────────────

// The flow section counts transitions and the feed lists them, both renamed
// under the link's own secret.
func TestTheDayIsDrawnFromTheEventLog(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"rotate the production keys","command":[]}`)

	now := time.Now()
	for _, ev := range []store.SessionEvent{
		{At: now.Add(-2 * time.Hour).Unix(), SessionID: sess.ID, ProjectID: project.ID,
			From: session.StateWorking, To: session.StateWaiting},
		{At: now.Add(-time.Hour).Unix(), SessionID: sess.ID, ProjectID: project.ID,
			From: session.StateWaiting, To: session.StateWorking, ForSeconds: 3600},
		{At: now.Add(-time.Minute).Unix(), SessionID: sess.ID, ProjectID: project.ID,
			From: session.StateWorking, To: session.StateDone, ForSeconds: 60},
	} {
		if err := srv.DB.RecordSessionEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	link := newShare(t, ts, `{"name":"wall","detail":"names",`+
		`"board":{"grid":12,"widgets":[{"kind":"flow","span":6,"by":"hour"},`+
		`{"kind":"feed","span":6}]}}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)

	if got.Flow == nil || got.Feed == nil {
		t.Fatal("a board with a flow and a feed widget got neither section")
	}
	if got.Flow.Today.Waited != 1 || got.Flow.Today.Started != 1 || got.Flow.Today.Finished != 1 {
		t.Errorf("today reads started=%d waited=%d finished=%d, want 1 of each",
			got.Flow.Today.Started, got.Flow.Today.Waited, got.Flow.Today.Finished)
	}
	// The wire carries the sum and the count rather than an average, so that a
	// span where nothing finished waiting is empty rather than a zero-second
	// wait.
	if got.Flow.Today.WaitEnded != 1 || got.Flow.Today.WaitSeconds != 3600 {
		t.Errorf("waits read %d over %ds, want 1 over 3600s",
			got.Flow.Today.WaitEnded, got.Flow.Today.WaitSeconds)
	}
	if len(got.Feed.Entries) != 3 {
		t.Fatalf("the feed carries %d entries, want 3", len(got.Feed.Entries))
	}
	// Newest first, so a wall reads down from what just happened.
	if got.Feed.Entries[0].To != session.StateDone {
		t.Errorf("the feed's first entry is %q; it should be the most recent one",
			got.Feed.Entries[0].To)
	}
	raw, err := json.Marshal(got.Feed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sess.ID) || strings.Contains(string(raw), project.ID) {
		t.Errorf("the feed carries a real id:\n%s", raw)
	}
	if got.Feed.Entries[0].Name != "rotate the production keys" {
		t.Errorf("names mode dropped the session title from the feed: %q",
			got.Feed.Entries[0].Name)
	}

	// And under counts, no title at all.
	quiet := newShare(t, ts, `{"name":"wall","detail":"counts",`+
		`"board":{"grid":12,"widgets":[{"kind":"feed","span":12}]}}`)
	_, qbody := shareGET(t, ts, quiet.Token)
	if strings.Contains(string(qbody), "rotate the production keys") {
		t.Errorf("counts mode carried a session title in the feed:\n%s", qbody)
	}
}

// A link scoped to one project sees that project's events and no others.
//
// The failure this exists for has one shape everywhere on this surface: an
// empty filter means "everything", so a scope that resolves to nothing must
// show nothing rather than the whole panel.
func TestAScopedLinkSeesOnlyItsOwnEvents(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	mine := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"mine"}`)
	theirs := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"theirs"}`)
	now := time.Now().Unix()
	for _, p := range []string{mine.ID, theirs.ID, theirs.ID} {
		if err := srv.DB.RecordSessionEvent(ctx, store.SessionEvent{
			At: now, SessionID: "s-" + p, ProjectID: p,
			From: session.StateWorking, To: session.StateDone,
		}); err != nil {
			t.Fatal(err)
		}
	}

	link := newShare(t, ts, `{"name":"one","detail":"names","scope":"project","scopeId":"`+
		mine.ID+`","board":{"grid":12,"widgets":[{"kind":"flow","span":12}]}}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Flow == nil || got.Flow.Today.Finished != 1 {
		t.Fatalf("a project-scoped link counted %v finished; it should see only its own one",
			got.Flow)
	}

	// And a link whose project has been deleted sees nothing rather than
	// everything.
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/projects/"+mine.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	_, after := shareGET(t, ts, link.Token)
	gone := decodeDashboard(t, after)
	if gone.Flow != nil && gone.Flow.Today.Finished != 0 {
		t.Errorf("a link whose project was deleted counted %d finished; an empty scope must "+
			"not become an empty filter", gone.Flow.Today.Finished)
	}
}
