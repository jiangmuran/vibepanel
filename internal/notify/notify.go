// Package notify sends a session's state change somewhere the person is.
//
// The panel already knows when a session starts waiting for a human. Until now
// the only way to hear about it was a browser notification, which needs the
// panel open in a tab or installed as an app -- so the case it does not cover
// is the one that matters: the laptop is shut and the person is somewhere else.
//
// One mechanism, not a list of providers. Bark, ntfy, Gotify, ServerChan,
// PushPlus, Slack, Discord and a shell script behind a reverse proxy are all
// "make an HTTP request with these headers and this body", and a package with a
// case per service is a package that is missing whichever one somebody uses.
// Presets fill the same three fields in; they are values, not code paths.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is what happened, in the fields a template may use.
type Event struct {
	// State is the session's new state: waiting, working or done. Bare strings
	// here rather than the enum because this package is on the far side of the
	// wire from it -- see red line 3 in AGENTS.md; the caller passes what the
	// panel decided.
	State   string
	Session string
	Project string
	// URL is where to open the panel at that session, so a notification on a
	// phone is one tap from the thing it is about.
	URL string
	At  time.Time
}

// Webhook is one destination.
type Webhook struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Method, URL, Headers and Body are the request, with {placeholders}
	// substituted. A GET with everything in the query string is how Bark and
	// ServerChan work; a POST with a JSON body is how ntfy and Slack do.
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`

	// States is which transitions fire it. Empty means waiting only, which is
	// the one worth waking somebody for; a webhook that fires on every state is
	// a webhook somebody mutes within a day.
	States  []string `json:"states,omitempty"`
	Enabled bool     `json:"enabled"`
}

// Fires reports whether this webhook wants a state.
func (w Webhook) Fires(state string) bool {
	if !w.Enabled {
		return false
	}
	if len(w.States) == 0 {
		return state == "waiting"
	}
	for _, s := range w.States {
		if s == state {
			return true
		}
	}
	return false
}

// maxBody bounds what is read back from a webhook.
//
// The response is only ever shown as "it worked" or "it said this", and a
// destination that answers with a megabyte of HTML should not become a
// megabyte held in the panel.
const maxBody = 8 << 10

// timeout bounds one attempt. There are no retries: a notification that
// arrives four minutes late is worse than one that did not arrive, because the
// person has already looked.
const timeout = 8 * time.Second

// Render substitutes the event's fields into a template.
//
// Two escapes, and picking the wrong one is the bug this exists to avoid. In a
// URL a session called "fix a&b" must arrive as %26, or everything after the
// ampersand becomes a different query parameter. In a JSON body it must arrive
// as an escaped JSON string, or a title with a quote in it produces a body the
// destination rejects -- and agent titles contain quotes constantly.
func Render(tmpl string, ev Event, esc func(string) string) string {
	rep := strings.NewReplacer(
		"{state}", esc(ev.State),
		"{session}", esc(ev.Session),
		"{project}", esc(ev.Project),
		"{url}", esc(ev.URL),
		"{time}", esc(ev.At.Format(time.RFC3339)),
	)
	return rep.Replace(tmpl)
}

// EscapeQuery is the escape for anything going into a URL.
func EscapeQuery(s string) string { return url.QueryEscape(s) }

// EscapeJSON is the escape for anything going into a JSON string literal.
//
// The quotes the encoder adds are stripped: the template already has them, as
// `"title": "{session}"`, which is how somebody writes a body by hand.
func EscapeJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Send makes the request. The returned string is what the destination said,
// bounded, for showing after a test.
func Send(ctx context.Context, c *http.Client, w Webhook, ev Event) (string, error) {
	if c == nil {
		c = &http.Client{Timeout: timeout}
	}
	target := Render(w.URL, ev, EscapeQuery)
	if _, err := url.ParseRequestURI(target); err != nil {
		return "", fmt.Errorf("notify: %s is not a URL: %w", w.Name, err)
	}

	method := strings.ToUpper(strings.TrimSpace(w.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if w.Body != "" {
		body = strings.NewReader(Render(w.Body, ev, EscapeJSON))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return "", err
	}
	for k, v := range w.Headers {
		req.Header.Set(k, Render(v, ev, EscapeQuery))
	}
	if w.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("notify: %s: %w", w.Name, err)
	}
	defer res.Body.Close()
	said, _ := io.ReadAll(io.LimitReader(res.Body, maxBody))
	text := strings.TrimSpace(string(bytes.TrimRight(said, "\n")))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return text, fmt.Errorf("notify: %s answered %s", w.Name, res.Status)
	}
	return text, nil
}
