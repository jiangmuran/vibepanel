package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A share link is a second door onto a panel whose first door is one password
// in front of a writable terminal. Every test in this file is about the shape
// of that door rather than about what the dashboard looks like.

// freshShare is the one moment a link's token is readable.
type freshShare struct {
	Token     string `json:"token"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Detail    string `json:"detail"`
	ExpiresAt int64  `json:"expiresAt"`
	CreatedAt int64  `json:"createdAt"`
}

// newShare mints a link through the real endpoint, as the signed-in owner.
func newShare(t *testing.T, ts *httptest.Server, body string) freshShare {
	t.Helper()
	return postJSON[freshShare](t, ts, "/api/settings/shares", body)
}

// shareGET fetches the dashboard the way a wall display does: no cookie, no
// header, nothing but the URL.
func shareGET(t *testing.T, ts *httptest.Server, token string) (*http.Response, []byte) {
	t.Helper()
	res, err := anonymousClient(t).Get(ts.URL + "/api/share/" + token + "/dashboard")
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	return res, body
}

func decodeDashboard(t *testing.T, body []byte) shareDashboard {
	t.Helper()
	var out shareDashboard
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode dashboard: %v: %s", err, body)
	}
	return out
}

// revokeShare deletes a link as the signed-in owner.
func revokeShare(t *testing.T, ts *httptest.Server, id string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/shares/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	res.Body.Close()
	return res.StatusCode
}

// The boundary, asserted from the outside.
//
// A read-only link is only read-only if the token it carries is not a
// credential anywhere else, and "anywhere else" has to be checked rather than
// assumed: the two obvious ways to present it are the two the panel already
// accepts from a program and from a browser, and neither is supposed to work.
//
// Delete the middleware, move the route inside the RequireAuth group, or teach
// currentUser to look in share_links, and this fails.
func TestAShareTokenReachesTheDashboardAndNothingElse(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	link := newShare(t, ts, `{"name":"wall","detail":"counts"}`)

	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the dashboard itself = %d, want 200: %s", res.StatusCode, body)
	}
	if got := decodeDashboard(t, body); got.Name != "wall" {
		t.Errorf("name = %q, want the link's own name", got.Name)
	}

	// Everything a share token must not be a credential for. Each way of
	// presenting it exists for somebody else: the Bearer header is how an API
	// token arrives, the cookie is how a browser session arrives.
	anon := anonymousClient(t)
	for _, probe := range []struct {
		method, path, why string
	}{
		{http.MethodGet, "/api/state", "the whole panel, names and paths included"},
		{http.MethodGet, "/api/settings", "where the panel lives on disk"},
		{http.MethodGet, "/api/settings/audit", "usernames and addresses"},
		{http.MethodGet, "/api/settings/shares", "minting a second link from the first"},
		{http.MethodPost, "/api/sessions", "starting a process"},
		{http.MethodDelete, "/api/projects/" + project.ID, "killing every session in a project"},
		{http.MethodGet, "/api/projects/" + project.ID + "/files", "reading the disk"},
		{http.MethodGet, "/api/projects/" + project.ID + "/notes", "somebody's notes"},
		{http.MethodGet, "/api/usage", "the unredacted per-session reading"},
	} {
		for _, how := range []string{"bearer", "cookie"} {
			req, err := http.NewRequest(probe.method, ts.URL+probe.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("build %s %s: %v", probe.method, probe.path, err)
			}
			req.Header.Set("Content-Type", "application/json")
			if how == "bearer" {
				req.Header.Set("Authorization", "Bearer "+link.Token)
			} else {
				req.Header.Set("Cookie", "vibepanel_session="+link.Token)
			}
			r, err := anon.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", probe.method, probe.path, err)
			}
			r.Body.Close()
			if r.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with the share token as a %s = %d, want 401 (%s)",
					probe.method, probe.path, how, r.StatusCode, probe.why)
			}
		}
	}

	// The socket is the terminal. A share token opening it would make every
	// other refusal above decoration.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", "vibepanel_session="+link.Token)
	c, wres, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws",
		&websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		c.CloseNow()
		t.Fatal("a share token opened the terminal socket")
	}
	if wres != nil && wres.StatusCode != http.StatusUnauthorized {
		t.Errorf("the socket answered %d to a share token, want 401", wres.StatusCode)
	}

	// And the share surface is one GET. Anything else under it is not a
	// narrower version of the dashboard, it is a route that does not exist.
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/api/share/" + link.Token + "/dashboard"},
		{http.MethodDelete, "/api/share/" + link.Token + "/dashboard"},
		{http.MethodGet, "/api/share/" + link.Token + "/state"},
		{http.MethodGet, "/api/share/" + link.Token + "/usage"},
	} {
		req, err := http.NewRequest(probe.method, ts.URL+probe.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := anon.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", probe.method, probe.path, err)
		}
		r.Body.Close()
		if r.StatusCode == http.StatusOK {
			t.Errorf("%s %s succeeded; the share surface is one GET", probe.method, probe.path)
		}
	}
}

// Revocation is the whole reason a link is a row rather than a signed URL.
func TestARevokedShareLinkStopsWorking(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	if res, body := shareGET(t, ts, link.Token); res.StatusCode != http.StatusOK {
		t.Fatalf("before revoking = %d: %s", res.StatusCode, body)
	}
	if code := revokeShare(t, ts, link.ID); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
	if res, body := shareGET(t, ts, link.Token); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("after revoking = %d, want 401: %s", res.StatusCode, body)
	}
}

// An expiry nobody has to come back and act on.
//
// The row is aged by hand rather than by sleeping: the property under test is
// that the comparison lives in the query, and a test that waits a second to
// find that out is a test somebody eventually deletes.
func TestAnExpiredShareLinkStopsWorking(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","expiresIn":3600}`)
	if link.ExpiresAt == 0 {
		t.Fatal("expiresIn was ignored; the link never expires")
	}

	if res, body := shareGET(t, ts, link.Token); res.StatusCode != http.StatusOK {
		t.Fatalf("before expiry = %d: %s", res.StatusCode, body)
	}

	if _, err := srv.DB.SQL().ExecContext(context.Background(),
		`UPDATE share_links SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).Unix(), link.ID); err != nil {
		t.Fatalf("age the link: %v", err)
	}

	if res, body := shareGET(t, ts, link.Token); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("after expiry = %d, want 401: %s", res.StatusCode, body)
	}
}

// What the link deliberately does not say.
//
// A project path names a customer and a home directory; a command line carries
// whatever an agent was invoked with; a tmux name and a session id are how the
// authenticated API addresses a row. None of them has a use on a screen behind
// somebody's desk. The whole response is searched as text rather than checked
// field by field, so a field added to the payload later is covered by a test
// written today.
func TestTheDashboardNeverCarriesAPathACommandOrARealID(t *testing.T) {
	ts, _ := newTestServer(t)

	// A path with something recognisable in it, so a substring search means
	// something: t.TempDir() on its own appears in nobody's output.
	dir := filepath.Join(t.TempDir(), "acme-holdings-payroll")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"rotate the production keys","command":[]}`)

	for _, detail := range []string{"counts", "names"} {
		link := newShare(t, ts, `{"name":"wall","detail":"`+detail+`"}`)
		res, body := shareGET(t, ts, link.Token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: dashboard = %d: %s", detail, res.StatusCode, body)
		}
		text := string(body)

		for _, secret := range []struct{ value, what string }{
			{dir, "the project's path on disk"},
			{"acme-holdings-payroll", "a directory name"},
			{sess.TmuxName, "the tmux session name"},
			{sess.ID, "the real session id"},
			{project.ID, "the real project id"},
		} {
			if secret.value != "" && strings.Contains(text, secret.value) {
				t.Errorf("%s mode discloses %s (%q):\n%s", detail, secret.what, secret.value, text)
			}
		}
		// The field names themselves, because a leak arrives as a field added
		// to the payload by somebody who did not read this file.
		for _, key := range []string{`"path"`, `"cwd"`, `"command"`, `"tmuxName"`, `"diskPath"`,
			`"url"`, `"remote"`} {
			if strings.Contains(text, key) {
				t.Errorf("%s mode carries a %s field; nothing on a wall needs it:\n%s",
					detail, key, text)
			}
		}

		got := decodeDashboard(t, body)
		if len(got.Sessions) != 1 || len(got.Projects) != 1 {
			t.Fatalf("%s: %d session rows and %d groups, want 1 and 1",
				detail, len(got.Sessions), len(got.Projects))
		}
		row := got.Sessions[0]
		switch detail {
		case "counts":
			if row.Name != "" || got.Projects[0].Name != "" {
				t.Errorf("counts mode named a session (%q) or a project (%q); the default "+
					"has to be the one that is safe to point a camera at",
					row.Name, got.Projects[0].Name)
			}
			if strings.Contains(text, "rotate the production keys") ||
				strings.Contains(text, "Acme payroll") {
				t.Errorf("counts mode carries a name somewhere in the body:\n%s", text)
			}
			// A repository is a name, and a public one. Under counts the board
			// sends none, so a link to github.com/<org>/<repo> would identify
			// the customer more precisely than the project path this mode
			// exists to withhold -- and it would do it on the mode people pick
			// precisely because they are pointing a camera at the screen.
			if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
				t.Errorf("counts mode named a repository (%q/%q)",
					got.ScopeRepoOwner, got.ScopeRepoName)
			}
			if strings.Contains(text, "github.com") {
				t.Errorf("counts mode mentions github.com somewhere in the body:\n%s", text)
			}
		case "names":
			if row.Name != "rotate the production keys" {
				t.Errorf("names mode did not carry the title; got %q", row.Name)
			}
			if got.Projects[0].Name != "Acme payroll" {
				t.Errorf("names mode did not carry the project name; got %q", got.Projects[0].Name)
			}
		}
	}
}

