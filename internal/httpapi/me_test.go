package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A session's door (me.go): the derived token its environment holds opens
// /api/me/v1/ for that session and nothing else.

// meSession makes a project and a session as the owner and returns the
// session with the token its environment would hold.
func meSession(t *testing.T, ts *httptest.Server, srv *Server) (store.Session, string) {
	t.Helper()
	status, body := doJSON(t, ts, http.MethodPost, "/api/projects", `{"name":"me","path":"`+t.TempDir()+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("project: %d %s", status, body)
	}
	var p store.Project
	_ = json.Unmarshal(body, &p)
	status, body = doJSON(t, ts, http.MethodPost, "/api/sessions", `{"projectId":"`+p.ID+`","title":"agent","command":["sleep","60"]}`)
	if status != http.StatusCreated {
		t.Fatalf("session: %d %s", status, body)
	}
	var sess store.Session
	_ = json.Unmarshal(body, &sess)
	root, err := srv.HookToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return sess, hooks.ReportToken(root, sess.ID)
}

func meCall(t *testing.T, ts *httptest.Server, method, path, sessionID, token, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Vibepanel-Session", sessionID)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 1<<16)
	for {
		n, rerr := res.Body.Read(buf)
		raw.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return res.StatusCode, []byte(raw.String())
}

func TestASessionSaysWhatItIsUpTo(t *testing.T) {
	ts, srv := newTestServer(t)
	sess, token := meSession(t, ts, srv)
	status, body := meCall(t, ts, http.MethodGet, "/api/me/v1", sess.ID, token, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"project":"me"`) || !strings.Contains(string(body), `"title":"agent"`) {
		t.Fatalf("me: %d %s", status, body)
	}
	status, body = meCall(t, ts, http.MethodPatch, "/api/me/v1", sess.ID, token,
		`{"summary":"  migrating the schema  ","progress":140,"labels":["db"," migration ","db","","x","y","z","w"],"state":"waiting","why":"need a decision on the old table"}`)
	if status != http.StatusOK {
		t.Fatalf("patch: %d %s", status, body)
	}
	row, err := srv.DB.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Summary != "migrating the schema" || row.Progress != 100 || len(row.Labels) != 5 || row.State != "waiting" {
		t.Errorf("row: summary %q progress %d labels %v state %s", row.Summary, row.Progress, row.Labels, row.State)
	}
	if last, ok, _ := srv.DB.LatestSessionMessage(t.Context(), sess.ID); !ok || last.Kind != store.MessageNotice || last.Text != "need a decision on the old table" {
		t.Errorf("the why is not the session's last notice: %+v %v", last, ok)
	}
	// The owner sees it in the state.
	_, body = doJSON(t, ts, http.MethodGet, "/api/state", "")
	if !strings.Contains(string(body), `"summary":"migrating the schema"`) || !strings.Contains(string(body), `"progress":100`) {
		t.Errorf("state: %s", body)
	}
	// Clearing: an empty summary, a negative progress, an empty label list.
	status, _ = meCall(t, ts, http.MethodPatch, "/api/me/v1", sess.ID, token, `{"summary":"","progress":-1,"labels":[]}`)
	if status != http.StatusOK {
		t.Fatal("clear")
	}
	row, _ = srv.DB.GetSession(t.Context(), sess.ID)
	if row.Summary != "" || row.Progress != -1 || len(row.Labels) != 0 {
		t.Errorf("after clearing: %q %d %v", row.Summary, row.Progress, row.Labels)
	}
	if status, _ = meCall(t, ts, http.MethodPatch, "/api/me/v1", sess.ID, token, `{"state":"sleeping"}`); status != http.StatusBadRequest {
		t.Errorf("an unknown state: %d", status)
	}
}

// The token names one session: another session's id, the root token, no
// header, and every other route are refused.
func TestASessionTokenIsItsSessionsAlone(t *testing.T) {
	ts, srv := newTestServer(t)
	sess, token := meSession(t, ts, srv)
	status, body := doJSON(t, ts, http.MethodPost, "/api/sessions", `{"projectId":"`+sess.ProjectID+`","title":"other","command":["sleep","60"]}`)
	if status != http.StatusCreated {
		t.Fatalf("second session: %d %s", status, body)
	}
	var other store.Session
	_ = json.Unmarshal(body, &other)
	if status, _ := meCall(t, ts, http.MethodPatch, "/api/me/v1", other.ID, token, `{"title":"hijacked"}`); status != http.StatusUnauthorized {
		t.Errorf("another session's id with this token: %d", status)
	}
	root, _ := srv.HookToken(t.Context())
	if status, _ := meCall(t, ts, http.MethodGet, "/api/me/v1", sess.ID, root, ""); status != http.StatusUnauthorized {
		t.Errorf("the root hook token: %d", status)
	}
	if status, _ := meCall(t, ts, http.MethodGet, "/api/me/v1", "", token, ""); status != http.StatusUnauthorized {
		t.Errorf("no session header: %d", status)
	}
	for _, p := range []string{"/api/state", "/api/sessions/" + sess.ID + "/screen", "/api/projects/" + sess.ProjectID + "/notes", "/api/settings/tokens", "/api/plugin/" + token + "/v1/view"} {
		if status, _ := meCall(t, ts, http.MethodGet, p, sess.ID, token, ""); status != http.StatusUnauthorized && status != http.StatusNotFound {
			t.Errorf("%s with a session token: %d", p, status)
		}
	}
	if row, _ := srv.DB.GetSession(t.Context(), other.ID); row.Title != "other" {
		t.Errorf("the other session was renamed: %q", row.Title)
	}
}

// The door is two routes, and this list is where a third is decided.
func TestASessionTokenReachesOnlyTheseRoutes(t *testing.T) {
	_, srv := newTestServer(t)
	var got []string
	_ = chi.Walk(srv.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/me/") {
			got = append(got, method+" "+route)
		}
		return nil
	})
	sort.Strings(got)
	want := []string{"GET /api/me/v1", "PATCH /api/me/v1", "POST /api/me/v1/notify", "POST /api/me/v1/say", "POST /api/me/v1/send"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("a session's door is %v, want %v: a route added here is one every agent in every session can reach", got, want)
	}
}

// say and notify from inside: a line in the session's messages, an audit
// row with the byte count and not the words, and a rate: the seventh notice
// in a minute is a 429 with a Retry-After.
func TestASessionSaysAndNotifies(t *testing.T) {
	ts, srv := newTestServer(t)
	sess, token := meSession(t, ts, srv)
	if status, body := meCall(t, ts, http.MethodPost, "/api/me/v1/say", sess.ID, token, `{"text":"the migration finished"}`); status != http.StatusNoContent {
		t.Fatalf("say: %d %s", status, body)
	}
	if last, ok, _ := srv.DB.LatestSessionMessage(t.Context(), sess.ID); !ok || last.Text != "the migration finished" || last.Kind != store.MessageNotice {
		t.Errorf("after say: %+v", last)
	}
	status, body := meCall(t, ts, http.MethodPost, "/api/me/v1/notify", sess.ID, token, `{"text":"drop the old table?","choices":["drop","keep","a very long choice that gets cut"]}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"contacts":0`) {
		t.Fatalf("notify: %d %s", status, body)
	}
	for i := 0; i < noticeRateNotify; i++ {
		meCall(t, ts, http.MethodPost, "/api/me/v1/notify", sess.ID, token, `{"text":"again"}`)
	}
	status, _ = meCall(t, ts, http.MethodPost, "/api/me/v1/notify", sess.ID, token, `{"text":"once more"}`)
	if status != http.StatusTooManyRequests {
		t.Errorf("the notice after the limit: %d", status)
	}
	if status, _ := meCall(t, ts, http.MethodPost, "/api/me/v1/say", sess.ID, token, `{"text":"   "}`); status != http.StatusBadRequest {
		t.Errorf("an empty say: %d", status)
	}
	// The owner's door to the same thing.
	if status, body := doJSON(t, ts, http.MethodPost, "/api/sessions/"+sess.ID+"/say", `{"text":"from the owner"}`); status != http.StatusNoContent {
		t.Errorf("owner say: %d %s", status, body)
	}
	entries, _ := srv.DB.RecentAudit(t.Context(), 50)
	said, notified := 0, 0
	for _, e := range entries {
		if e.Event == "session.said" {
			said++
			if strings.Contains(e.Detail, "migration") {
				t.Errorf("the audit row carries the words: %s", e.Detail)
			}
		}
		if e.Event == "session.notified" {
			notified++
		}
	}
	// The limit is six a minute: the first and five more are audited, the
	// seventh is the 429 above and leaves no row.
	if said != 2 || notified != noticeRateNotify {
		t.Errorf("audit: said %d notified %d", said, notified)
	}
}

// A file from inside: kept under the session's directory, served inline
// when it is an image and as a download otherwise, HTML included.
func TestASessionSendsAFile(t *testing.T) {
	ts, srv := newTestServer(t)
	sess, token := meSession(t, ts, srv)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	upload := func(name string, data []byte, caption string) (int, []byte) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("caption", caption)
		part, _ := mw.CreateFormFile("file", name)
		_, _ = part.Write(data)
		_ = mw.Close()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/me/v1/send", &body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Vibepanel-Session", sess.ID)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res, err := anonymousClient(t).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	status, body := upload("../../chart one.png", png, "coverage")
	if status != http.StatusCreated || !strings.Contains(string(body), `"name":"chart_one.png"`) || !strings.Contains(string(body), "/api/sessions/"+sess.ID+"/attachments/chart_one.png") {
		t.Fatalf("send: %d %s", status, body)
	}
	if _, err := os.Stat(filepath.Join(srv.attachmentsDir(sess.ID), "chart_one.png")); err != nil {
		t.Fatal(err)
	}
	// The same name again is a second file, not a replacement.
	if status, body := upload("chart one.png", png, ""); status != http.StatusCreated || !strings.Contains(string(body), `"chart_one-2.png"`) {
		t.Errorf("second: %d %s", status, body)
	}
	if status, _ := upload("page.html", []byte("<script>alert(1)</script>"), ""); status != http.StatusCreated {
		t.Fatal("html upload")
	}
	// Served to the owner: the image inline, the page as a download.
	res, _ := ts.Client().Get(ts.URL + "/api/sessions/" + sess.ID + "/attachments/chart_one.png")
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/png") || res.Header.Get("Content-Disposition") != "" {
		t.Errorf("image: %d %v", res.StatusCode, res.Header)
	}
	res, _ = ts.Client().Get(ts.URL + "/api/sessions/" + sess.ID + "/attachments/page.html")
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") || !contains(res.Header.Values("Content-Security-Policy"), "sandbox") {
		t.Errorf("html: %d %v", res.StatusCode, res.Header)
	}
	// Not for a stranger, not for the session's own token.
	if r, _ := anonGET(t, ts, "/api/sessions/"+sess.ID+"/attachments/chart_one.png"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.StatusCode)
	}
	if status, _ := meCall(t, ts, http.MethodGet, "/api/sessions/"+sess.ID+"/attachments/chart_one.png", sess.ID, token, ""); status != http.StatusUnauthorized {
		t.Errorf("the session token reading its own attachment through the owner's route: %d", status)
	}
	_, body = doJSON(t, ts, http.MethodGet, "/api/sessions/"+sess.ID+"/attachments", "")
	if !strings.Contains(string(body), `"chart_one.png"`) || !strings.Contains(string(body), `"page.html"`) {
		t.Errorf("list: %s", body)
	}
	// Too big is refused and leaves nothing.
	if status, _ := upload("big.bin", bytes.Repeat([]byte{1}, attachmentMaxBytes+1), ""); status != http.StatusRequestEntityTooLarge {
		t.Errorf("over the cap: %d", status)
	}
	if _, err := os.Stat(filepath.Join(srv.attachmentsDir(sess.ID), "big.bin")); err == nil {
		t.Error("the oversized file was kept")
	}
}

// The me door resolves a credential, so the address allowlist applies to it
// like every capability surface: an address the operator excluded must not
// reach it just because the route is new (AUDIT.md #1's shape).
func TestTheMeDoorHonoursTheAddressAllowlist(t *testing.T) {
	ts, srv := newTestServer(t)
	row, token := meSession(t, ts, srv)
	nets, err := auth.ParseCIDRs([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.Allow = nets
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/me/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(meSessionHeader, row.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("me from an excluded address = %d, want 403", res.StatusCode)
	}
}
