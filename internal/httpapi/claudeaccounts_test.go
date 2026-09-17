package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

func createClaudeAccount(t *testing.T, ts *httptest.Server, body string) claudeAccountView {
	t.Helper()
	code, resp := send(t, ts, http.MethodPost, "/api/settings/claude-accounts", body)
	if code != http.StatusCreated {
		t.Fatalf("POST claude account: %d %s", code, resp)
	}
	var v claudeAccountView
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func createProfileWith(t *testing.T, ts *httptest.Server, body string) store.LaunchProfile {
	t.Helper()
	code, resp := send(t, ts, http.MethodPost, "/api/launch-profiles", body)
	if code != http.StatusCreated {
		t.Fatalf("POST profile: %d %s", code, resp)
	}
	var p store.LaunchProfile
	if err := json.Unmarshal([]byte(resp), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The feature end to end: an account made in settings, a profile that uses it,
// a session started from the profile running with CLAUDE_CONFIG_DIR at the
// account and the account's directory linked into the ~/.claude the hooks are
// installed in.
func TestASessionStartsUnderItsProfilesClaudeAccount(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	acct := createClaudeAccount(t, ts, `{"name":"work"}`)
	if want := filepath.Join(srv.Cfg.DataDir, "claude-accounts", acct.ID); acct.Dir != want {
		t.Fatalf("dir %q, want %q", acct.Dir, want)
	}
	if dest, err := os.Readlink(filepath.Join(acct.Dir, "settings.json")); err != nil ||
		dest != filepath.Join(srv.claudeHome, ".claude", "settings.json") {
		t.Fatalf("settings.json is not linked to the shared one: %q %v", dest, err)
	}

	prof := createProfileWith(t, ts, `{"name":"claude work","command":["sh","-c","sleep 60"],
		"env":[],"claudeAccountId":"`+acct.ID+`"}`)
	if prof.ClaudeAccountID != acct.ID {
		t.Fatalf("the profile did not keep its account: %q", prof.ClaudeAccountID)
	}

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"p"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)
	if sess.ClaudeAccountID != acct.ID {
		t.Fatalf("the session did not record its account: %q", sess.ClaudeAccountID)
	}
	got, err := srv.Tmux.SessionEnvValue(ctx, sess.TmuxName, "CLAUDE_CONFIG_DIR")
	if err != nil || got != acct.Dir {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q (%v), want %q", got, err, acct.Dir)
	}
	if v, _ := srv.Tmux.SessionEnvValue(ctx, sess.TmuxName, "VIBEPANEL_SESSION_ID"); v != sess.ID {
		t.Fatalf("VIBEPANEL_SESSION_ID = %q; the account displaced the panel's own variables", v)
	}

	// And the list says what uses it.
	code, body := send(t, ts, http.MethodGet, "/api/settings/claude-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, body)
	}
	var list []claudeAccountView
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Running != 1 || len(list[0].Profiles) != 1 {
		t.Fatalf("list = %+v", list)
	}
}

// A key or token beside an account is used instead of the login, silently.
func TestAProfileCannotCarryAnAccountAndACredentialThatReplacesIt(t *testing.T) {
	ts, _ := newTestServer(t)
	acct := createClaudeAccount(t, ts, `{"name":"work"}`)

	for _, env := range []string{
		`[{"name":"ANTHROPIC_API_KEY","value":"sk-x","secret":true}]`,
		`[{"name":"CLAUDE_CODE_OAUTH_TOKEN","value":"t","secret":true}]`,
		`[{"name":"CLAUDE_CONFIG_DIR","value":"/elsewhere"}]`,
		`[{"name":"CLAUDE_SECURESTORAGE_CONFIG_DIR","value":""}]`,
	} {
		code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
			`{"name":"x","command":["claude"],"env":`+env+`,"claudeAccountId":"`+acct.ID+`"}`)
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", env, code, body)
		}
	}

	// The built-in Claude Code profile names two of them with no value, and
	// an empty value is never passed: that is not a conflict.
	createProfileWith(t, ts, `{"name":"ok","command":["claude"],
		"env":[{"name":"ANTHROPIC_AUTH_TOKEN","value":"","secret":true},{"name":"ANTHROPIC_BASE_URL","value":""}],
		"claudeAccountId":"`+acct.ID+`"}`)

	code, body := send(t, ts, http.MethodPost, "/api/launch-profiles",
		`{"name":"x","command":["claude"],"env":[],"claudeAccountId":"nosuchaccount"}`)
	if code != http.StatusBadRequest {
		t.Errorf("an account that does not exist: %d %s, want 400", code, body)
	}
}

// A secret is not sent back to the browser, so an edit that keeps it arrives
// with an empty value. The check has to see the stored one.
func TestAStoredKeyStillConflictsWhenTheEditLeavesItBlank(t *testing.T) {
	ts, _ := newTestServer(t)
	acct := createClaudeAccount(t, ts, `{"name":"work"}`)
	prof := createProfileWith(t, ts, `{"name":"x","command":["claude"],
		"env":[{"name":"ANTHROPIC_API_KEY","value":"sk-stored","secret":true}]}`)
	code, body := send(t, ts, http.MethodPatch, "/api/launch-profiles/"+prof.ID,
		`{"name":"x","command":["claude"],"env":[{"name":"ANTHROPIC_API_KEY","value":"","secret":true}],
		  "claudeAccountId":"`+acct.ID+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("PATCH: %d %s, want 400", code, body)
	}
}

