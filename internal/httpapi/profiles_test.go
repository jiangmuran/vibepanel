package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

func send(t *testing.T, ts *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func listProfiles(t *testing.T, ts *httptest.Server) []store.LaunchProfile {
	t.Helper()
	code, body := send(t, ts, http.MethodGet, "/api/launch-profiles", "")
	if code != http.StatusOK {
		t.Fatalf("GET launch-profiles: %d %s", code, body)
	}
	var out []store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestAFreshPanelAlreadyOffersProfiles(t *testing.T) {
	ts, _ := newTestServer(t)
	got := listProfiles(t, ts)
	if len(got) != len(store.BuiltinLaunchProfiles()) {
		t.Fatalf("a panel with no rows offers %d profiles; the built-ins are the "+
			"reason the feature is usable the moment it exists", len(got))
	}
	if got[0].ID != store.BuiltinShell {
		t.Errorf("the first option is %s; the shell is what the button did before "+
			"there was anything to choose, and hiding it below an agent changes "+
			"what one click does", got[0].ID)
	}
}

// TestABuiltinIsEditedInPlaceAndCanBeRestored.
//
// This was TestABuiltinCannotBeEditedOrRemoved, and both refusals were removed
// on purpose: 「自带的几个 agent 没法删啊现在」, and reordering a list you cannot
// take anything out of is arranging clutter.
//
// The reason the refusals gave was real and is answered rather than dropped --
// copy-on-write would mean the catalogue a release ships stops being the
// catalogue people have, one panel at a time, with nothing saying so. What
// makes that acceptable now is that the override is visible (`overridden` on
// the row, said on the settings page) and one action puts it back. What is
// checked here is the part that would make it silent again: the count.
func TestABuiltinIsEditedInPlaceAndCanBeRestored(t *testing.T) {
	ts, srv := newTestServer(t)
	before := len(listProfiles(t, ts))

	code, body := send(t, ts, http.MethodPatch, "/api/launch-profiles/"+store.BuiltinShell,
		`{"name":"mine","command":[],"env":[]}`)
	if code != http.StatusNoContent {
		t.Fatalf("PATCH a built-in: %d %s", code, body)
	}
	// One row, not two. An override that appeared *beside* the catalogue entry
	// would show the same profile twice and the picker is where you would find
	// out.
	if got := len(listProfiles(t, ts)); got != before {
		t.Errorf("editing a built-in changed the count from %d to %d", before, got)
	}

	code, body = send(t, ts, http.MethodDelete, "/api/launch-profiles/"+store.BuiltinShell, "")
	if code != http.StatusNoContent {
		t.Fatalf("DELETE a built-in: %d %s", code, body)
	}
	if got := len(listProfiles(t, ts)); got != before-1 {
		t.Errorf("after removing one, the list has %d, want %d", got, before-1)
	}
	// And the override row goes with it. Nothing on screen would show it
	// staying -- hidden filters it out of the list either way -- but a row
	// nobody can see still spends one of the sixty-four a picker is allowed,
	// and the way that surfaces is somebody being told they have too many
	// profiles while looking at fewer than they have.
	if n, err := srv.DB.CountLaunchProfiles(context.Background()); err != nil || n != 0 {
		t.Errorf("removing an edited built-in left %d rows (err %v), want none", n, err)
	}

	code, body = send(t, ts, http.MethodPost, "/api/launch-profiles/restore", `{}`)
	if code != http.StatusNoContent {
		t.Fatalf("restore: %d %s", code, body)
	}
	list := listProfiles(t, ts)
	if len(list) != before {
		t.Errorf("restore left %d profiles, want %d", len(list), before)
	}
	for _, p := range list {
		if p.ID == store.BuiltinShell && p.Name == "mine" {
			t.Error("restore put the list back but kept the edit")
		}
	}
}

func TestAProfileRoundTripsWithoutItsKey(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"gateway","command":["claude"],"env":[
			{"name":"ANTHROPIC_BASE_URL","value":"https://gw.example/v1"},
			{"name":"ANTHROPIC_AUTH_TOKEN","value":"sk-secret","secret":true}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, body)
	}
	if strings.Contains(body, "sk-secret") {
		t.Fatalf("the response to a create echoed the key back: %s", body)
	}
	if !strings.Contains(body, "https://gw.example/v1") {
		t.Errorf("a plain variable was withheld: %s", body)
	}

	list := listProfiles(t, ts)
	made := list[len(list)-1]
	if made.Name != "gateway" || made.Builtin {
		t.Fatalf("got %+v", made)
	}
	for _, v := range made.Env {
		if v.Secret && v.Value != "" {
			t.Fatalf("a secret value is in the listing: %s=%q", v.Name, v.Value)
		}
		if !v.HasValue {
			t.Errorf("%s says nothing is stored, so the form would offer to set one", v.Name)
		}
	}
}

// The request a browser makes when somebody renames a profile is exactly the
// request it makes when they clear its key, apart from a flag: a secret always
// comes back with an empty value, because it was never sent out.
func TestRenamingOverTheAPIKeepsTheKey(t *testing.T) {
	ts, srv := newTestServer(t)
	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"one","command":[],"env":[{"name":"K","value":"sk-secret","secret":true}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, body)
	}
	var made store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}

	code, body = send(t, ts, http.MethodPatch, "/api/launch-profiles/"+made.ID,
		`{"name":"two","command":[],"env":[{"name":"K","value":"","secret":true,"hasValue":true}]}`)
	if code != http.StatusNoContent {
		t.Fatalf("PATCH: %d %s", code, body)
	}

	got, err := srv.DB.GetLaunchProfile(context.Background(), made.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "two" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Env[0].Value != "sk-secret" {
		t.Fatalf("renaming the profile wiped its key: %q", got.Env[0].Value)
	}
}

