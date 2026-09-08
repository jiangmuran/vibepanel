package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

/*
A directory served as a page, and the four things that make it safe to.

Every one of these was measured in a browser before it was written down, and
two of them came out the opposite of the design: see the note at the top of
preview.go.
*/

// TestADirectoryPreviewServesItsOwnAssets is the feature, and the reason the
// session cannot be the authority.
//
// The first version required a signed-in session as well as the token. In a
// browser the page rendered and *nothing else loaded*: the sandbox gives the
// document an opaque origin, the document's own asset requests are therefore
// cross-origin, SameSite keeps the cookie off them, and the 401 comes back as
// ERR_BLOCKED_BY_ORB rather than as a 401. The sandbox that stops the page
// stealing the session is the same thing that stops it proving it has one.
func TestADirectoryPreviewServesItsOwnAssets(t *testing.T) {
	ts, srv := newTestServer(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "index.html"), `<link rel=stylesheet href=style.css><h1>OK</h1>`)
	write(t, filepath.Join(root, "style.css"), `h1{color:red}`)
	write(t, filepath.Join(root, "sub", "note.txt"), "SUBFILE")

	tok := makePreview(t, ts, root)

	for _, tc := range []struct{ path, want string }{
		{"/", "<h1>OK</h1>"},
		{"/style.css", "h1{color:red}"},
		{"/sub/note.txt", "SUBFILE"},
	} {
		body, code := getRaw(t, ts, "/preview/"+tok+tc.path)
		if code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.path, code)
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: body %q does not contain %q", tc.path, body, tc.want)
		}
	}
	_ = srv
}

// TestAPreviewCannotReachOutOfItsDirectory covers the traversal shapes.
//
// A directory of build output is exactly where a symlink pointing elsewhere is
// plausible, which is why `browse.Resolve` resolves rather than string-prefixes
// -- and why the symlink case is here rather than only the `..` ones.
func TestAPreviewCannotReachOutOfItsDirectory(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(root, "index.html"), "inside")
	write(t, filepath.Join(outside, "secret.txt"), "SHOULD_NOT_BE_SERVED")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	tok := makePreview(t, ts, root)

	for _, path := range []string{
		"/../../etc/passwd",
		"/%2e%2e/%2e%2e/etc/passwd",
		"/..%2f..%2fetc/passwd",
		"/escape/secret.txt",
	} {
		body, code := getRaw(t, ts, "/preview/"+tok+path)
		if code == http.StatusOK {
			t.Errorf("%s: served %d with %q; it is outside the root", path, code, body)
		}
		if strings.Contains(body, "SHOULD_NOT_BE_SERVED") {
			t.Errorf("%s: the file outside the root came back", path)
		}
	}
}

// TestAPreviewSaysNothingAboutWhereItIs.
//
// A preview link is a capability over one directory. It should not also tell
// whoever holds it where that directory lives on the machine -- the account
// name and the filesystem layout are in an absolute path, and neither is part
// of what was shared.
func TestAPreviewSaysNothingAboutWhereItIs(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")

	tok := makePreview(t, ts, root)
	body, code := getRaw(t, ts, "/preview/"+tok+"/")
	if code != http.StatusOK {
		t.Fatalf("index: status %d", code)
	}
	if strings.Contains(body, root) {
		t.Errorf("the listing contains its own absolute path %q:\n%s", root, body)
	}
	if !strings.Contains(body, "a.txt") {
		t.Errorf("the listing does not name the file it is listing:\n%s", body)
	}
	// A wrong token and a missing file answer the same way, so probing cannot
	// tell "no such link" from "no such file".
	if _, code := getRaw(t, ts, "/preview/nosuchtoken/"); code != http.StatusNotFound {
		t.Errorf("an unknown token answered %d, want 404", code)
	}
}

// TestAPreviewIsSandboxedOnEveryResponse.
//
// The header is what makes this safe on a single origin: it applies the iframe
// sandbox to a *top-level* document, so the page loads into an opaque origin
// and cannot read the session cookie or call the API with it. Measured in a
// browser: `window.origin` is "null", `document.cookie` throws SecurityError,
// and `fetch('/api/state')` is blocked.
//
// On every response and not only the HTML, because any of them can be
// navigated to directly, and the one that is not sandboxed is the one that
// runs with the panel's origin.
//
// `allow-same-origin` must never appear. With `allow-scripts` beside it the
// sandbox gives everything back, and the two together are the whole of what
// this prevents.
func TestAPreviewIsSandboxedOnEveryResponse(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "index.html"), "<h1>x</h1>")
	write(t, filepath.Join(root, "style.css"), "h1{}")
	write(t, filepath.Join(root, "data.json"), `{"a":1}`)

	tok := makePreview(t, ts, root)
	for _, path := range []string{"/", "/style.css", "/data.json"} {
		res, err := ts.Client().Get(ts.URL + "/preview/" + tok + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		csp := res.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s: no sandbox in %q", path, csp)
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%s: allow-same-origin makes the sandbox theatre: %q", path, csp)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: a file whose first bytes look like markup could be rendered", path)
		}
		if res.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: the token is in the path and would travel in a Referer", path)
		}
	}
}

