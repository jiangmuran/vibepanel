package httpapi

import (
	"net/http"
	"testing"
)

// TestSecurityHeaders pins four headers on every response.
//
// Referrer-Policy is the one this was written for. The terminal turns every
// URL an agent prints into a clickable link, so a link click used to send
// `Referer: https://<the panel>/` to a host chosen by whatever the agent read
// or was told to print. Measured against a listener standing in for somewhere
// else on the internet, before the fix: `referer: http://127.0.0.1:38475/`,
// the panel's exact origin. Afterwards: null.
//
// The rest are free and are here so that adding them is not a separate errand:
// nosniff, `frame-ancestors 'none'` instead of a full policy that would have
// to survive the inline styles xterm and Tailwind generate, and COOP so a page
// the panel opens cannot reach back into its window.
func TestSecurityHeaders(t *testing.T) {
	ts, _ := newTestServer(t)

	want := map[string]string{
		"Referrer-Policy":            "no-referrer",
		"X-Content-Type-Options":     "nosniff",
		"Content-Security-Policy":    "frame-ancestors 'none'",
		"Cross-Origin-Opener-Policy": "same-origin",
	}

	// Both the API and whatever serves the page: a header on one and not the
	// other protects nothing, and the link click that leaks the referrer
	// happens on the page.
	for _, path := range []string{"/api/health", "/"} {
		res, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		func() {
			defer res.Body.Close()
			for h, v := range want {
				if got := res.Header.Get(h); got != v {
					t.Errorf("GET %s: %s = %q, want %q", path, h, got, v)
				}
			}
		}()
	}

	// A refused request is still a response, and an attacker's browser is
	// still a browser.
	anon := &http.Client{} // no cookie jar: nobody, from the server's point of view
	res, err := anon.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("an unauthenticated /api/state succeeded; this test needs a refusal")
	}
	if got := res.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("a refused request carried Referrer-Policy %q", got)
	}
}