func TestTheAPIRefusesTheVariablesTheValidatorDoes(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"name":"p","command":[],"env":[{"name":"VIBEPANEL_URL","value":"http://evil"}]}`,
		`{"name":"p","command":[],"env":[{"name":"","value":"x"}]}`,
		`{"name":"p","command":[],"env":[{"name":"A B","value":"x"}]}`,
		`{"name":"p","command":[],"env":[{"name":"K","value":"a\nb"}]}`,
		`{"name":"","command":[],"env":[]}`,
	} {
		code, got := send(t, ts, http.MethodPost, "/api/launch-profiles", body)
		if code != http.StatusBadRequest {
			t.Errorf("POST %s: %d %s, want 400", body, code, got)
		}
	}
}

// Unknown fields are refused, which is what makes "PATCH replaces the whole
// profile" a promise rather than a hope: a caller that thinks it is sending a
// partial edit finds out rather than getting a 204 that quietly did less.
func TestAnUnknownFieldOnAProfileIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"p","command":[],"env":[],"apiHost":"https://gw"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST with an unknown field: %d %s", code, body)
	}
}

// The audit log is read on a settings page, printed into a journal and shipped
// wherever journals go. A profile's variables are the reason this feature holds
// credentials at all.
func TestTheAuditLogNeverCarriesAKey(t *testing.T) {
	ts, srv := newTestServer(t)
	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"gateway","command":[],"env":[{"name":"K","value":"sk-secret","secret":true}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST: %d %s", code, body)
	}
	var made store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}
	if code, body := send(t, ts, http.MethodPatch, "/api/launch-profiles/"+made.ID,
		`{"name":"gateway","command":[],"env":[{"name":"K","value":"sk-other","secret":true}]}`); code != http.StatusNoContent {
		t.Fatalf("PATCH: %d %s", code, body)
	}
	if code, body := send(t, ts, http.MethodDelete, "/api/launch-profiles/"+made.ID, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", code, body)
	}

	entries, err := srv.DB.RecentAudit(context.Background(), 100)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var events []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Event, "profile.") {
			continue
		}
		events = append(events, e.Event)
		if strings.Contains(e.Detail, "sk-secret") || strings.Contains(e.Detail, "sk-other") {
			t.Fatalf("a key is in the audit log: %q", e.Detail)
		}
	}
	sort.Strings(events)
	want := []string{"profile.created", "profile.deleted", "profile.updated"}
	if len(events) != 3 || events[0] != want[0] || events[1] != want[1] || events[2] != want[2] {
		t.Errorf("audited %v, want %v", events, want)
	}
}

// The whole point of the feature, end to end: a session created with a profile
// runs in the environment that profile describes, and the process can read it.
func TestASessionStartsInItsProfilesEnvironment(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"p"}`)

	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"gateway","command":["sh","-c","printf %s \"[$ANTHROPIC_BASE_URL]\"; sleep 60"],
		  "env":[{"name":"ANTHROPIC_BASE_URL","value":"https://gw.example/v1"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST profile: %d %s", code, body)
	}
	var prof store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &prof); err != nil {
		t.Fatal(err)
	}

	// No command in the request: the profile's argv is what runs, which is what
	// keeps the picker from having to know anything.
	s := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)
	if s.LaunchProfileID != prof.ID {
		t.Fatalf("the session did not record its profile: %q", s.LaunchProfileID)
	}
	if len(s.LaunchCommand) == 0 {
		t.Fatal("the resolved argv was not recorded; a restore would start a login shell " +
			"under the profile's name")
	}

	ctx := context.Background()
	got, err := srv.Tmux.SessionEnvValue(ctx, s.TmuxName, "ANTHROPIC_BASE_URL")
	if err != nil {
		t.Fatalf("SessionEnvValue: %v", err)
	}
	if got != "https://gw.example/v1" {
		t.Fatalf("the session's environment has %q", got)
	}
	// And the panel's own survived being ordered after it.
	if v, _ := srv.Tmux.SessionEnvValue(ctx, s.TmuxName, "VIBEPANEL_SESSION_ID"); v != s.ID {
		t.Fatalf("VIBEPANEL_SESSION_ID = %q, want %q; the session cannot report its state", v, s.ID)
	}
}

