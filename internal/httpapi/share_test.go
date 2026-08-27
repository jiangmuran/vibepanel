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
		for _, key := range []string{`"path"`, `"cwd"`, `"command"`, `"tmuxName"`, `"diskPath"`} {
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