// TestTheDirectoryCSPNamesTheOriginRatherThanSelf.
//
// `'self'` is the obvious spelling and it is wrong here: `sandbox` puts the
// document in an opaque origin, and an opaque origin matches no source
// expression -- so `'self'` forbids exactly the requests this feature exists to
// allow. Naming the origin is the same set of requests, written the only way
// that works, and it still refuses every other host.
func TestTheDirectoryCSPNamesTheOriginRatherThanSelf(t *testing.T) {
	csp := dirPreviewCSP("https://panel.example:18443", false)
	// The *fetch* directives only. `frame-ancestors 'self'` is correct and
	// stays: that one is about who may embed this, and "this" is embedded by
	// the panel, whose origin is not opaque. Asserting against the whole string
	// failed on it, which is the check being wrong rather than the policy.
	for _, d := range []string{"default-src", "style-src", "script-src", "img-src", "font-src"} {
		i := strings.Index(csp, d+" ")
		if i < 0 {
			t.Errorf("%s missing from %q", d, csp)
			continue
		}
		clause := csp[i:]
		if j := strings.Index(clause, ";"); j >= 0 {
			clause = clause[:j]
		}
		if strings.Contains(clause, "'self'") {
			t.Errorf("%q: 'self' matches nothing from an opaque origin", clause)
		}
	}
	if !strings.Contains(csp, "default-src https://panel.example:18443") {
		t.Errorf("the origin is not named: %q", csp)
	}
	// Nothing outbound. A preview holding <img src="https://someone/?leak">
	// must make no request, which is the promise the single-file door keeps
	// with default-src 'none' and this keeps in the only form open to it.
	if strings.Contains(csp, "*") || strings.Contains(csp, "https: ") {
		t.Errorf("a wildcard source would let a preview phone home: %q", csp)
	}
	// With no origin to name, nothing loads rather than everything.
	if none := dirPreviewCSP("", false); !strings.Contains(none, "default-src 'none'") {
		t.Errorf("an unknown origin should forbid rather than allow: %q", none)
	}
}

// makePreview creates a link the way the panel does and returns its token,
// which is readable exactly once.
func makePreview(t *testing.T, ts *httptest.Server, root string) string {
	t.Helper()
	made := postJSON[struct {
		Token string `json:"token"`
		Root  string `json:"root"`
	}](t, ts, "/api/settings/previews",
		`{"root":`+strconv.Quote(root)+`,"name":"probe","expiresIn":3600}`)
	if made.Token == "" {
		t.Fatal("no token came back")
	}
	return made.Token
}

// getRaw fetches a preview path without following anything or decoding JSON.
//
// `--path-as-is` in curl terms: the client must not normalise `..` out of the
// path before it is sent, or the traversal cases below are testing the client
// rather than the server. `http.NewRequest` with an `Opaque` URL is how that is
// spelled here.
func getRaw(t *testing.T, ts *httptest.Server, path string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.URL.Opaque = path
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body), res.StatusCode
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAPreviewMayReachACDNOnlyWhenTheLinkSaidSo.
//
// The default policy is the one that makes a preview safe to click, and it is
// also why a page an agent just wrote renders blank: three.js from cdnjs, a
// font from Google, and a console full of refusals. 「预览的时候好像会报错好多」.
//
// So the widening is per link and off unless asked for. What stays put either
// way is the half that protects the *panel* rather than the directory: the
// sandbox, the opaque origin it produces, base-uri and frame-ancestors. A link
// setting must not be able to reach those, which is what the second half of
// this checks.
func TestAPreviewMayReachACDNOnlyWhenTheLinkSaidSo(t *testing.T) {
	const origin = "https://panel.example:18443"
	closed := dirPreviewCSP(origin, false)
	open := dirPreviewCSP(origin, true)

	for _, d := range []string{"script-src", "style-src", "font-src", "img-src"} {
		if wildcard(closed, d) {
			t.Errorf("%s reaches the network with the link switched off: %q", d, clauseOf(closed, d))
		}
		if !wildcard(open, d) {
			t.Errorf("%s still cannot load a CDN with the link switched on: %q", d, clauseOf(open, d))
		}
	}
	// Not widened, in either mode. connect-src is the obvious exfiltration road
	// and closing it is cheap; it is not what makes this safe, and the comment
	// on dirPreviewCSP says so rather than letting a reader assume otherwise.
	for _, csp := range []string{closed, open} {
		if !strings.Contains(csp, "connect-src 'none'") {
			t.Errorf("connect-src is open: %q", csp)
		}
		if !strings.Contains(csp, "base-uri 'none'") {
			t.Errorf("base-uri is open: %q", csp)
		}
		if !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
			t.Errorf("the sandbox moved: %q", csp)
		}
		if !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("frame-ancestors moved: %q", csp)
		}
	}
	// form-action names the panel's own origin in both, never https: -- a form
	// that may post anywhere is a page that may send the directory somewhere
	// with no script at all.
	if wildcard(open, "form-action") {
		t.Errorf("form-action widened with the rest: %q", clauseOf(open, "form-action"))
	}
}