// A profile cannot redirect where a session reports its state, even if a row
// somehow holds the variable. Two layers: the validator refuses to save it, and
// the ordering means it would lose anyway. This drives the second by writing
// the row past the first.
func TestAProfileCannotRedirectAStateReport(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"p"}`)

	if _, err := srv.DB.CreateLaunchProfile(ctx, "handmade", store.LaunchProfile{Name: "x"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Straight into the column, the way a restored backup or somebody with
	// sqlite3 would.
	if _, err := srv.DB.SQL().ExecContext(ctx,
		`UPDATE launch_profiles SET env = ? WHERE id = 'handmade'`,
		`[{"name":"VIBEPANEL_URL","value":"http://evil.example"}]`); err != nil {
		t.Fatalf("hand-edit: %v", err)
	}

	s := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"handmade"}`)
	got, err := srv.Tmux.SessionEnvValue(ctx, s.TmuxName, "VIBEPANEL_URL")
	if err != nil {
		t.Fatalf("SessionEnvValue: %v", err)
	}
	if strings.Contains(got, "evil") {
		t.Fatalf("VIBEPANEL_URL = %q; the session posts its state, with the panel's "+
			"hook token, to an address a row chose", got)
	}
}

func TestASessionAsksForAProfileThatIsNotThere(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"p"}`)
	code, body := send(t, ts, http.MethodPost, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"gone"}`)
	// Not a session against the default endpoint, which is a substitution
	// nobody notices until the bill.
	if code != http.StatusNotFound {
		t.Fatalf("POST session with an unknown profile: %d %s, want 404", code, body)
	}
}

// A restore reads the profile again rather than a copy on the session row, so a
// session that was pointed at a gateway comes back pointed at the same gateway.
func TestARestoredSessionComesBackInItsProfilesEnvironment(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"p"}`)

	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"gateway","command":["sh","-c","sleep 60"],
		  "env":[{"name":"ANTHROPIC_BASE_URL","value":"https://gw.example/v1"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST profile: %d %s", code, body)
	}
	var prof store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &prof); err != nil {
		t.Fatal(err)
	}
	s := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)

	// What a reboot leaves: the row, and no tmux session.
	if err := srv.Tmux.Kill(ctx, s.TmuxName); err != nil {
		t.Fatalf("kill: %v", err)
	}
	rec, err := srv.DB.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := srv.restoreSession(ctx, rec); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := srv.Tmux.SessionEnvValue(ctx, s.TmuxName, "ANTHROPIC_BASE_URL")
	if err != nil {
		t.Fatalf("SessionEnvValue: %v", err)
	}
	if got != "https://gw.example/v1" {
		t.Fatalf("the restored session's base URL is %q; it came back against the "+
			"default endpoint", got)
	}
}

// A profile deleted after a session was created must not make that session
// unrestorable. It comes back without those variables, and the id it keeps is
// what lets a client say the profile is gone.
func TestASessionRestoresAfterItsProfileIsDeleted(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"p"}`)

	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"gateway","command":["sh","-c","sleep 60"],
		  "env":[{"name":"ANTHROPIC_BASE_URL","value":"https://gw.example/v1"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST profile: %d %s", code, body)
	}
	var prof store.LaunchProfile
	if err := json.Unmarshal([]byte(body), &prof); err != nil {
		t.Fatal(err)
	}
	s := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)

	if code, body := send(t, ts, http.MethodDelete, "/api/launch-profiles/"+prof.ID, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", code, body)
	}
	if err := srv.Tmux.Kill(ctx, s.TmuxName); err != nil {
		t.Fatalf("kill: %v", err)
	}
	rec, err := srv.DB.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.LaunchProfileID != prof.ID {
		t.Fatalf("deleting the profile changed the session row to %q; the UI can no "+
			"longer tell 'the profile is gone' from 'there never was one'",
			rec.LaunchProfileID)
	}
	if err := srv.restoreSession(ctx, rec); err != nil {
		t.Fatalf("a deleted profile made the session unrestorable: %v", err)
	}
}