// A board that is about one project may say which repository, and only then.
//
// 「read only和面板左下角等等地方 都加上GitHub链接和项目名」. The name half was
// already there under `names`; the repository is new, and it is the first thing
// on this surface that reads a working tree — so what it discloses is worth a
// test of its own rather than a line in the redaction sweep.
//
// Four narrowings, one case each below. Only under `names`; only for a
// project-scoped link; only for a github.com remote; and as two parsed halves
// rather than the remote string, so the viewer's browser can build one URL and
// nothing else.
func TestARepositoryIsNamedOnlyOnAProjectBoardThatAlreadyNamesThings(t *testing.T) {
	ts, srv := newTestServer(t)

	dir := t.TempDir()
	repoAtRemote(t, dir, "https://github.com/acme-holdings/payroll.git")
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)

	// Somewhere with a remote this panel will not link to, to prove the refusal
	// is about the host and not about there being no remote at all.
	other := t.TempDir()
	repoAtRemote(t, other, "git@gitlab.example.com:acme/secret.git")
	elsewhere := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+other+`","name":"Elsewhere"}`)

	// The remote is read in the background now -- the poll never runs a process
	// -- so every assertion below has to be made against a warm entry that has
	// landed. Without this wait the refusals pass on a server that had simply
	// not read anything yet, which is a disclosure test checking nothing.
	warmRemote(t, srv, dir)
	warmRemote(t, srv, other)

	read := func(t *testing.T, body string) (shareDashboard, string) {
		t.Helper()
		link := newShare(t, ts, body)
		res, raw := shareGET(t, ts, link.Token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("dashboard = %d: %s", res.StatusCode, raw)
		}
		return decodeDashboard(t, raw), string(raw)
	}

	t.Run("named and project-scoped", func(t *testing.T) {
		got, raw := read(t, `{"name":"wall","detail":"names","scope":"project","scopeId":"`+
			project.ID+`"}`)
		if got.ScopeRepoOwner != "acme-holdings" || got.ScopeRepoName != "payroll" {
			t.Fatalf("repository = %q/%q, want acme-holdings/payroll",
				got.ScopeRepoOwner, got.ScopeRepoName)
		}
		// Two halves and not a URL. What the viewer builds is decided by the
		// viewer's code, which refuses everything that is not github.com/x/y;
		// sending a URL would move that decision to whatever is in a config
		// file on this machine.
		if strings.Contains(raw, ".git") || strings.Contains(raw, "https://github.com/") {
			t.Errorf("the body carries a remote URL rather than two halves:\n%s", raw)
		}
		// And still nothing about where it is on disk.
		if strings.Contains(raw, dir) {
			t.Errorf("the body carries the project's path:\n%s", raw)
		}
	})

	t.Run("counts, project-scoped", func(t *testing.T) {
		got, raw := read(t, `{"name":"wall","detail":"counts","scope":"project","scopeId":"`+
			project.ID+`"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("counts mode named %q/%q", got.ScopeRepoOwner, got.ScopeRepoName)
		}
		if strings.Contains(raw, "acme-holdings") || strings.Contains(raw, "payroll") {
			t.Errorf("counts mode leaked the repository:\n%s", raw)
		}
	})

	t.Run("named, whole panel", func(t *testing.T) {
		// No single project, so no single repository. A board covering three
		// projects that named one of their repositories would be worse than
		// naming none.
		got, _ := read(t, `{"name":"wall","detail":"names"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("an unscoped board named %q/%q", got.ScopeRepoOwner, got.ScopeRepoName)
		}
	})

	t.Run("named, session-scoped", func(t *testing.T) {
		// ScopeName is the session's title here. Hanging a repository off it
		// would disclose which project a session belongs to on a link that was
		// deliberately narrowed to one session.
		sess := postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","title":"one job","command":[]}`)
		got, _ := read(t, `{"name":"wall","detail":"names","scope":"session","scopeId":"`+
			sess.ID+`"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("a session board named %q/%q", got.ScopeRepoOwner, got.ScopeRepoName)
		}
	})

	t.Run("a host this panel does not link to", func(t *testing.T) {
		got, raw := read(t, `{"name":"wall","detail":"names","scope":"project","scopeId":"`+
			elsewhere.ID+`"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("a non-GitHub remote was disclosed as %q/%q",
				got.ScopeRepoOwner, got.ScopeRepoName)
		}
		if strings.Contains(raw, "gitlab.example.com") || strings.Contains(raw, "secret") {
			t.Errorf("the body carries a remote it will not link to:\n%s", raw)
		}
	})

	t.Run("a directory that is not a checkout", func(t *testing.T) {
		bare := t.TempDir()
		plain := postJSON[store.Project](t, ts, "/api/projects",
			`{"path":"`+bare+`","name":"Plain"}`)
		warmRemote(t, srv, bare)
		got, _ := read(t, `{"name":"wall","detail":"names","scope":"project","scopeId":"`+
			plain.ID+`"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("a directory with no repository reported %q/%q",
				got.ScopeRepoOwner, got.ScopeRepoName)
		}
	})
}

// warmRemote waits until the background read of one directory's origin has
// landed, which is what a second poll of a wall would find.
func warmRemote(t *testing.T, srv *Server, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := srv.Git.Remote(dir); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the warm cache never read the origin of %s", dir)
}

// countingGit puts a `git` on PATH that records every invocation and the
// directory it ran in.
//
// A shim rather than a counter inside internal/git, because what is being
// asserted is that a *process* was not started, and only the process itself can
// say that.
func countingGit(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations")
	script := "#!/bin/sh\n" +
		"printf '%s\\t%s\\n' \"$(pwd -P)\" \"$*\" >> " + log + "\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = get-url ]; then\n" +
		"    echo https://github.com/acme-holdings/payroll.git\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// A wall's poll starts no process, whatever it asks the working tree for.
//
// Red line 8, and the half of it that is easiest to undo by accident: the
// obvious way to put a repository's name on a board is s.Git.Read, which is the
// *foreground* cache with a three-second TTL, and it runs three subprocesses.
// Against a screen polling every two seconds forever that is a fork per project
// per poll, and one of the three is
// `git log --format=%H%x00%an%x00%at%x00%s` -- author names and commit subjects
// pulled through this process to arrive at two words that were already in the
// remote URL. Counts and a repository name are all this surface may read.
func TestAWallsPollNeverRunsGit(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"Acme payroll"}`)

	ran := countingGit(t)
	// No foreground caching at all, so a read on the request goroutine cannot
	// hide behind the three-second TTL and pass on a fast suite. The warm cache
	// keeps its own TTL, which is the thing under test.
	srv.Git = git.Cache{TTL: -1}

	link := newShare(t, ts, `{"name":"wall","detail":"names","scope":"project","scopeId":"`+
		project.ID+`","board":{"widgets":[{"kind":"machine"}]}}`)
	const polls = 6
	for i := 0; i < polls; i++ {
		res, raw := shareGET(t, ts, link.Token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("dashboard = %d: %s", res.StatusCode, raw)
		}
	}

	// Matched against the resolved path: the shim reports `pwd -P`, and t.TempDir
	// hands out a path under a /tmp that is a symlink on macOS.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	here := []string{}
	for _, line := range ran() {
		if at, args, found := strings.Cut(line, "\t"); found && at == real {
			here = append(here, args)
		}
	}
	for _, args := range here {
		for _, forbidden := range []string{" log ", " status "} {
			if strings.Contains(args+" ", forbidden) {
				t.Errorf("the poll ran `git%s`, which carries commit subjects, author "+
					"names and every changed path through this process for a repository "+
					"name: %s", forbidden, args)
			}
		}
	}
	// One background read of the origin, shared by every poll. Two is the TTL
	// having expired mid-test; six is a fork per poll and the bug.
	if len(here) > 2 {
		t.Errorf("%d polls started %d git processes:\n  %s", polls, len(here),
			strings.Join(here, "\n  "))
	}
}

// A board whose only spend series is `spentmade` still gets one.
//
// The section is computed because board.Needs said the board wants it, and the
// *width* of it was then decided by a second list that had never heard of the
// widget. Nothing matched, the widest was zero, and the day array came back
// empty -- so newsroom, deskwall and the spentmade preset all render "nothing
// to show" forever, with a spend total beside them that is not zero.
func TestABoardWhoseOnlySpendSeriesIsSpentMadeGetsIt(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"Acme"}`)
	seedUsage(t, srv, project.Path, 4242)

	link := newShare(t, ts,
		`{"name":"wall","board":{"widgets":[{"kind":"spentmade"}]}}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Spend == nil {
		t.Fatal("a board with a spend series carries no spend section at all")
	}
	if !got.Spend.Readable || got.Spend.Today.Total == 0 {
		t.Fatalf("nothing was counted, so this test would pass on any server: "+
			"readable=%v today=%d", got.Spend.Readable, got.Spend.Today.Total)
	}
	if len(got.Spend.Days) == 0 {
		t.Errorf("the board draws a day series and was sent none, while its own totals "+
			"say %d tokens were spent today", got.Spend.Today.Total)
	}
}

// The row ids are per link, and are not the panel's.
//
// Stable, because a list that re-keys itself every two seconds re-mounts every
// row; different between links, so two dashboards on two walls cannot be
// correlated into one picture of the panel by somebody watching both.
func TestShareRowIDsAreStablePerLinkAndDifferBetweenLinks(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":[]}`)

	first := newShare(t, ts, `{"name":"one"}`)
	second := newShare(t, ts, `{"name":"two"}`)

	idsFor := func(token string) []string {
		res, body := shareGET(t, ts, token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("dashboard = %d: %s", res.StatusCode, body)
		}
		out := []string{}
		for _, row := range decodeDashboard(t, body).Sessions {
			out = append(out, row.ID)
		}
		if len(out) == 0 {
			t.Fatal("no session rows, so nothing is being compared")
		}
		return out
	}

	a1 := idsFor(first.Token)
	a2 := idsFor(first.Token)
	b1 := idsFor(second.Token)

	if a1[0] != a2[0] {
		t.Errorf("the same link gave one row two ids (%q then %q); every poll would re-mount "+
			"every row", a1[0], a2[0])
	}
	if a1[0] == b1[0] {
		t.Errorf("two links gave one row the same id (%q); two walls could be joined into "+
			"one picture of the panel", a1[0])
	}
}

// The token is a credential, and is stored the way credentials are here.
func TestAShareTokenIsNotStoredInTheClear(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	// The stored bytes, read out and compared against both answers: it must not
	// be the token, and it must be its SHA-256. Counting rows that match the
	// token would pass whatever is in the column, because SQLite never treats a
	// BLOB as equal to a TEXT.
	var stored []byte
	if err := srv.DB.SQL().QueryRowContext(context.Background(),
		`SELECT token_hash FROM share_links WHERE id = ?`, link.ID).Scan(&stored); err != nil {
		t.Fatalf("query: %v", err)
	}
	if string(stored) == link.Token {
		t.Error("the token itself is in the database; a leaked backup hands over live links")
	}
	want := sha256.Sum256([]byte(link.Token))
	if !bytes.Equal(stored, want[:]) {
		t.Errorf("token_hash is %x, want the SHA-256 of the token; the lookup will never "+
			"match and every link is dead on arrival, or it is not a hash at all", stored)
	}

	// And listing never hands one back: there is no "show it again".
	res, err := ts.Client().Get(ts.URL + "/api/settings/shares")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), link.Token) {
		t.Errorf("the list carries the token back:\n%s", body)
	}
	if !strings.Contains(string(body), link.Prefix) {
		t.Errorf("the list has no prefix to name the row by:\n%s", body)
	}
}

// Making and revoking a door onto the panel is a security event.
func TestCreatingAndRevokingAShareLinkIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","detail":"names"}`)
	revokeShare(t, ts, link.ID)

	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"share.created": false, "share.revoked": false}
	for _, e := range entries {
		if _, interesting := want[e.Event]; interesting {
			want[e.Event] = true
		}
	}
	for event, seen := range want {
		if !seen {
			t.Errorf("nothing recorded %s", event)
		}
	}
}

