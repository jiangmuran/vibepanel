package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// repoAt makes a working tree with one commit in it and a GitHub origin.
func repoAt(t *testing.T, dir string) {
	repoAtRemote(t, dir, "git@github.com:owner/name.git")
}

// repoAtRemote is the same, with the origin named by the caller.
//
// Split out because "is this remote one we will link to" is a decision with
// several answers, and a test for the ones that are refused needs a tree whose
// origin is not github.com.
func repoAtRemote(t *testing.T, dir, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	sh := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	sh("init", "-b", "main")
	sh("config", "user.email", "t@example.com")
	sh("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh("add", "a.txt")
	sh("commit", "-m", "first")
	sh("remote", "add", "origin", origin)
}

func TestGitEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	repoAt(t, root)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"repo"}`)

	read := func() gitResponse {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/git")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("%s: %s", res.Status, b)
		}
		var out gitResponse
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("reads the tree with no network and no credential", func(t *testing.T) {
		got := read()
		if !got.Status.Repo || got.Status.Branch != "main" {
			t.Fatalf("%+v", got.Status)
		}
		if got.Status.Untracked != 1 {
			t.Errorf("untracked = %d", got.Status.Untracked)
		}
		if len(got.Commits) != 1 || got.Commits[0].Subject != "first" {
			t.Errorf("commits = %+v", got.Commits)
		}
		if got.Remote == nil || !got.GitHub {
			t.Errorf("remote = %+v github = %v", got.Remote, got.GitHub)
		}
	})

	t.Run("says a plain directory is not a repository rather than failing", func(t *testing.T) {
		// Half the projects anybody adds are not repositories, and an error
		// panel for that is a panel that looks broken most of the time.
		plain := postJSON[store.Project](t, ts, "/api/projects",
			`{"path":"`+t.TempDir()+`","name":"plain"}`)
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + plain.ID + "/git")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", res.StatusCode)
		}
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var out gitResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.Status.Repo {
			t.Error("a plain directory was reported as a repository")
		}
		// Read as text, not through the decoder, because the decoder cannot
		// tell `[]` from `null` and the frontend can: it takes `.length` of
		// whatever arrives, and the case that reaches it first is this one.
		// See browse.List, where the same nil took the console down on an
		// empty project.
		for _, field := range []string{`"changes":[]`, `"commits":[]`, `"sessions":[]`} {
			if !strings.Contains(string(raw), field) {
				t.Errorf("the response has no %s in it: %s", field, raw)
			}
		}
	})

	t.Run("needs the session", func(t *testing.T) {
		bare := &http.Client{}
		res, err := bare.Get(ts.URL + "/api/projects/" + project.ID + "/git")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", res.StatusCode)
		}
	})

	t.Run("reports whether a token exists without disclosing it", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		if read().TokenSet {
			t.Error("tokenSet with nothing in the environment")
		}
		t.Setenv("GITHUB_TOKEN", "sekrit")
		got := read()
		if !got.TokenSet {
			t.Error("tokenSet false with a token in the environment")
		}
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/git")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if strings.Contains(string(body), "sekrit") {
			t.Error("the token itself was sent to the browser")
		}
	})
}

// The GET half must never reach a network, whatever is in the environment.
//
// The guard behind this is the subcommand allowlist in internal/git; this is
// the end-to-end version of it, because the thing that would break it is a call
// site here rather than a change there.
func TestTheLocalHalfMakesNoOutboundRequest(t *testing.T) {
	var reached bool
	spy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer spy.Close()

	ts, srv := newTestServer(t)
	srv.GitHub = git.Client{Endpoint: spy.URL, HTTP: spy.Client()}
	root := t.TempDir()
	repoAt(t, root)
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"repo"}`)
	t.Setenv("GITHUB_TOKEN", "a-token-that-exists")

	// Read it several times: a poller behind the tab would show up here.
	for i := 0; i < 3; i++ {
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/git")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	if reached {
		t.Fatal("reading the working tree reached the network. The product promise is that " +
			"nothing leaves the machine unless somebody presses the button.")
	}
}

func TestGitHubEndpoint(t *testing.T) {
	root := t.TempDir()
	repoAt(t, root)

	const answer = `{"data":{"repository":{"pullRequests":{"totalCount":1,"nodes":[
		{"number":7,"title":"add auth","url":"https://github.com/o/r/pull/7","isDraft":false,
		 "updatedAt":"2026-08-20T10:00:00Z","headRefName":"feat/auth","baseRefName":"main",
		 "reviewDecision":null,"author":{"login":"someone"},
		 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}}
	]}}}}`
	var calls int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	defer fake.Close()

	ts, srv := newTestServer(t)
	srv.GitHub = git.Client{Endpoint: fake.URL, HTTP: fake.Client()}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"repo"}`)

	t.Run("refuses without a token, and without opening a connection", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		before := calls
		res, err := ts.Client().Post(ts.URL+"/api/projects/"+project.ID+"/git/github", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status %d, want 400", res.StatusCode)
		}
		if calls != before {
			t.Error("a request was made with no credential to send")
		}
	})

	t.Run("asks once per press", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "t0ken")
		before := calls
		got := postJSON[githubResponse](t, ts, "/api/projects/"+project.ID+"/git/github", "")
		if calls != before+1 {
			t.Errorf("calls = %d, want exactly one more than %d", calls, before)
		}
		if got.Total != 1 || len(got.PRs) != 1 || got.PRs[0].Branch != "feat/auth" {
			t.Fatalf("%+v", got)
		}
		if got.PRs[0].Checks != "success" {
			t.Errorf("checks = %q", got.PRs[0].Checks)
		}
		// There is no poller behind this list, so its age is part of it.
		if got.CheckedAt == 0 {
			t.Error("no checkedAt, so a list from an hour ago looks live")
		}
	})

	t.Run("is not reachable with GET", func(t *testing.T) {
		// A GET is something a browser re-issues on its own: a reload, a back
		// button, a prefetch. The one request in this panel that leaves the
		// machine must not have that property.
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/git/github")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d; GET must not reach the network", res.StatusCode)
		}
	})

	t.Run("refuses a project whose remote is not github.com", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "t0ken")
		other := t.TempDir()
		repoAt(t, other)
		cmd := exec.Command("git", "remote", "set-url", "origin", "git@gitlab.com:o/r.git")
		cmd.Dir = other
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		p2 := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+other+`","name":"gl"}`)
		before := calls
		res, err := ts.Client().Post(ts.URL+"/api/projects/"+p2.ID+"/git/github", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status %d, want 400", res.StatusCode)
		}
		if calls != before {
			t.Error("a non-GitHub remote reached the GitHub API anyway")
		}
	})

	t.Run("needs the session", func(t *testing.T) {
		bare := &http.Client{}
		res, err := bare.Post(ts.URL+"/api/projects/"+project.ID+"/git/github", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", res.StatusCode)
		}
	})
}