func TestThereIsACapOnProfiles(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	for i := 0; i < store.MaxLaunchProfiles; i++ {
		if _, err := srv.DB.CreateLaunchProfile(ctx, "id"+strings.Repeat("x", i),
			store.LaunchProfile{Name: "p"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles", `{"name":"one more","command":[],"env":[]}`)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST past the cap: %d %s", code, body)
	}
}

// The built-in ids are the server's, so the frontend cannot type-check them and
// the untranslated-string test cannot see them -- it reads .tsx files and these
// are ids in a .go one. A built-in with no dictionary entry renders as its own
// English name in a Chinese picker.
func TestEveryBuiltinProfileHasBothLanguages(t *testing.T) {
	const path = "../../web/src/i18n.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so nothing was compared: %v", path, err)
	}
	dict := string(src)
	builtins := store.BuiltinLaunchProfiles()
	if len(builtins) < 2 {
		t.Fatalf("only %d built-ins collected; the catalogue reader has stopped reading",
			len(builtins))
	}
	for _, p := range builtins {
		// The whole id, which is what profileLabel looks up: keying on the id
		// rather than on a stripped suffix is what lets that function be one
		// expression with no branch that no test could tell from its absence.
		key := "profile.name." + p.ID
		// The dictionary keeps both languages on one line per key, so finding
		// the key finds both.
		if !strings.Contains(dict, "'"+key+"'") {
			t.Errorf("%s has no entry for %q", path, key)
		}
	}
}

// Every list a profile carries is a list on the wire, including an empty one.
//
// The Shell built-in has neither a command nor an environment, and
// `append([]string(nil), nil...)` is still nil, which marshals as `null`. The
// settings page then died on `p.env.map(...)` before drawing anything -- the
// whole dialog, not the profiles section, because a throw in a child unmounts
// the tree above it. Two render-check assertions went red and neither of them
// mentioned profiles.
//
// Asserted on the raw bytes. A JSON decoder cannot tell `[]` from `null`:
// both decode to a nil slice in Go and to a value that fails `.map` in
// TypeScript only at runtime, so a test that decodes proves nothing here.
func TestAProfileWithNothingInItStillSendsArrays(t *testing.T) {
	ts, _ := newTestServer(t)

	res, err := ts.Client().Get(ts.URL + "/api/launch-profiles")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	for _, bad := range []string{`"command":null`, `"env":null`} {
		if bytes.Contains(body, []byte(bad)) {
			t.Errorf("the profile list contains %s. docs/api.md's first "+
				"convention is that an endpoint with nothing to return sends "+
				"[], never null -- and the browser maps over both of these.",
				bad)
		}
	}
	if !bytes.Contains(body, []byte(`"command":[]`)) {
		t.Error("no profile came back with an empty command, so the check " +
			"above ran against nothing")
	}
}

// ─── arranging the catalogue ───────────────────────────────────────────────

func profileIDs(t *testing.T, ts *httptest.Server) []string {
	t.Helper()
	out := []string{}
	for _, p := range listProfiles(t, ts) {
		out = append(out, p.ID)
	}
	return out
}

// TestABuiltinCanBeTakenOutOfTheList.
//
// 「自带的几个 agent 没法删啊现在」. There is no row to delete -- the catalogue
// is a slice in internal/store -- so it is hidden, and the thing that makes a
// one-click delete safe is that /restore puts it back.
func TestABuiltinCanBeTakenOutOfTheList(t *testing.T) {
	ts, _ := newTestServer(t)
	const codex = store.BuiltinPrefix + "codex"

	if !slices.Contains(profileIDs(t, ts), codex) {
		t.Fatal("codex is not in the catalogue to begin with")
	}
	if code, body := send(t, ts, http.MethodDelete, "/api/launch-profiles/"+codex, ""); code >= 300 {
		t.Fatalf("hiding codex: %d %s", code, body)
	}
	if got := profileIDs(t, ts); slices.Contains(got, codex) {
		t.Errorf("codex is still offered after being removed: %v", got)
	}
	if code, body := send(t, ts, http.MethodPost, "/api/launch-profiles/restore", `{}`); code >= 300 {
		t.Fatalf("restore: %d %s", code, body)
	}
	if got := profileIDs(t, ts); !slices.Contains(got, codex) {
		t.Errorf("restore did not put codex back: %v", got)
	}
}