// --allow-from is not something a share link may step around.
//
// The allowlist is the hardening an operator turns on deliberately, and the
// share route is the one place in the panel that answers without a session —
// so leaving the check out of its middleware would have made "make a share
// link" the way to reach the panel from an address that was excluded on
// purpose. The link is valid here; the address is not.
func TestAShareLinkDoesNotBypassTheAllowlist(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	// Made before the allowlist goes up, because creating one needs a session
	// and the session comes from an address that is about to be excluded.
	nets, err := auth.ParseCIDRs([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.Allow = nets

	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a valid link from an excluded address = %d, want 403: %s", res.StatusCode, body)
	}
}

// A route with no credential in front of it is one an outsider can hammer.
func TestAnUnknownShareTokenIsRefusedAndRecorded(t *testing.T) {
	ts, srv := newTestServer(t)

	res, body := shareGET(t, ts, "not-a-real-token-at-all")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an invented token = %d, want 401: %s", res.StatusCode, body)
	}

	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == "share.rejected" {
			return
		}
	}
	t.Error("nothing recorded share.rejected, which is the only sign that a link is being " +
		"guessed at")
}

// The detail mode decides what a link says for as long as it exists, so an
// unrecognised value is refused rather than resolved to whichever branch is
// first in the code -- the same rule the hook installer follows for ?agent=.
func TestShareCreationRefusesAnUnknownDetail(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Post(ts.URL+"/api/settings/shares", "application/json",
		strings.NewReader(`{"name":"wall","detail":"everything"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("detail=everything = %d, want 400", res.StatusCode)
	}

	// And the default is the quiet one, because the default is what a link
	// made in a hurry gets.
	link := newShare(t, ts, `{"name":"wall"}`)
	if link.Detail != string(store.ShareCounts) {
		t.Errorf("the default detail is %q, want %q", link.Detail, store.ShareCounts)
	}
}

// The dashboard carries the numbers it exists for.
//
// Thin on purpose -- the interesting assertions above are all about what is
// absent, and a suite that only checks absence passes on a handler returning an
// empty object.
func TestTheDashboardCarriesTheMachineAndTheSessions(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":[]}`)

	link := newShare(t, ts, `{"name":"wall"}`)
	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dashboard = %d: %s", res.StatusCode, body)
	}
	got := decodeDashboard(t, body)

	if got.At == 0 {
		t.Error("no reading time; the page cannot say when the numbers were last true")
	}
	if got.Machine.Cores == 0 {
		t.Error("no core count")
	}
	if got.Machine.MemTotal == 0 {
		t.Error("no memory reading")
	}
	if got.Counts.Sessions != 1 || len(got.Sessions) != 1 {
		t.Errorf("counts.sessions = %d, rows = %d, want 1 and 1",
			got.Counts.Sessions, len(got.Sessions))
	}
	if got.Counts.Projects != 1 || len(got.Projects) != 1 {
		t.Errorf("counts.projects = %d, groups = %d, want 1 and 1",
			got.Counts.Projects, len(got.Projects))
	}
	if got.Sessions[0].ProjectID != got.Projects[0].ID {
		t.Error("the row's group id matches no group; the wall cannot arrange the rows")
	}
	if got.Sessions[0].Kind == "" {
		t.Error("no kind on the row; the wall cannot tell an agent from a shell")
	}

	// A cache between here and the screen would look exactly like a live
	// dashboard while being none of the things the indicator promises.
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// A scratch terminal is not a task.
//
// Bottom terminals are ordinary session rows with a parent, so a dashboard that
// listed them would report two rows for one job and count a shell sitting at a
// prompt as something that had finished.
func TestTheDashboardLeavesScratchTerminalsOut(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":[]}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":[]}`)

	link := newShare(t, ts, `{"name":"wall"}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if len(got.Sessions) != 1 {
		t.Errorf("%d rows, want 1: a scratch terminal reached the wall", len(got.Sessions))
	}
	if got.Counts.Sessions != 1 {
		t.Errorf("counts.sessions = %d, want 1", got.Counts.Sessions)
	}
}

