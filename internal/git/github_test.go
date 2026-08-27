package git

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGitHub(t *testing.T, body string, status int) (*httptest.Server, *string) {
	t.Helper()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = r.Header.Get("Authorization") + "\n" + string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestOpenPRs(t *testing.T) {
	const answer = `{"data":{"repository":{"pullRequests":{"totalCount":3,"nodes":[
		{"number":7,"title":"add auth","url":"https://github.com/o/r/pull/7","isDraft":false,
		 "updatedAt":"2026-08-20T10:00:00Z","headRefName":"feat/auth","baseRefName":"main",
		 "reviewDecision":"CHANGES_REQUESTED","author":{"login":"someone"},
		 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}},
		{"number":8,"title":"docs","url":"https://github.com/o/r/pull/8","isDraft":true,
		 "updatedAt":"2026-08-19T10:00:00Z","headRefName":"docs","baseRefName":"main",
		 "reviewDecision":null,"author":null,
		 "commits":{"nodes":[]}}
	]}}}}`
	srv, seen := fakeGitHub(t, answer, http.StatusOK)
	c := Client{Token: "t0ken", Endpoint: srv.URL, HTTP: srv.Client()}

	res, err := c.OpenPRs(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("OpenPRs: %v", err)
	}
	// The count is not len(PRs) once the page bites, and the panel says so.
	if res.Total != 3 || len(res.PRs) != 2 {
		t.Fatalf("total %d prs %d", res.Total, len(res.PRs))
	}
	pr := res.PRs[0]
	if pr.Branch != "feat/auth" {
		t.Errorf("branch = %q; this is the field that joins a PR to a session's branch", pr.Branch)
	}
	// Lowercased on this side so the panel has one vocabulary rather than
	// GitHub's SCREAMING_CASE leaking into a dictionary lookup.
	if pr.Checks != "failure" || pr.Review != "changes_requested" {
		t.Errorf("checks %q review %q", pr.Checks, pr.Review)
	}
	if pr.Author != "someone" || pr.UpdatedAt == 0 {
		t.Errorf("pr = %+v", pr)
	}
	// A deleted account and a repository with no CI. Both are ordinary and
	// neither may be a nil dereference or a fabricated state.
	if res.PRs[1].Author != "" || res.PRs[1].Checks != "" || res.PRs[1].Review != "" {
		t.Errorf("pr = %+v", res.PRs[1])
	}
	if !res.PRs[1].Draft {
		t.Error("a draft was not reported as one")
	}

	t.Run("sends the token and the names as variables", func(t *testing.T) {
		if !strings.Contains(*seen, "Bearer t0ken") {
			t.Errorf("request did not carry the token: %q", *seen)
		}
		var sent struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_, body, _ := strings.Cut(*seen, "\n")
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatalf("the request body is not the JSON it claims: %v", err)
		}
		if sent.Variables["owner"] != "o" || sent.Variables["name"] != "r" {
			t.Errorf("variables = %v", sent.Variables)
		}
		// The names come out of a file in a working tree. Interpolated into
		// the query text they would be somebody else's string inside ours.
		if strings.Contains(sent.Query, `"o"`) || strings.Contains(sent.Query, `"r"`) {
			t.Errorf("the repository was interpolated into the query: %q", sent.Query)
		}
	})
}

// GraphQL answers 200 with an errors array. A handler that reads only the
// status code reports success and an empty list, which on screen is "no open
// pull requests" about a repository the token cannot see.
func TestAGraphQLErrorIsAnError(t *testing.T) {
	srv, _ := fakeGitHub(t, `{"data":{"repository":null},"errors":[{"message":"Could not resolve to a Repository"}]}`, http.StatusOK)
	c := Client{Token: "t", Endpoint: srv.URL, HTTP: srv.Client()}
	_, err := c.OpenPRs(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("a 200 carrying an errors array was reported as success")
	}
	if !strings.Contains(err.Error(), "Could not resolve") {
		t.Errorf("err = %v, and the message is the only actionable part", err)
	}
}

func TestARefusedTokenSaysSo(t *testing.T) {
	srv, _ := fakeGitHub(t, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	c := Client{Token: "wrong", Endpoint: srv.URL, HTTP: srv.Client()}
	_, err := c.OpenPRs(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v; "+
			"a refused token is the one failure here with an action attached", err)
	}
}

func TestNoTokenIsNoRequest(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()
	c := Client{Endpoint: srv.URL, HTTP: srv.Client()}
	if _, err := c.OpenPRs(context.Background(), "o", "r"); err == nil {
		t.Error("a request with no token was attempted")
	}
	if reached {
		t.Error("the panel opened a connection to GitHub with no credential to send")
	}
}

func TestAnUnusableRepositoryNameNeverLeavesTheMachine(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()
	c := Client{Token: "t", Endpoint: srv.URL, HTTP: srv.Client()}
	for _, name := range []string{"", "a/b", "a b", strings.Repeat("x", 200)} {
		if _, err := c.OpenPRs(context.Background(), "o", name); err == nil {
			t.Errorf("OpenPRs(%q) was allowed", name)
		}
	}
	if reached {
		t.Error("a name out of a working tree was sent to another machine unchecked")
	}
}

func TestTheEndpointIsAConstant(t *testing.T) {
	// Derived from a remote URL it would be a hostname out of a cloned
	// repository, resolved by the panel, with a token in the Authorization
	// header. It is not derived from anything.
	if Endpoint != "https://api.github.com/graphql" {
		t.Errorf("Endpoint = %q", Endpoint)
	}
}
