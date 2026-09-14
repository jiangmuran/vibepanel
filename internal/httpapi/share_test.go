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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A share link is a second door onto a panel whose first door is one password
// in front of a writable terminal. Every test in this file is about the shape
// of that door rather than about what a page looks like.

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

// widestManifest asks for every section with every option, so a link made on
// it carries everything a link can carry. The redaction tests use it because a
// sweep for what must never be sent is only worth something against the most
// that is.
const widestManifest = `{"sdk":1,"name":"Everything",
	"sections":["sessions","todos","spend","trend","flow","feed","repo"],
	"spend":{"days":30,"months":true,"heatmap":true,"split":["tool","model","project"]},
	"repo":{"days":14,"prs":true},"flow":{"by":"hour"}}`

// newShare mints a link through the real endpoint, as the signed-in owner, on
// a published page with widestManifest unless the body names a page.
func newShare(t *testing.T, ts *httptest.Server, body string) freshShare {
	t.Helper()
	return shareOn(t, ts, widestManifest, body)
}

// shareOn mints a link on a freshly published page with this manifest.
func shareOn(t *testing.T, ts *httptest.Server, manifest, body string) freshShare {
	t.Helper()
	return postJSON[freshShare](t, ts, "/api/settings/shares", withPage(t, ts, manifest, body))
}

// withPage adds a published page with this manifest to a create body that
// does not name one.
func withPage(t *testing.T, ts *httptest.Server, manifest, body string) string {
	t.Helper()
	if strings.Contains(body, `"pageId"`) {
		return body
	}
	p := newPublishedPage(t, ts, manifest, nil)
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body), "{"))
	if rest == "}" {
		return `{"pageId":"` + p.page.ID + `"}`
	}
	return `{"pageId":"` + p.page.ID + `",` + rest
}

// shareGET fetches the snapshot the way a wall display does: no cookie, no
// header, nothing but the URL.
func shareGET(t *testing.T, ts *httptest.Server, token string) (*http.Response, []byte) {
	t.Helper()
	res, err := anonymousClient(t).Get(ts.URL + "/api/share/" + token + "/v1/snapshot")
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return res, body
}