// ─── boards ────────────────────────────────────────────────────────────────

// A board is data, and this is the line it must not cross.
//
// Every field on a widget is an enum or a bounded number, checked against the
// registry in internal/store. The failure being refused here is the one where a
// stored row starts choosing what the server does: an unknown kind resolved to
// a neighbouring one, a metric that names a column, a range with no ceiling.
// Each case is something a person could type into the request and something the
// database could come to hold.
func TestABoardRefusesAnythingOutsideItsVocabulary(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, bad := range []struct{ body, why string }{
		{`{"name":"w","board":{"widgets":[{"kind":"rm -rf"}]}}`,
			"a widget kind nothing renders"},
		{`{"name":"w","board":{"widgets":[{"kind":"bignumber","metric":"passwordHash"}]}}`,
			"a metric that is not in the list"},
		{`{"name":"w","board":{"widgets":[{"kind":"bignumber"}]}}`,
			"a one-number widget with no number"},
		{`{"name":"w","board":{"widgets":[{"kind":"spendsplit","by":"cwd"}]}}`,
			"a dimension that would name a directory"},
		{`{"name":"w","board":{"widgets":[{"kind":"states","span":99}]}}`,
			"a span outside the grid"},
		{`{"name":"w","board":{"widgets":[{"kind":"spendbars","days":100000}]}}`,
			"a day range with no ceiling"},
		{`{"name":"w","board":{"widgets":[{"kind":"states","text":"hello"}]}}`,
			"free text on a widget that has none"},
		{`{"name":"w","board":{"widgets":[{"kind":"gauge","metric":"cpu","rotate":10}]}}`,
			"a rotation on something with no list"},
		{`{"name":"w","board":{"widgets":[]}}`,
			"a board with nothing on it"},
		{`{"name":"w","preset":"whatever-i-like"}`,
			"a preset that does not exist"},
		{`{"name":"w","board":{"preset":"whatever-i-like","widgets":[{"kind":"states"}]}}`,
			"a preset name smuggled inside the board"},
	} {
		res, err := ts.Client().Post(ts.URL+"/api/settings/shares", "application/json",
			strings.NewReader(bad.body))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400 (%s): %s", bad.body, res.StatusCode, bad.why, body)
		}
	}

	// And a board that is entirely within the vocabulary is accepted, so the
	// loop above is not passing because everything is refused.
	link := newShare(t, ts,
		`{"name":"w","board":{"rotate":20,"widgets":[`+
			`{"kind":"bignumber","metric":"waiting","span":2,"page":0},`+
			`{"kind":"spendsplit","by":"model","span":2,"page":1},`+
			`{"kind":"caption","text":"team A","span":4,"page":1}]}}`)
	if link.Token == "" {
		t.Fatal("a valid board was refused")
	}
}

