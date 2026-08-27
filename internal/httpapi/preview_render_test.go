package httpapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// The isolation around a rendered preview, asserted layer by layer.
//
// Every one of these exists because removing the guard it covers leaves a
// preview that still works, still looks right, and is a click away from the
// session cookie. There is no visible symptom for any of them, which is the
// whole reason they are here rather than in a browser check.

// A rendered preview must never be able to reach the panel's origin.
func TestTheRenderedPreviewNeverGetsThePanelsOrigin(t *testing.T) {
	for _, scripts := range []bool{false, true} {
		if got := previewSandbox(scripts); strings.Contains(got, "allow-same-origin") {
			t.Errorf("previewSandbox(%v) = %q, which puts a project's HTML on the origin "+
				"holding the session cookie", scripts, got)
		}
		if got := previewCSP(scripts); strings.Contains(got, "allow-same-origin") {
			t.Errorf("previewCSP(%v) = %q, which puts a project's HTML on the origin "+
				"holding the session cookie", scripts, got)
		}
	}
}

// Nothing runs unless somebody asked for it, and the server is what decides.
func TestScriptsAreOffUnlessAsked(t *testing.T) {
	if got := previewSandbox(false); got != "" {
		t.Errorf("previewSandbox(false) = %q, want the empty sandbox: the default has to be "+
			"a document that cannot execute", got)
	}
	if got := previewSandbox(true); got != "allow-scripts" {
		t.Errorf("previewSandbox(true) = %q", got)
	}
	off := previewCSP(false)
	if !strings.Contains(off, "sandbox") || strings.Contains(off, "sandbox allow-scripts") {
		t.Errorf("previewCSP(false) = %q, want a bare sandbox directive", off)
	}
	if strings.Contains(off, "script-src") {
		t.Errorf("previewCSP(false) = %q names script-src; with default-src 'none' that can "+
			"only widen what runs", off)
	}
	on := previewCSP(true)
	if !strings.Contains(on, "sandbox allow-scripts") || !strings.Contains(on, "script-src") {
		t.Errorf("previewCSP(true) = %q", on)
	}
}

// The policy is what keeps "the panel does not phone home" true for a document
// somebody else wrote.
func TestARenderedPreviewHasNoNetwork(t *testing.T) {
	for _, scripts := range []bool{false, true} {
		csp := previewCSP(scripts)
		if !strings.HasPrefix(csp, "default-src 'none'") {
			t.Fatalf("previewCSP(%v) = %q, and without default-src 'none' an <img src=https://…> "+
				"or a nested <iframe> in a project's HTML is an outbound request nobody asked for",
				scripts, csp)
		}
		// The four that would each reopen it on their own.
		for _, forbidden := range []string{"connect-src", "frame-src", "child-src", "*"} {
			if strings.Contains(csp, forbidden) {
				t.Errorf("previewCSP(%v) = %q contains %q", scripts, csp, forbidden)
			}
		}
		if !strings.Contains(csp, "form-action 'none'") {
			t.Errorf("previewCSP(%v) = %q lets a form in a preview post somewhere", scripts, csp)
		}
		if !strings.Contains(csp, "base-uri 'none'") {
			t.Errorf("previewCSP(%v) = %q lets the document move its own base", scripts, csp)
		}
	}
}

// The panel has to be able to frame it, and every other response must not be.
func TestOnlyTheRenderRouteRelaxesFrameAncestors(t *testing.T) {
	for _, scripts := range []bool{false, true} {
		if !strings.Contains(previewCSP(scripts), "frame-ancestors 'self'") {
			t.Errorf("previewCSP(%v) does not allow the panel to frame it, so the preview is a "+
				"blank box", scripts)
		}
	}
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("an ordinary response carries %q; relaxing frame-ancestors globally would let "+
			"any page frame the terminal", got)
	}
}

func TestRenderedPreviewServing(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("report.html", "<h1>hello</h1><script>fetch('/api/state')</script>")
	write("chart.svg", `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`)
	write("notes.txt", "<h1>not a page</h1>")
	write("a.out", "\x7fELF\x02\x01\x01\x00\x00\x00")

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"render"}`)
	get := func(q string) *http.Response {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/preview/render?path=" + q)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		return res
	}

	t.Run("serves HTML inline with the isolation headers", func(t *testing.T) {
		res := get("report.html")
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		if got := res.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		// attachment here means the iframe downloads the file instead of
		// drawing it, which is a preview that silently does nothing.
		if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline;") {
			t.Errorf("Content-Disposition = %q", got)
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q", got)
		}
		if got := res.Header.Get("Content-Security-Policy"); got != previewCSP(false) {
			t.Errorf("Content-Security-Policy = %q, want the no-scripts policy", got)
		}
		if !strings.Contains(string(body), "<h1>hello</h1>") {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("only a query of exactly 1 turns scripts on", func(t *testing.T) {
		for _, q := range []string{"", "&scripts=0", "&scripts=true", "&scripts=yes", "&scripts=01"} {
			res := get("report.html" + q)
			got := res.Header.Get("Content-Security-Policy")
			res.Body.Close()
			if got != previewCSP(false) {
				t.Errorf("scripts%q gave %q; the switch between cannot-execute and can must be "+
					"reachable by one spelling", q, got)
			}
		}
		res := get("report.html&scripts=1")
		defer res.Body.Close()
		if got := res.Header.Get("Content-Security-Policy"); got != previewCSP(true) {
			t.Errorf("scripts=1 gave %q", got)
		}
	})

	t.Run("serves SVG as SVG and nothing else as anything", func(t *testing.T) {
		res := get("chart.svg")
		defer res.Body.Close()
		if got := res.Header.Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := res.Header.Get("Content-Security-Policy"); got != previewCSP(false) {
			t.Errorf("an SVG is a scriptable document and got %q", got)
		}
	})

	t.Run("refuses anything that is not a page", func(t *testing.T) {
		// The narrow whitelist is what stops this route being a general "serve
		// a project's bytes with a renderable content type" endpoint.
		for _, name := range []string{"notes.txt", "a.out"} {
			res := get(name)
			res.Body.Close()
			if res.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s: status %d, want 415", name, res.StatusCode)
			}
		}
	})

	t.Run("refuses a path that leaves the project", func(t *testing.T) {
		// One path check in this codebase, and this route uses it.
		res := get("..%2f..%2fetc%2fpasswd")
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d, want a refusal", res.StatusCode)
		}
	})

	t.Run("needs the session like everything else", func(t *testing.T) {
		bare := &http.Client{}
		res, err := bare.Get(ts.URL + "/api/projects/" + project.ID + "/preview/render?path=report.html")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", res.StatusCode)
		}
	})
}

// The safe endpoint stays safe. Adding the markup header to it must not have
// turned it into something a browser will render.
func TestThePlainPreviewStillNeverRenders(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"plain"}`)
	res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/preview?path=report.html")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q; /preview must never offer a project's bytes as something "+
			"to render", got)
	}
	if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := res.Header.Get("X-Preview-Kind"); got != "text" {
		t.Errorf("X-Preview-Kind = %q", got)
	}
	// The header that tells the panel a second endpoint would draw it. Without
	// it there is no way to offer the choice, and the feature is unreachable.
	if got := res.Header.Get("X-Preview-Markup"); got != "html" {
		t.Errorf("X-Preview-Markup = %q, want html", got)
	}
}
