package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ev() Event {
	return Event{
		State:   "waiting",
		Session: `fix a&b "now"`,
		Project: "billing",
		URL:     "https://panel.example/#s_1",
		At:      time.Unix(1700000000, 0).UTC(),
	}
}

// A title going into a URL and the same title going into a JSON body need
// different escapes, and using one for the other is the whole bug.
//
// Agent titles contain ampersands and quotes constantly. Unescaped in a query
// string, everything after the & becomes a different parameter and the
// notification arrives truncated. Unescaped in a JSON body, the quote ends the
// string and the destination rejects the request.
func TestTheTwoEscapesAreNotInterchangeable(t *testing.T) {
	inURL := Render("https://x/?title={session}", ev(), EscapeQuery)
	if strings.Contains(inURL, "&b") {
		t.Errorf("the ampersand reached the query string unescaped: %s", inURL)
	}
	if !strings.Contains(inURL, "%26") {
		t.Errorf("the ampersand is not percent-encoded: %s", inURL)
	}

	body := Render(`{"title":"{session}"}`, ev(), EscapeJSON)
	var out map[string]string
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("the rendered body is not JSON: %v\n%s", err, body)
	}
	if out["title"] != ev().Session {
		t.Errorf("the title came back as %q, want %q", out["title"], ev().Session)
	}
}

func TestControlCharactersSurviveTheJSONEscape(t *testing.T) {
	e := ev()
	e.Session = "a\nb\tc\x01d"
	body := Render(`{"t":"{session}"}`, e, EscapeJSON)
	var out map[string]string
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("a control character broke the body: %v\n%s", err, body)
	}
	if out["t"] != e.Session {
		t.Errorf("round trip changed %q into %q", e.Session, out["t"])
	}
}

// The default is waiting only. A webhook that fires on every state is a
// webhook somebody mutes within a day, and a muted webhook is the same as none.
func TestWhichStatesFire(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w     Webhook
		state string
		want  bool
	}{
		{"default is waiting", Webhook{Enabled: true}, "waiting", true},
		{"default is not working", Webhook{Enabled: true}, "working", false},
		{"default is not done", Webhook{Enabled: true}, "done", false},
		{"explicit list", Webhook{Enabled: true, States: []string{"done"}}, "done", true},
		{"explicit list excludes", Webhook{Enabled: true, States: []string{"done"}}, "waiting", false},
		{"disabled never fires", Webhook{Enabled: false, States: []string{"waiting"}}, "waiting", false},
	} {
		if got := tc.w.Fires(tc.state); got != tc.want {
			t.Errorf("%s: Fires(%q) = %v, want %v", tc.name, tc.state, got, tc.want)
		}
	}
}

func TestSendPostsTheRenderedRequest(t *testing.T) {
	var gotPath, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok\n"))
	}))
	defer srv.Close()

	said, err := Send(context.Background(), srv.Client(), Webhook{
		Name: "test", Method: "POST", URL: srv.URL + "/push?s={state}",
		Headers: map[string]string{"Authorization": "Bearer t"},
		Body:    `{"title":"{session}","project":"{project}","url":"{url}"}`,
		Enabled: true,
	}, ev())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if said != "ok" {
		t.Errorf("said = %q", said)
	}
	if !strings.Contains(gotPath, "s=waiting") {
		t.Errorf("the URL template was not rendered: %s", gotPath)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("headers were not sent: %q", gotAuth)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(gotBody), &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, gotBody)
	}
	if out["project"] != "billing" {
		t.Errorf("body = %s", gotBody)
	}
	// The title, exactly. Percent-encoding it instead is still valid JSON --
	// which is why asserting "the body parses" passes with the wrong escape --
	// and the notification arrives reading `fix+a%26b+%22now%22`.
	if out["title"] != ev().Session {
		t.Errorf("the title arrived as %q, want %q; the wrong escape was used "+
			"for a JSON body", out["title"], ev().Session)
	}
}

// A destination that says no has to say so, with what it said. "The
// notification failed" and nothing else is a message nobody can act on.
func TestARefusalCarriesWhatTheDestinationSaid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("bad token"))
	}))
	defer srv.Close()
	said, err := Send(context.Background(), srv.Client(),
		Webhook{Name: "n", URL: srv.URL, Enabled: true}, ev())
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if said != "bad token" {
		t.Errorf("the destination's own words were dropped: %q", said)
	}
}

func TestANonURLIsRefusedBeforeAnyRequest(t *testing.T) {
	if _, err := Send(context.Background(), nil,
		Webhook{Name: "n", URL: "not a url", Enabled: true}, ev()); err == nil {
		t.Error("a string that is not a URL was accepted")
	}
}