// The read path drops what it cannot render, and never repairs it.
//
// A row can hold a board this build does not understand: written by a newer
// release, edited by hand, half-written. The dashboard has nobody standing at
// it, so it must keep working — but an unknown kind must never be resolved to
// a known one, because that is a stored string choosing a code path.
func TestAStoredBoardIsRevalidatedOnTheWayOut(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","preset":"single"}`)

	// Straight into the column, past every check the API makes.
	if _, err := srv.DB.SQL().ExecContext(context.Background(),
		`UPDATE share_links SET board = ? WHERE id = ?`,
		`{"preset":"nonsense","widgets":[{"kind":"states","span":2},`+
			`{"kind":"exec","span":4},{"kind":"bignumber","metric":"$(whoami)"}]}`,
		link.ID); err != nil {
		t.Fatal(err)
	}

	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if len(got.Board.Widgets) != 1 || got.Board.Widgets[0].Kind != "states" {
		t.Errorf("the board came back as %+v; the two unrenderable widgets should have been "+
			"dropped and the one good one kept", got.Board.Widgets)
	}
	if got.Board.Preset != "" {
		t.Errorf("preset = %q, want empty: an unknown preset name is not a preset",
			got.Board.Preset)
	}
	if strings.Contains(string(body), "whoami") || strings.Contains(string(body), `"exec"`) {
		t.Errorf("a widget nothing renders reached the client:\n%s", body)
	}

	// And a column that is not JSON at all still leaves a working screen.
	if _, err := srv.DB.SQL().ExecContext(context.Background(),
		`UPDATE share_links SET board = ? WHERE id = ?`, `{{{`, link.ID); err != nil {
		t.Fatal(err)
	}
	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a corrupt board answered %d; a wall display goes dark: %s", res.StatusCode, body)
	}
	if len(decodeDashboard(t, body).Board.Widgets) == 0 {
		t.Error("a corrupt board rendered nothing at all")
	}
}