// wildcard reports whether a directive carries the bare `https:` scheme source
// -- "anywhere over TLS" -- as opposed to naming an origin.
//
// By token and not by substring, which is how the first version of this test
// failed on its own subject: the panel's origin *is* `https://panel...`, so
// `strings.Contains(clause, "https:")` was true of every clause in both modes
// and the test reported the closed policy as open.
func wildcard(csp, directive string) bool {
	for _, f := range strings.Fields(clauseOf(csp, directive)) {
		if f == "https:" {
			return true
		}
	}
	return false
}

// clauseOf pulls one directive out of a policy.
func clauseOf(csp, directive string) string {
	i := strings.Index(csp, directive+" ")
	if i < 0 {
		return ""
	}
	c := csp[i:]
	if j := strings.Index(c, ";"); j >= 0 {
		c = c[:j]
	}
	return c
}

// TestAChosenPreviewAddressCannotLeaveItsSegment.
//
// The address a person types becomes a URL path segment, so the characters it
// refuses are not a house style: a slash makes it two segments, a dot lets `.`
// and `..` in, and a per-cent lets a caller re-encode either of those past the
// place that resolves them. The regexp is the only thing between a text field
// and a path, and every one of these is a way somebody would try.
func TestAChosenPreviewAddressCannotLeaveItsSegment(t *testing.T) {
	for _, bad := range []string{
		"a/b", "..", "../etc", "a.b", "a%2fb", "a%2e%2e", "a b", "ab\n",
		"-lead", "trail-", "ab", "", // too short, and empty means "generate one"
		strings.Repeat("a", 65),
	} {
		got, err := previewToken(bad)
		if bad == "" {
			// Empty is the ordinary case: no address asked for, so a random
			// token. It has to be long, or "visible to everyone" is the
			// default rather than a choice.
			if err != nil || len(got) < 40 {
				t.Errorf("an empty address gave %q, %v; want a long random token", got, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("previewToken(%q) was accepted as %q", bad, got)
		}
	}
	for _, ok := range []string{"demo", "my-demo", "a1-b2-c3", "x2y", strings.Repeat("a", 64)} {
		if got, err := previewToken(ok); err != nil || got != ok {
			t.Errorf("previewToken(%q) = %q, %v; want it kept", ok, got, err)
		}
	}
	// Folded, because a person reading an address off a screen does not
	// reproduce its case, and two that differ only in case are one address to
	// everybody except the database.
	if got, _ := previewToken("My-Demo"); got != "my-demo" {
		t.Errorf("case was not folded: %q", got)
	}
}

// TestAChosenAddressOpensThePreview, end to end, and the collision after it.
func TestAChosenAddressOpensThePreview(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "index.html"), "<h1>NAMED</h1>")

	made := postJSON[struct {
		Token string `json:"token"`
	}](t, ts, "/api/settings/previews",
		`{"root":`+strconv.Quote(root)+`,"name":"probe","address":"my-demo","expiresIn":0}`)
	if made.Token != "my-demo" {
		t.Fatalf("token = %q, want the address that was asked for", made.Token)
	}
	body, code := getRaw(t, ts, "/preview/my-demo/")
	if code != http.StatusOK || !strings.Contains(body, "NAMED") {
		t.Errorf("the named preview answered %d: %s", code, body)
	}

	// The same word again, from the same account. The uniqueness is the
	// database's, so this is the answer whoever loses the race gets.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/previews",
		strings.NewReader(`{"root":`+strconv.Quote(root)+`,"name":"again","address":"my-demo"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	if res.StatusCode != http.StatusConflict {
		t.Errorf("a second link on the same address = %d, want 409", res.StatusCode)
	}
}
