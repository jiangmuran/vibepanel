package httpapi

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
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

func TestAllowlistRejectionsDoNotWriteARowPerRequest(t *testing.T) {
	// The allowlist check runs before authentication and is not behind the
	// login throttle, so an outsider decides how often it happens. It wrote a
	// database row every time: measured at 237 rows/sec against the real
	// binary, 400 requests turning a 4 KiB database into 156 KiB, on the disk
	// that holds the projects.
	//
	// The path exists only when the operator turns the allowlist on, so
	// enabling the hardening was what opened it.
	ts, srv := newTestServer(t)
	_, block, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	srv.Auth.Allow = []*net.IPNet{block}
	srv.Auth.BlockedAudit = auth.NewCooldown(time.Minute)

	const requests = 50
	for i := 0; i < requests; i++ {
		res, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("request %d: status = %d, want 403", i, res.StatusCode)
		}
	}

	entries, err := srv.DB.RecentAudit(context.Background(), 500)
	if err != nil {
		t.Fatalf("RecentAudit: %v", err)
	}
	blocked := 0
	for _, e := range entries {
		if e.Event == "blocked" {
			blocked++
		}
	}
	if blocked > 1 {
		t.Errorf("%d requests wrote %d audit rows; one per window is the point", requests, blocked)
	}
	if blocked == 0 {
		t.Error("nothing was recorded at all; the gate is not supposed to hide the attack")
	}
}