// A board narrows what a link discloses and can never widen it.
//
// The two halves matter separately. A board with no spend widget must not carry
// the spend section — that is the narrowing. And the widest board possible must
// still carry nothing beyond the fixed structs — that is the ceiling, and it is
// the half that would fail if somebody added a widget that named a field.
func TestABoardCanOnlyNarrowWhatALinkDiscloses(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":[]}`)

	narrow := newShare(t, ts, `{"name":"one","board":{"widgets":[`+
		`{"kind":"bignumber","metric":"waiting"}]}}`)
	_, body := shareGET(t, ts, narrow.Token)
	got := decodeDashboard(t, body)
	if got.Spend != nil {
		t.Error("a board showing one count carries the whole spend section")
	}
	if got.Todos != nil {
		t.Error("a board showing one count carries the checklists")
	}
	if len(got.Sessions) != 0 {
		t.Errorf("a board showing one count carries %d session rows", len(got.Sessions))
	}
	if got.Counts.Sessions != 1 {
		t.Error("the count itself is missing, so the narrowing took the number too")
	}

	// The widest board there is. Every section arrives, and nothing else does.
	wide := newShare(t, ts, `{"name":"all","board":{"widgets":[`+
		`{"kind":"sessionlist"},{"kind":"todos"},{"kind":"spendtotals"},`+
		`{"kind":"spendheatmap"},{"kind":"spendsplit","by":"project"},`+
		`{"kind":"spendsplit","by":"model"},{"kind":"spendsplit","by":"tool"},`+
		`{"kind":"spendbars","by":"month"},{"kind":"spendbars","by":"day"}]}}`)
	_, wideBody := shareGET(t, ts, wide.Token)
	wideGot := decodeDashboard(t, wideBody)
	if wideGot.Spend == nil || wideGot.Todos == nil {
		t.Fatal("the widest board did not get the sections it asked for")
	}
	for _, key := range []string{`"path"`, `"cwd"`, `"command"`, `"tmuxName"`, `"diskPath"`,
		`"agentSession"`, `"scopeId"`} {
		if strings.Contains(string(wideBody), key) {
			t.Errorf("the widest board carries a %s field:\n%s", key, wideBody)
		}
	}
}

// A checklist is counted and never read.
//
// A todo line says what somebody is about to do about a customer, a bug or a
// date. It is the one piece of user text neither detail mode offers, so this
// asserts it in both -- an assertion about `counts` alone would pass on a
// server that disclosed every item under `names`.
func TestTheDashboardCountsTodosAndNeverQuotesThem(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos",
		`{"text":"rotate the production keys before the audit"}`)
	postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos",
		`{"text":"email legal about the acme contract"}`)

	for _, detail := range []string{"counts", "names"} {
		link := newShare(t, ts, `{"name":"w","detail":"`+detail+`","board":{"widgets":[`+
			`{"kind":"todos"}]}}`)
		_, body := shareGET(t, ts, link.Token)
		if strings.Contains(string(body), "production keys") ||
			strings.Contains(string(body), "acme contract") {
			t.Errorf("%s mode quotes a todo item:\n%s", detail, body)
		}
		got := decodeDashboard(t, body)
		if got.Todos == nil || got.Todos.Open != 2 {
			t.Errorf("%s mode counted %+v, want two open items", detail, got.Todos)
		}
	}
}

// ─── scope ─────────────────────────────────────────────────────────────────

// A link about one project is about one project.
//
// The enforcement has to be server-side and it has to survive the request
// asking differently, which is why the scope is on the row and the handler
// reads it from there. Remove the filter and the sessions of every other
// project appear on a link somebody sent to one collaborator.
func TestAScopedLinkSeesOnlyWhatItIsScopedTo(t *testing.T) {
	ts, _ := newTestServer(t)
	mine := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"ours"}`)
	theirs := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"someone else"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+mine.ID+`","title":"our work","command":[]}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+theirs.ID+`","title":"their work","command":[]}`)
	one := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+mine.ID+`","title":"the one thing","command":[]}`)

	byProject := newShare(t, ts, `{"name":"p","detail":"names","scope":"project","scopeId":"`+
		mine.ID+`","board":{"widgets":[{"kind":"sessionlist"}]}}`)
	_, body := shareGET(t, ts, byProject.Token)
	got := decodeDashboard(t, body)
	if len(got.Sessions) != 2 || got.Counts.Sessions != 2 {
		t.Errorf("a project-scoped link saw %d rows and counted %d, want 2 and 2",
			len(got.Sessions), got.Counts.Sessions)
	}
	if strings.Contains(string(body), "their work") {
		t.Errorf("a project-scoped link disclosed another project's session:\n%s", body)
	}
	if got.ScopeName != "ours" {
		t.Errorf("scopeName = %q, want the project's name under names", got.ScopeName)
	}

	bySession := newShare(t, ts, `{"name":"s","detail":"names","scope":"session","scopeId":"`+
		one.ID+`","board":{"widgets":[{"kind":"sessionlist"}]}}`)
	_, sbody := shareGET(t, ts, bySession.Token)
	sgot := decodeDashboard(t, sbody)
	if len(sgot.Sessions) != 1 || sgot.Sessions[0].Name != "the one thing" {
		t.Errorf("a session-scoped link saw %d rows: %+v", len(sgot.Sessions), sgot.Sessions)
	}
	if strings.Contains(string(sbody), "our work") {
		t.Errorf("a session-scoped link disclosed a sibling session:\n%s", sbody)
	}

	// The scope is not a name the counts mode leaks either.
	quiet := newShare(t, ts, `{"name":"p","scope":"project","scopeId":"`+mine.ID+`"}`)
	_, qbody := shareGET(t, ts, quiet.Token)
	if strings.Contains(string(qbody), "ours") || strings.Contains(string(qbody), mine.ID) {
		t.Errorf("a counts-mode scoped link named its project:\n%s", qbody)
	}
}