func TestAnAccountInUseCannotBeDeleted(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	var mu sync.Mutex
	var calls []string
	srv.claudeRun = func(_ context.Context, env []string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, strings.Join(env, " ")+" | "+strings.Join(args, " "))
		return nil, nil
	}

	acct := createClaudeAccount(t, ts, `{"name":"work"}`)
	prof := createProfileWith(t, ts, `{"name":"claude work","command":["sh","-c","sleep 60"],
		"env":[],"claudeAccountId":"`+acct.ID+`"}`)
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"p"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)

	code, body := send(t, ts, http.MethodDelete, "/api/settings/claude-accounts/"+acct.ID, "")
	if code != http.StatusConflict || !strings.Contains(body, "claude work") {
		t.Fatalf("delete with a profile using it: %d %s", code, body)
	}

	// The profile no longer uses it; the session started from it still runs.
	code, body = send(t, ts, http.MethodPatch, "/api/launch-profiles/"+prof.ID,
		`{"name":"claude work","command":["sh","-c","sleep 60"],"env":[]}`)
	if code != http.StatusNoContent {
		t.Fatalf("PATCH profile: %d %s", code, body)
	}
	code, body = send(t, ts, http.MethodDelete, "/api/settings/claude-accounts/"+acct.ID, "")
	if code != http.StatusConflict || !strings.Contains(body, "running") {
		t.Fatalf("delete with a live session: %d %s", code, body)
	}

	if err := srv.Tmux.Kill(ctx, sess.TmuxName); err != nil {
		t.Fatal(err)
	}
	code, body = send(t, ts, http.MethodDelete, "/api/settings/claude-accounts/"+acct.ID, "")
	if code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, err := os.Lstat(acct.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the account directory is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.claudeHome, ".claude", "projects")); err != nil {
		t.Errorf("deleting the account took the shared projects with it: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || !strings.Contains(calls[0], "CLAUDE_CONFIG_DIR="+acct.Dir) ||
		!strings.HasSuffix(calls[0], "auth logout") {
		t.Errorf("logout was run as %q; on macOS the login outlives the directory otherwise", calls)
	}
}

// A session comes back under the account it was started with, whatever its
// profile says now -- and does not come back at all if that account is gone.
func TestARestoreKeepsTheAccountTheSessionStartedWith(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	work := createClaudeAccount(t, ts, `{"name":"work"}`)
	personal := createClaudeAccount(t, ts, `{"name":"personal"}`)
	prof := createProfileWith(t, ts, `{"name":"claude","command":["sh","-c","sleep 60"],
		"env":[],"claudeAccountId":"`+work.ID+`"}`)
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"p"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","launchProfileId":"`+prof.ID+`"}`)

	code, body := send(t, ts, http.MethodPatch, "/api/launch-profiles/"+prof.ID,
		`{"name":"claude","command":["sh","-c","sleep 60"],"env":[],"claudeAccountId":"`+personal.ID+`"}`)
	if code != http.StatusNoContent {
		t.Fatalf("PATCH profile: %d %s", code, body)
	}

	simulateReboot(t, srv)
	if err := srv.restoreSession(ctx, getSession(t, srv, sess.ID)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := srv.Tmux.SessionEnvValue(ctx, sess.TmuxName, "CLAUDE_CONFIG_DIR")
	if err != nil || got != work.Dir {
		t.Fatalf("restored with CLAUDE_CONFIG_DIR = %q (%v), want the work account %q", got, err, work.Dir)
	}

	// Now the account goes. The tmux session has to go first, or the delete is
	// refused.
	simulateReboot(t, srv)
	if err := srv.DB.DeleteClaudeAccount(ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	err = srv.restoreSession(ctx, getSession(t, srv, sess.ID))
	if !errors.Is(err, errClaudeAccountGone) {
		t.Fatalf("restoring under a removed account: %v, want a refusal", err)
	}
	if has, _ := srv.Tmux.Has(ctx, sess.TmuxName); has {
		t.Error("the session was started anyway")
	}
	if _, err := os.Stat(filepath.Join(srv.Cfg.RestoreDir(), sess.ID+".scrollback")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refusal left the archive copy behind: %v", err)
	}
}

func TestAccountStatusReportsTheCLIAndTheLinks(t *testing.T) {
	ts, srv := newTestServer(t)
	acct := createClaudeAccount(t, ts, `{"name":"work","isolated":true}`)
	var gotEnv []string
	srv.claudeRun = func(_ context.Context, env []string, args ...string) ([]byte, error) {
		gotEnv = env
		return []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"w@example.com","configDirectory":"` +
			acct.Dir + `"}`), nil
	}
	code, body := send(t, ts, http.MethodGet, "/api/settings/claude-accounts/"+acct.ID+"/status", "")
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var st claudeAccountStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status == nil || !st.Status.LoggedIn || st.Status.Email != "w@example.com" {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(strings.Join(gotEnv, " "), "CLAUDE_CONFIG_DIR="+acct.Dir) {
		t.Errorf("status was asked with env %v", gotEnv)
	}
	private := 0
	for _, l := range st.Links {
		if l.State == "private" {
			private++
		}
	}
	if private == 0 {
		t.Errorf("an isolated account reports no private entries: %+v", st.Links)
	}

	// The email reaches the settings page and nothing that records.
	entries, err := srv.DB.RecentAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Detail, "@") || strings.Contains(e.Detail, acct.Dir) {
			t.Errorf("audit entry %+v discloses the account", e)
		}
	}
}
