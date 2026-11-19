package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The inline route serves a project's bytes on the panel's origin with a type
// a browser renders, so what it may name is the whole of its safety: the
// media list, and SVG, and every other file as the download's octet-stream
// attachment. Walked over the viewer's fixtures and over files written to be
// the wrong answer -- a page, a page that calls itself an image, an SVG with a
// script -- the property is the same for each: a listed media type served
// inline and sandboxed, or a download, and never anything else.
func TestTheInlineRouteNamesOnlyMediaTypes(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	fixtures := filepath.Join("..", "..", "web", "scripts", "fixtures", "preview", "files")
	want := map[string]string{
		"media/clip.mp4":    "video/mp4",
		"media/clip.webm":   "video/webm",
		"media/tone.mp3":    "audio/mpeg",
		"media/tone.ogg":    "audio/ogg",
		"media/tone.wav":    "audio/wav",
		"img/alpha.png":     "image/png",
		"img/photo.jpg":     "image/jpeg",
		"img/anim.gif":      "image/gif",
		"img/photo.webp":    "image/webp",
		"img/favicon.ico":   "image/x-icon",
		"img/one-pixel.bmp": "image/bmp",
		"img/icon.svg":      "image/svg+xml",
		"docs/manual.pdf":   "application/pdf",
		// Everything below is a download.
		"page.html":              "",
		"README.md":              "",
		"src/app.ts":             "",
		"bin/program":            "",
		"archives/bundle.zip":    "",
		"archives/bundle.tar.gz": "",
		"data/config.json":       "",
	}
	for rel := range want {
		b, err := os.ReadFile(filepath.Join(fixtures, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostile := map[string]string{
		// A page wearing an image's name: decided by its bytes, it is text.
		"looks.png": "<html><script>alert(1)</script></html>",
		// HTML in an .svg: an SVG needs its element as well as its name.
		"page.svg": "<!doctype html><script>alert(2)</script>",
		// A real SVG with a script in it: served as an image, sandboxed.
		"script.svg": `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(3)</script></svg>`,
		"x.html":     "<script>alert(4)</script>",
		// A database is listed by its own route; here it is a download.
		"app.db": "SQLite format 3\x00",
	}
	for rel, body := range hostile {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want["looks.png"] = ""
	want["page.svg"] = ""
	want["script.svg"] = "image/svg+xml"
	want["x.html"] = ""
	want["app.db"] = ""

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"inline"}`)
	for rel, mime := range want {
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/inline?path=" + url.QueryEscape(rel))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		ct, cd, csp := res.Header.Get("Content-Type"), res.Header.Get("Content-Disposition"), res.Header.Get("Content-Security-Policy")
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", rel)
		}
		switch {
		case ct == "application/octet-stream":
			if !strings.HasPrefix(cd, "attachment;") {
				t.Errorf("%s: octet-stream but %q", rel, cd)
			}
		case ct == "image/svg+xml" || slices.Contains(browse.MediaTypes, ct):
			if !strings.HasPrefix(cd, "inline;") {
				t.Errorf("%s: %s but %q", rel, ct, cd)
			}
			// A PDF too: pdf.js fetches its bytes, and no <object> embeds it.
			if !strings.HasPrefix(csp, "sandbox") {
				t.Errorf("%s: %s served inline without a sandbox: %q", rel, ct, csp)
			}
			// The panel's own frame-ancestors 'none' is kept, not dropped with
			// the CSP it replaces: nothing frames these, the panel included.
			if !strings.Contains(csp, "frame-ancestors 'none'") {
				t.Errorf("%s: %s served inline with %q, want frame-ancestors 'none'", rel, ct, csp)
			}
		default:
			t.Errorf("%s: served as %q, which is neither a listed media type nor a download", rel, ct)
		}
		if mime == "" && ct != "application/octet-stream" {
			t.Errorf("%s: served as %q, want a download", rel, ct)
		}
		if mime != "" && ct != mime {
			t.Errorf("%s: served as %q, want %q", rel, ct, mime)
		}
	}
}

// A video seeks by asking for ranges, and the route answers them.
func TestTheInlineRouteAnswersRanges(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "scripts", "fixtures", "preview", "files", "media", "clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clip.mp4"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"ranges"}`)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/projects/"+project.ID+"/inline?path=clip.mp4", nil)
	req.Header.Set("Range", "bytes=4-11")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || string(got) != string(b[4:12]) {
		t.Errorf("Range 4-11: %d %q, want 206 %q", res.StatusCode, got, b[4:12])
	}
	if res.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges %q", res.Header.Get("Accept-Ranges"))
	}
}