// A scope that resolves to nothing shows nothing.
//
// The one that would be missed. An empty scope id, an empty path, an empty
// filter -- and an empty filter means "everything". A link sent to one
// collaborator about one project would become a view of every project on the
// machine on the day somebody deleted that project.
func TestAScopedLinkWhoseTargetIsGoneShowsNothing(t *testing.T) {
	ts, srv := newTestServer(t)
	doomed := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"doomed"}`)
	other := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"still here"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+other.ID+`","title":"somebody elses work","command":[]}`)

	postJSON[store.Todo](t, ts, "/api/projects/"+other.ID+"/todos", `{"text":"not yours"}`)
	// Real spend, attributed to the project that survives. Without it the spend
	// assertion below passes on any server: the test ingester has read no
	// transcripts, so every total is zero whether or not the scope was applied.
	seedUsage(t, srv, other.Path, 4242)

	link := newShare(t, ts, `{"name":"p","detail":"names","scope":"project","scopeId":"`+
		doomed.ID+`","board":{"widgets":[{"kind":"sessionlist"},{"kind":"spendtotals"},`+
		`{"kind":"todos"}]}}`)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/projects/"+doomed.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Counts.Sessions != 0 || len(got.Sessions) != 0 {
		t.Errorf("a link scoped to a deleted project shows %d sessions; it has fallen back "+
			"to the whole panel:\n%s", got.Counts.Sessions, body)
	}
	if strings.Contains(string(body), "somebody elses work") {
		t.Errorf("a link scoped to a deleted project discloses another project:\n%s", body)
	}
	if got.Spend == nil {
		t.Fatal("the board asked for spend and got none at all")
	}
	if got.Spend.Window.Total != 0 || got.Spend.Today.Total != 0 {
		t.Errorf("a link scoped to a deleted project reports %d tokens; an empty scope "+
			"became an empty filter, and an empty filter means everything",
			got.Spend.Window.Total)
	}
	if got.Todos != nil && len(got.Todos.Projects) != 0 {
		t.Errorf("a link scoped to a deleted project reports %d checklists",
			len(got.Todos.Projects))
	}
}

// A scope with no id is a scope, not an absence of one.
//
// Unreachable through the API, which refuses it -- and reachable through a
// hand-edited row, a restored backup, or a migration written by somebody who
// defaulted the column to the empty string. It is the same failure as a deleted
// project wearing different clothes: an empty id compared with `==` matches
// nothing, and an empty id treated as "no scope" matches everything.
func TestAScopedLinkWithNoTargetShowsNothing(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"private"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"private work","command":[]}`)
	seedUsage(t, srv, project.Path, 9999)

	link := newShare(t, ts, `{"name":"p","detail":"names","board":{"widgets":[`+
		`{"kind":"sessionlist"},{"kind":"spendtotals"},{"kind":"todos"}]}}`)
	// Straight into the column: scope set, nothing for it to point at.
	if _, err := srv.DB.SQL().ExecContext(context.Background(),
		`UPDATE share_links SET scope = 'project', scope_id = '' WHERE id = ?`,
		link.ID); err != nil {
		t.Fatal(err)
	}

	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Counts.Sessions != 0 || len(got.Sessions) != 0 {
		t.Errorf("a scope pointing at nothing showed %d sessions:\n%s",
			got.Counts.Sessions, body)
	}
	if strings.Contains(string(body), "private work") {
		t.Errorf("a scope pointing at nothing disclosed a session:\n%s", body)
	}
	if got.Spend == nil {
		t.Fatal("the board asked for spend and got none at all")
	}
	if got.Spend.Window.Total != 0 {
		t.Errorf("a scope pointing at nothing reported %d tokens", got.Spend.Window.Total)
	}
}

// seedUsage puts real token spend in the database, attributed to one directory.
//
// A finished pass first, then the rows: the ingester forgets transcripts that
// are not on disk, and these are not. Without the pass, `readable` is false and
// every assertion about a total passes on a server that never applied a filter
// at all -- which is how two mutations survived the first run of this suite.
func seedUsage(t *testing.T, srv *Server, cwd string, tokens int64) {
	t.Helper()
	ctx := context.Background()
	if srv.Tokens == nil {
		t.Fatal("the test server has no ingester, so nothing can be counted")
	}
	if pass := srv.Tokens.RunNow(ctx); pass.At.IsZero() {
		t.Fatal("the pass did not complete, so spend stays unreadable")
	}
	day := time.Now().Format("2006-01-02")
	if err := srv.DB.ReplaceUsageFile(ctx, store.UsageFile{
		Path: filepath.Join(cwd, "transcript.jsonl"), Tool: "claude",
		Size: 1, ModifiedAt: time.Now().Unix(),
		Rows: []store.UsageRow{{
			Day: day, Tool: "claude", Session: "seeded", CWD: cwd,
			Model: "test-model", Input: tokens, Requests: 3,
		}},
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}

// A scope is checked when it is made, not left to fail quietly forever.
func TestShareCreationRefusesAScopeThatNamesNothing(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"name":"w","scope":"project","scopeId":"no-such-project"}`,
		`{"name":"w","scope":"session","scopeId":"no-such-session"}`,
		`{"name":"w","scope":"everything","scopeId":"x"}`,
		`{"name":"w","scope":"project"}`,
		`{"name":"w","scopeId":"dangling"}`,
	} {
		res, err := ts.Client().Post(ts.URL+"/api/settings/shares", "application/json",
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, res.StatusCode)
		}
	}
}

// ─── editing an existing link ──────────────────────────────────────────────