func decodeSnapshot(t *testing.T, body []byte) shareSnapshot {
	t.Helper()
	var out shareSnapshot
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode snapshot: %v: %s", err, body)
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
func TestAShareTokenReachesItsSnapshotAndNothingElse(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	link := newShare(t, ts, `{"name":"wall","detail":"counts"}`)

	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the snapshot itself = %d, want 200: %s", res.StatusCode, body)
	}
	if got := decodeSnapshot(t, body); got.Name != "wall" {
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

	// And the share surface is GETs. Anything else under it is not a narrower
	// version of the snapshot, it is a route that does not exist -- the
	// dashboard boards drew from included.
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/api/share/" + link.Token + "/v1/snapshot"},
		{http.MethodDelete, "/api/share/" + link.Token + "/v1/snapshot"},
		{http.MethodGet, "/api/share/" + link.Token + "/dashboard"},
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
			t.Errorf("%s %s succeeded; the share surface is the snapshot and a page's files",
				probe.method, probe.path)
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
func TestTheSnapshotNeverCarriesAPathACommandOrARealID(t *testing.T) {
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
			t.Fatalf("%s: snapshot = %d: %s", detail, res.StatusCode, body)
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

		got := decodeSnapshot(t, body)
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
			// A repository is a name, and a public one. Under counts the link
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

// A link that is about one project may say which repository, and only then.
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
func TestARepositoryIsNamedOnlyOnAProjectLinkThatAlreadyNamesThings(t *testing.T) {
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

	read := func(t *testing.T, body string) (shareSnapshot, string) {
		t.Helper()
		link := newShare(t, ts, body)
		res, raw := shareGET(t, ts, link.Token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("snapshot = %d: %s", res.StatusCode, raw)
		}
		return decodeSnapshot(t, raw), string(raw)
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
		// No single project, so no single repository. A link covering three
		// projects that named one of their repositories would be worse than
		// naming none.
		got, _ := read(t, `{"name":"wall","detail":"names"}`)
		if got.ScopeRepoOwner != "" || got.ScopeRepoName != "" {
			t.Fatalf("an unscoped link named %q/%q", got.ScopeRepoOwner, got.ScopeRepoName)
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
			t.Fatalf("a session link named %q/%q", got.ScopeRepoOwner, got.ScopeRepoName)
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
// obvious way to put a repository's name on a screen is s.Git.Read, which is the
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

	link := shareOn(t, ts, `{"sdk":1,"name":"Machine","sections":[]}`,
		`{"name":"wall","detail":"names","scope":"project","scopeId":"`+project.ID+`"}`)
	const polls = 6
	for i := 0; i < polls; i++ {
		// Past the one-second memo, so each poll is a build and not a copy of
		// the first: the property is about what a build runs.
		srv.snapshots.entries = nil
		res, raw := shareGET(t, ts, link.Token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("snapshot = %d: %s", res.StatusCode, raw)
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

// A page that asks for a spend series gets one as long as it asked.
//
// The section is computed because the manifest named it, and the *width* of
// the day series comes from the same manifest. When the two were decided in
// two places, one of them could say "wanted" and the other "zero days", and the
// page rendered "nothing to show" forever beside a spend total that was not
// zero.
func TestAPageAskingForASpendSeriesGetsIt(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"Acme"}`)
	seedUsage(t, srv, project.Path, 4242)

	link := shareOn(t, ts, `{"sdk":1,"name":"Spend","sections":["spend"],"spend":{"days":7}}`,
		`{"name":"wall"}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeSnapshot(t, body)
	if got.Spend == nil {
		t.Fatal("a page asking for spend carries no spend section at all")
	}
	if !got.Spend.Readable || got.Spend.Today.Total == 0 {
		t.Fatalf("nothing was counted, so this test would pass on any server: "+
			"readable=%v today=%d", got.Spend.Readable, got.Spend.Today.Total)
	}
	if len(got.Spend.Days) == 0 || len(got.Spend.Days) > 7 {
		t.Errorf("the page asked for seven days and was sent %d, while its own totals "+
			"say %d tokens were spent today", len(got.Spend.Days), got.Spend.Today.Total)
	}
	if len(got.Spend.Months) != 0 || len(got.Spend.Heatmap) != 0 || len(got.Spend.Tools) != 0 {
		t.Errorf("a page asking for days alone was sent months, the heatmap or the tools: %s", body)
	}
}

// daysOr is the last bound on a day range before a query sees it. A stored
// manifest is decoded leniently, so the bound cannot rest on publish alone.
func TestADayRangeIsBoundedWhereverItCameFrom(t *testing.T) {
	for _, tc := range []struct{ days, fallback, want int }{
		{0, 14, 14},
		{-3, 14, 14},
		{30, 14, 30},
		{pages.MaxDays, 14, pages.MaxDays},
		{pages.MaxDays + 1, 14, pages.MaxDays},
		{1 << 30, 14, pages.MaxDays},
	} {
		if got := daysOr(tc.days, tc.fallback); got != tc.want {
			t.Errorf("daysOr(%d, %d) = %d, want %d", tc.days, tc.fallback, got, tc.want)
		}
	}
}

// The row ids are per link, and are not the panel's.
//
// Stable, because a list that re-keys itself every two seconds re-mounts every
// row; different between links, so two screens on two walls cannot be
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
			t.Fatalf("snapshot = %d: %s", res.StatusCode, body)
		}
		out := []string{}
		for _, row := range decodeSnapshot(t, body).Sessions {
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
		strings.NewReader(withPage(t, ts, sessionsManifest, `{"name":"wall","detail":"everything"}`)))
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

// The snapshot carries the numbers it exists for.
//
// Thin on purpose -- the interesting assertions above are all about what is
// absent, and a suite that only checks absence passes on a handler returning an
// empty object.
func TestTheSnapshotCarriesTheMachineAndTheSessions(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":[]}`)

	link := newShare(t, ts, `{"name":"wall"}`)
	res, body := shareGET(t, ts, link.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("snapshot = %d: %s", res.StatusCode, body)
	}
	got := decodeSnapshot(t, body)

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
	// screen while being none of the things the indicator promises.
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// A scratch terminal is not a task.
//
// Bottom terminals are ordinary session rows with a parent, so a snapshot that
// listed them would report two rows for one job and count a shell sitting at a
// prompt as something that had finished.
func TestTheSnapshotLeavesScratchTerminalsOut(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":[]}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":[]}`)

	link := newShare(t, ts, `{"name":"wall"}`)
	_, body := shareGET(t, ts, link.Token)
	got := decodeSnapshot(t, body)
	if len(got.Sessions) != 1 {
		t.Errorf("%d rows, want 1: a scratch terminal reached the wall", len(got.Sessions))
	}
	if got.Counts.Sessions != 1 {
		t.Errorf("counts.sessions = %d, want 1", got.Counts.Sessions)
	}
}

// ─── what a page asks for ──────────────────────────────────────────────────

// A page narrows what a link discloses and can never widen it.
//
// The two halves matter separately. A page with no spend section must not carry
// the spend section -- that is the narrowing. And the widest manifest possible
// must still carry nothing beyond the fixed structs -- that is the ceiling, and
// it is the half that would fail if somebody added a manifest option that named
// a field.
func TestAPageCanOnlyNarrowWhatALinkDiscloses(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":[]}`)

	narrow := shareOn(t, ts, `{"sdk":1,"name":"One","sections":[]}`, `{"name":"one"}`)
	_, body := shareGET(t, ts, narrow.Token)
	got := decodeSnapshot(t, body)
	for name, present := range map[string]bool{
		"spend": got.Spend != nil, "todos": got.Todos != nil, "trend": got.Trend != nil,
		"flow": got.Flow != nil, "feed": got.Feed != nil, "repo": got.Repo != nil,
		"sessions": len(got.Sessions) != 0,
	} {
		if present {
			t.Errorf("a page asking for no sections carries %s:\n%s", name, body)
		}
	}
	if len(got.Sections) != 0 {
		t.Errorf("sections = %v, want none", got.Sections)
	}
	if got.Counts.Sessions != 1 {
		t.Error("the count itself is missing, so the narrowing took the number too")
	}

	// The widest page there is. Every section arrives, and nothing else does.
	wide := newShare(t, ts, `{"name":"all"}`)
	_, wideBody := shareGET(t, ts, wide.Token)
	wideGot := decodeSnapshot(t, wideBody)
	if wideGot.Spend == nil || wideGot.Todos == nil || wideGot.Flow == nil ||
		wideGot.Feed == nil || wideGot.Repo == nil || wideGot.Trend == nil || len(wideGot.Sessions) == 0 {
		t.Fatalf("the widest page did not get the sections it asked for:\n%s", wideBody)
	}
	for _, key := range []string{`"path"`, `"cwd"`, `"command"`, `"tmuxName"`, `"diskPath"`,
		`"agentSession"`, `"scopeId"`, `"board"`, `"locked"`} {
		if strings.Contains(string(wideBody), key) {
			t.Errorf("the widest page carries a %s field:\n%s", key, wideBody)
		}
	}
}

// A link draws a page. There is no other thing for it to draw, so a request
// that names none is a mistake worth a 400 rather than a link that shows nothing.
func TestALinkCannotBeMadeWithoutAPage(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"name":"wall"}`,
		`{"name":"wall","pageId":""}`,
		`{"name":"wall","pageId":"no-such-page"}`,
		`{"name":"wall","preset":"attention"}`,
	} {
		status, out := callJSON(t, ts, http.MethodPost, "/api/settings/shares", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, status, out)
		}
	}
	if links := listShares(t, ts); len(links) != 0 {
		t.Errorf("%d links were stored by refused requests", len(links))
	}

	// Nor can an existing link be pointed at nothing afterwards.
	link := newShare(t, ts, `{"name":"wall"}`)
	if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+link.ID+"/page",
		`{"pageId":""}`); status != http.StatusBadRequest {
		t.Errorf("pointing a link at no page = %d, want 400: %s", status, out)
	}
	if status, _ := anonGET(t, ts, "/share/"+link.Token+"/"); status.StatusCode != http.StatusOK {
		t.Errorf("after a refused re-point the link's page = %d", status.StatusCode)
	}
}

// A checklist is counted and never read.
//
// A todo line says what somebody is about to do about a customer, a bug or a
// date. It is the one piece of user text neither detail mode offers, so this
// asserts it in both -- an assertion about `counts` alone would pass on a
// server that disclosed every item under `names`.
func TestTheSnapshotCountsTodosAndNeverQuotesThem(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos",
		`{"text":"rotate the production keys before the audit"}`)
	postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos",
		`{"text":"email legal about the acme contract"}`)

	for _, detail := range []string{"counts", "names"} {
		link := shareOn(t, ts, `{"sdk":1,"name":"Todos","sections":["todos"]}`,
			`{"name":"w","detail":"`+detail+`"}`)
		_, body := shareGET(t, ts, link.Token)
		if strings.Contains(string(body), "production keys") ||
			strings.Contains(string(body), "acme contract") {
			t.Errorf("%s mode quotes a todo item:\n%s", detail, body)
		}
		got := decodeSnapshot(t, body)
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

	byProject := shareOn(t, ts, sessionsManifest, `{"name":"p","detail":"names","scope":"project","scopeId":"`+
		mine.ID+`"}`)
	_, body := shareGET(t, ts, byProject.Token)
	got := decodeSnapshot(t, body)
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

	bySession := shareOn(t, ts, sessionsManifest, `{"name":"s","detail":"names","scope":"session","scopeId":"`+
		one.ID+`"}`)
	_, sbody := shareGET(t, ts, bySession.Token)
	sgot := decodeSnapshot(t, sbody)
	if len(sgot.Sessions) != 1 || sgot.Sessions[0].Name != "the one thing" {
		t.Errorf("a session-scoped link saw %d rows: %+v", len(sgot.Sessions), sgot.Sessions)
	}
	if strings.Contains(string(sbody), "our work") {
		t.Errorf("a session-scoped link disclosed a sibling session:\n%s", sbody)
	}

	// The scope is not a name the counts mode leaks either.
	// On a page of sessions alone: the widest one carries "hoursToday", which
	// would find "ours" in a body that names nothing.
	quiet := shareOn(t, ts, sessionsManifest, `{"name":"p","scope":"project","scopeId":"`+mine.ID+`"}`)
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

	link := shareOn(t, ts, `{"sdk":1,"name":"P","sections":["sessions","spend","todos"]}`,
		`{"name":"p","detail":"names","scope":"project","scopeId":"`+doomed.ID+`"}`)

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
	got := decodeSnapshot(t, body)
	if got.Counts.Sessions != 0 || len(got.Sessions) != 0 {
		t.Errorf("a link scoped to a deleted project shows %d sessions; it has fallen back "+
			"to the whole panel:\n%s", got.Counts.Sessions, body)
	}
	if strings.Contains(string(body), "somebody elses work") {
		t.Errorf("a link scoped to a deleted project discloses another project:\n%s", body)
	}
	if got.Spend == nil {
		t.Fatal("the page asked for spend and got none at all")
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

	link := shareOn(t, ts, `{"sdk":1,"name":"P","sections":["sessions","spend","todos"]}`,
		`{"name":"p","detail":"names"}`)
	// Straight into the column: scope set, nothing for it to point at.
	if _, err := srv.DB.SQL().ExecContext(context.Background(),
		`UPDATE share_links SET scope = 'project', scope_id = '' WHERE id = ?`,
		link.ID); err != nil {
		t.Fatal(err)
	}

	_, body := shareGET(t, ts, link.Token)
	got := decodeSnapshot(t, body)
	if got.Counts.Sessions != 0 || len(got.Sessions) != 0 {
		t.Errorf("a scope pointing at nothing showed %d sessions:\n%s",
			got.Counts.Sessions, body)
	}
	if strings.Contains(string(body), "private work") {
		t.Errorf("a scope pointing at nothing disclosed a session:\n%s", body)
	}
	if got.Spend == nil {
		t.Fatal("the page asked for spend and got none at all")
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
	// On a real page, so what refuses each of these is the scope and not a
	// missing page.
	page := newPublishedPage(t, ts, sessionsManifest, nil)
	for _, body := range []string{
		`{"name":"w","scope":"project","scopeId":"no-such-project"}`,
		`{"name":"w","scope":"session","scopeId":"no-such-session"}`,
		`{"name":"w","scope":"everything","scopeId":"x"}`,
		`{"name":"w","scope":"project"}`,
		`{"name":"w","scopeId":"dangling"}`,
	} {
		body = `{"pageId":"` + page.page.ID + `",` + strings.TrimPrefix(body, "{")
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

// A link can be renamed and relabelled afterwards; what it may say cannot.
//
// By the time anybody edits a link its URL is in an email or typed into a
// television. A name or a remark discloses nothing the link did not already
// carry. Changing `detail` or `scope` can, and the people holding the address
// would never see it happen -- so those two are fixed at creation, and a
// request that tries is refused rather than obeyed.
func TestEditingALinkChangesItsNameAndNothingElse(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"secret project"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"secret work","command":[]}`)

	link := newShare(t, ts, `{"name":"wall","detail":"counts"}`)

	if code := patchShare(t, ts, link.ID, `{"name":"the wall","remark":"lobby"}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}
	_, body := shareGET(t, ts, link.Token)
	got := decodeSnapshot(t, body)
	if got.Name != "the wall" || got.Remark != "lobby" {
		t.Errorf("the edit did not take: name %q, remark %q", got.Name, got.Remark)
	}

	// The fields that would widen it, sent anyway. Refused outright rather than
	// ignored: decode() disallows unknown fields, so a client asking for
	// something the edit surface does not offer is told so instead of getting a
	// 204 that quietly did less than it asked for.
	for _, wider := range []string{
		`{"name":"the wall","detail":"names"}`,
		`{"name":"the wall","scope":"project","scopeId":"` + project.ID + `"}`,
		`{"name":"the wall","board":{"widgets":[{"kind":"sessionlist"}]}}`,
	} {
		if code := patchShare(t, ts, link.ID, wider); code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400: the edit surface has grown a field that "+
				"changes what a link somebody is already holding shows", wider, code)
		}
	}
	_, body = shareGET(t, ts, link.Token)
	got = decodeSnapshot(t, body)
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

	// An empty name keeps the one it had rather than blanking a heading.
	if code := patchShare(t, ts, link.ID, `{"name":""}`); code != http.StatusNoContent {
		t.Fatalf("PATCH with no name = %d, want 204", code)
	}
	if _, body := shareGET(t, ts, link.Token); decodeSnapshot(t, body).Name != "the wall" {
		t.Error("an edit with no name blanked the link's name")
	}
}

// Editing a link is a change to a door onto the panel, so it is recorded.
func TestEditingAShareLinkIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/settings/shares/"+link.ID,
		strings.NewReader(`{"name":"wall"}`))
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