// TestEditingABuiltinKeepsItsIDAndSaysItWasEdited.
//
// The id has to survive: a session records the profile it was started with,
// and turning an edit into a new profile with a new id would leave every
// existing session pointing at something that is not there. `overridden` is
// the other half -- the objection to allowing this at all was that the
// catalogue a release ships would stop being the catalogue people have with
// nothing on screen saying so, and this is the thing that says so.
func TestEditingABuiltinKeepsItsIDAndSaysItWasEdited(t *testing.T) {
	ts, _ := newTestServer(t)
	const claude = store.BuiltinPrefix + "claude"

	if code, body := send(t, ts, http.MethodPatch, "/api/launch-profiles/"+claude,
		`{"name":"Claude via proxy","command":["claude"],"env":[{"name":"ANTHROPIC_BASE_URL","value":"http://proxy.internal"}]}`); code >= 300 {
		t.Fatalf("editing the built-in: %d %s", code, body)
	}

	list := listProfiles(t, ts)
	var seen int
	for _, p := range list {
		if p.ID != claude {
			continue
		}
		seen++
		if p.Name != "Claude via proxy" {
			t.Errorf("name = %q, want the edit", p.Name)
		}
		if !p.Overridden {
			t.Error("an edited built-in does not report itself as overridden")
		}
	}
	// Exactly once. A row beside the catalogue entry rather than instead of it
	// would show the same agent twice, and the picker would be where you found
	// out.
	if seen != 1 {
		t.Errorf("the edited built-in appears %d times, want 1", seen)
	}

	// And putting it back is one action.
	if code, body := send(t, ts, http.MethodPost, "/api/launch-profiles/restore", `{}`); code >= 300 {
		t.Fatalf("restore: %d %s", code, body)
	}
	list = listProfiles(t, ts)
	for _, p := range list {
		if p.ID == claude && p.Name == "Claude via proxy" {
			t.Error("restore left the edit in place")
		}
	}
}

// TestTheOrderIsTheOwnersAndSurvivesANewBuiltin.
//
// Both halves of `arrange`. Without the first the drag does nothing; without
// the second a profile the arrangement has never heard of -- one a release
// added, one created in another tab -- disappears from the picker instead of
// arriving at the end of it.
func TestTheOrderIsTheOwnersAndSurvivesANewBuiltin(t *testing.T) {
	ts, _ := newTestServer(t)
	all := profileIDs(t, ts)
	if len(all) < 3 {
		t.Fatalf("need three to reorder, have %v", all)
	}
	// Reversed, and deliberately naming only the first two: the rest have to
	// still be there.
	if code, body := send(t, ts, http.MethodPost, "/api/launch-profiles/reorder",
		`{"ids":`+jsonList(all[2], all[0])+`}`); code >= 300 {
		t.Fatalf("reorder: %d %s", code, body)
	}

	got := profileIDs(t, ts)
	if len(got) != len(all) {
		t.Fatalf("reordering changed the list from %v to %v", all, got)
	}
	if got[0] != all[2] || got[1] != all[0] {
		t.Errorf("order = %v, want %s and %s first", got, all[2], all[0])
	}
	for _, id := range all {
		if !slices.Contains(got, id) {
			t.Errorf("%s fell out of the list: %v", id, got)
		}
	}
}

// TestAnUnknownBuiltinCannotBeHidden.
//
// `builtin:` is a prefix and an id off a URL is whatever somebody typed, so
// the question the handlers have to ask is "is this in the catalogue" and not
// "does it look like it might be". IsBuiltinLaunchProfile compares against the
// catalogue, which is what makes this pass -- a second helper that only
// checked the prefix was written here first, justified in a comment by a claim
// about that function that was simply false, and would have been the two
// implementations of one guard this package keeps warning about.
func TestAnUnknownBuiltinCannotBeHidden(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, path := range []string{
		"/api/launch-profiles/" + store.BuiltinPrefix + "nope",
		"/api/launch-profiles/" + store.BuiltinPrefix + "../../etc",
	} {
		if code, _ := send(t, ts, http.MethodDelete, path, ""); code != http.StatusNotFound {
			t.Errorf("DELETE %s = %d, want 404", path, code)
		}
	}
}

func jsonList(ids ...string) string {
	b, _ := json.Marshal(ids)
	return string(b)
}