// A board can be rearranged afterwards; what the link may say cannot.
//
// By the time anybody edits a link its URL is in an email or typed into a
// television. Rearranging the board cannot disclose anything the link did not
// already carry. Changing `detail` or `scope` can, and the people holding the
// address would never see it happen -- so those two are fixed at creation, and
// a request that tries is ignored rather than obeyed.
func TestEditingALinkChangesItsBoardAndNothingElse(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"secret project"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"secret work","command":[]}`)

	link := newShare(t, ts, `{"name":"wall","detail":"counts","preset":"single"}`)

	patch := func(body string) int {
		req, err := http.NewRequest(http.MethodPatch,
			ts.URL+"/api/settings/shares/"+link.ID, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := patch(`{"name":"the wall","board":{"widgets":[{"kind":"sessionlist"}]}}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}
	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if got.Name != "the wall" || len(got.Board.Widgets) != 1 ||
		got.Board.Widgets[0].Kind != "sessionlist" {
		t.Errorf("the edit did not take: name %q, board %+v", got.Name, got.Board.Widgets)
	}

	// The fields that would widen it, sent anyway. Refused outright rather than
	// ignored: decode() disallows unknown fields, so a client asking for
	// something the edit surface does not offer is told so instead of getting a
	// 204 that quietly did less than it asked for.
	if code := patch(`{"name":"the wall","detail":"names","scope":"project","scopeId":"` +
		project.ID + `","board":{"widgets":[{"kind":"sessionlist"}]}}`); code !=
		http.StatusBadRequest {
		t.Fatalf("PATCH carrying detail and scope = %d, want 400: the edit surface has grown "+
			"the two fields that widen what a link somebody is already holding discloses",
			code)
	}
	_, body = shareGET(t, ts, link.Token)
	got = decodeDashboard(t, body)
	if got.Detail != "counts" {
		t.Errorf("detail became %q through an edit; a link somebody is already holding "+
			"started using names", got.Detail)
	}
	if got.Scope != "" {
		t.Errorf("scope became %q through an edit", got.Scope)
	}
	if strings.Contains(string(body), "secret work") ||
		strings.Contains(string(body), "secret project") {
		t.Errorf("an edit widened what the link discloses:\n%s", body)
	}

	// And a board that is not a board is refused rather than stored.
	if code := patch(`{"name":"x","board":{"widgets":[{"kind":"nonsense"}]}}`); code !=
		http.StatusBadRequest {
		t.Errorf("PATCH with an unknown widget = %d, want 400", code)
	}
	if code := patch(`{"name":"x"}`); code != http.StatusBadRequest {
		t.Errorf("PATCH with no board = %d, want 400", code)
	}
}

// Editing a link is a change to a door onto the panel, so it is recorded.
func TestEditingAShareLinkIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/settings/shares/"+link.ID,
		strings.NewReader(`{"name":"wall","board":{"widgets":[{"kind":"states"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == "share.updated" {
			return
		}
	}
	t.Error("nothing recorded share.updated")
}

// The catalogue is a settings route, not a share one.
//
// It lists every widget and preset the panel offers, which is harmless in
// itself -- and is exactly the kind of endpoint that gets mounted next to the
// dashboard because it is "just a list". One GET below requireShareToken, still.
func TestTheBoardCatalogueNeedsASessionAndIsNotOnTheShareSurface(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	res, err := ts.Client().Get(ts.URL + "/api/settings/shares/catalogue")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("signed in = %d, want 200", res.StatusCode)
	}
	var cat shareCatalogue
	if err := json.Unmarshal(body, &cat); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cat.Presets) < 10 {
		t.Errorf("%d presets; the point of the catalogue is that there are many",
			len(cat.Presets))
	}
	if len(cat.Widgets) != len(store.KnownWidgetKinds()) {
		t.Errorf("the catalogue lists %d widget kinds and the registry has %d",
			len(cat.Widgets), len(store.KnownWidgetKinds()))
	}

	anon := anonymousClient(t)
	for _, how := range []string{"bearer", "cookie", "none"} {
		req, err := http.NewRequest(http.MethodGet,
			ts.URL+"/api/settings/shares/catalogue", nil)
		if err != nil {
			t.Fatal(err)
		}
		switch how {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+link.Token)
		case "cookie":
			req.Header.Set("Cookie", "vibepanel_session="+link.Token)
		}
		r, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("the catalogue with %s = %d, want 401", how, r.StatusCode)
		}
	}

	// And it is not reachable under the share token's own prefix.
	r, err := anon.Get(ts.URL + "/api/share/" + link.Token + "/catalogue")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode == http.StatusOK {
		t.Error("the catalogue is mounted on the share surface")
	}
}

// Every preset in the catalogue is a board the validator accepts.
//
// A preset that does not validate is one the settings page offers and the
// server then refuses, and the person who finds out is the one who pressed the
// button. Cheap to check and impossible to notice by reading.
func TestEveryPresetIsAValidBoard(t *testing.T) {
	for _, p := range store.Presets() {
		board, ok := store.PresetBoard(p.ID)
		if !ok {
			t.Fatalf("PresetBoard(%q) is not a preset, but Presets() listed it", p.ID)
		}
		if _, err := store.ValidateBoard(board); err != nil {
			t.Errorf("preset %q does not validate: %v", p.ID, err)
		}
	}
}

// The vocabulary a board is built from has to be sayable in both languages.
//
// The strings are the server's: widget kinds, presets, metrics, filters,
// orders, groups and dimensions all come out of internal/store, so the frontend
// cannot type-check them and the untranslated-string test cannot see them -- it
// reads .tsx files and these are ids in a .go one. A kind added without a
// dictionary entry renders as its own identifier on a wall.
func TestEveryBoardWordHasBothLanguages(t *testing.T) {
	const path = "../../web/src/i18n.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so nothing was compared: %v", path, err)
	}
	dict := string(src)

	want := map[string]bool{}
	for _, kind := range store.KnownWidgetKinds() {
		want["board.kind."+kind] = true
		spec, _ := store.WidgetOptions(kind)
		for _, m := range spec.Metrics {
			want["board.metric."+m] = true
		}
		for _, f := range spec.Filters {
			want["board.filter."+f] = true
		}
		for _, o := range spec.Orders {
			want["board.order."+o] = true
		}
		for _, g := range spec.Groups {
			want["board.group."+g] = true
		}
		for _, b := range spec.Bys {
			want["board.by."+b] = true
		}
	}
	for _, p := range store.Presets() {
		want["board.preset."+p.ID] = true
		want["board.presetWhy."+p.ID] = true
		want["board.audience."+p.Audience] = true
	}
	if len(want) < 40 {
		t.Fatalf("only %d words collected; the registry reader has stopped reading", len(want))
	}

	var missing []string
	for key := range want {
		// The dictionary keeps both languages on one line per key, so finding
		// the key finds both -- which is the property that makes a missing
		// translation impossible to introduce by editing one file.
		if !strings.Contains(dict, "'"+key+"'") {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s has no entry for %v. These are ids the server owns; without a "+
			"dictionary entry the dashboard renders the identifier itself.", path, missing)
	}
}
