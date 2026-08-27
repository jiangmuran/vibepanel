package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// The preview endpoint reads a file somebody selected by tapping a row, which
// is a much lower bar than clicking a download button -- so what it refuses,
// and how much it is willing to read before refusing, is the whole design.
func TestFilePreview(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write := func(name string, content []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", []byte("hello\nfrom disk\n"))
	write("shot.png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 100)...))
	// A binary that is not one of the drawable formats: no magic to match, and
	// a NUL a few bytes in.
	write("a.out", []byte("\x7fELF\x02\x01\x01\x00\x00\x00"))
	// The name says text and the content does not. Deciding on the extension
	// would send this to a <pre>.
	write("log.txt", []byte("\x00\x01\x02gzipish"))

	// A file the project must never reach, one level up.
	outside := filepath.Join(filepath.Dir(root), "vp-preview-outside-"+filepath.Base(root))
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"prev"}`)
	get := func(path string) *http.Response {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/preview?path=" + path)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		return res
	}

	t.Run("shows a text file and says it is text", func(t *testing.T) {
		res := get("notes.txt")
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || string(body) != "hello\nfrom disk\n" {
			t.Fatalf("status %d body %q", res.StatusCode, body)
		}
		if got := res.Header.Get("X-Preview-Kind"); got != "text" {
			t.Errorf("X-Preview-Kind = %q, want text", got)
		}
		if res.Header.Get("X-Preview-Truncated") != "" {
			t.Error("a short file was reported as truncated")
		}
		// The bytes are a project's, on the panel's own origin. They are handed
		// to fetch() and never rendered, which is what these two headers say.
		if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
			t.Errorf("Content-Disposition = %q, want an attachment", cd)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Error("missing nosniff; a browser may decide it knows better")
		}
	})

	t.Run("names the media type of a picture without offering to render it", func(t *testing.T) {
		res := get("shot.png")
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		if got := res.Header.Get("X-Preview-Kind"); got != "image" {
			t.Errorf("X-Preview-Kind = %q, want image", got)
		}
		if got := res.Header.Get("X-Preview-Type"); got != "image/png" {
			t.Errorf("X-Preview-Type = %q, want image/png", got)
		}
		// The whole file, from byte zero: the handler read the head to sniff it
		// and has to rewind before streaming.
		if len(body) != 108 || !bytes.HasPrefix(body, []byte("\x89PNG")) {
			t.Errorf("body is %d bytes starting %q; the sniff buffer was not rewound", len(body), body[:min(8, len(body))])
		}
		// And the response itself is still a download, not a page.
		if ct := res.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type = %q; the project's bytes must never be offered inline", ct)
		}
	})

	t.Run("answers 415 for something it cannot show", func(t *testing.T) {
		for _, name := range []string{"a.out", "log.txt"} {
			res := get(name)
			res.Body.Close()
			if res.StatusCode != http.StatusUnsupportedMediaType {
				t.Errorf("%s: status = %d, want 415", name, res.StatusCode)
			}
		}
	})

	t.Run("refuses to leave the project", func(t *testing.T) {
		for _, attempt := range []string{
			"../" + filepath.Base(outside),
			"..%2f" + filepath.Base(outside),
			"/etc/passwd",
		} {
			res := get(attempt)
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				t.Errorf("%s: previewed %d bytes from outside the project", attempt, len(body))
			}
			if bytes.Contains(body, []byte("secret")) {
				t.Errorf("%s: leaked the file above the project", attempt)
			}
		}
	})

	t.Run("a directory is not a file", func(t *testing.T) {
		res := get("")
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", res.StatusCode)
		}
	})
}

// Text is truncated rather than refused, and the truncation has to be visible.
//
// Both bounds are here because they fail differently: the byte budget is what
// stops the server reading a two-gigabyte log, and the line cap is what stops
// the browser laying out a quarter of a million wrapped rows in a 280px
// column. A preview that silently stops is the same defect as a directory
// listing that silently stops, which this panel already refuses to do.
func TestPreviewTruncatesLongText(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()

	// Past the byte budget, in lines long enough that the cut lands mid-line.
	long := bytes.Repeat([]byte(strings.Repeat("x", 200)+"\n"), previewTextBytes/100)
	if err := os.WriteFile(filepath.Join(root, "big.log"), long, 0o644); err != nil {
		t.Fatal(err)
	}
	// Past the line cap while nowhere near the byte budget.
	many := bytes.Repeat([]byte("a\n"), previewTextLines*2)
	if err := os.WriteFile(filepath.Join(root, "many.log"), many, 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"trunc"}`)

	for _, c := range []struct{ name, file string }{
		{"the byte budget", "big.log"},
		{"the line cap", "many.log"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/preview?path=" + c.file)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", res.StatusCode, body)
			}
			if res.Header.Get("X-Preview-Truncated") != "true" {
				t.Error("the file was cut and the response did not say so")
			}
			if int64(len(body)) > previewTextBytes {
				t.Errorf("sent %d bytes, past the %d-byte budget", len(body), previewTextBytes)
			}
			if n := bytes.Count(body, []byte("\n")); n > previewTextLines {
				t.Errorf("sent %d lines, past the %d-line cap", n, previewTextLines)
			}
			// Cut back to a whole line, so nobody reads a half line as a whole
			// one.
			if len(body) > 0 && body[len(body)-1] != '\n' {
				t.Error("the preview ends mid-line")
			}
		})
	}
}

// The ceiling is the point of the endpoint, and it has to hold on the server.
//
// A limit the browser honours is a limit right up until somebody types the URL,
// and this one exists to stop a click costing a gigabyte of transfer.
func TestPreviewRefusesAFileOverTheCeiling(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()

	// Sparse: the size is real to Stat and costs no disk.
	f, err := os.Create(filepath.Join(root, "huge.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("\x89PNG\r\n\x1a\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(previewMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"huge"}`)
	res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/preview?path=huge.png")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d with %d bytes of body, want 413 and no file", res.StatusCode, len(body))
	}
	// The refusal names the limit. A panel that says "too big" without saying
	// how big sends the reader to the source to find out.
	if !bytes.Contains(body, []byte(fmt.Sprint(previewMaxBytes))) {
		t.Errorf("the refusal does not name the limit: %s", body)
	}
	// And the download, which has no ceiling on purpose, still works.
	dl, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/download?path=huge.png")
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Errorf("download status = %d; the preview limit has leaked onto the download", dl.StatusCode)
	}
}

// Previewing a FIFO must not hang the server, for the reason spelled out on
// TestDownloadRefusesNonRegularFiles -- and this is the easier of the two to
// reach, because it is a tap on a row rather than a click on a download button.
func TestPreviewRefusesNonRegularFiles(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"pipe"}`)

	done := make(chan int, 1)
	go func() {
		c := ts.Client()
		c.Timeout = 5 * time.Second
		res, err := c.Get(ts.URL + "/api/projects/" + project.ID + "/preview?path=pipe")
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()

	select {
	case status := <-done:
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no response: the handler is blocked on the FIFO, and shutdown will hang with it")
	}
}

// The ceiling is written down twice, once in each language, and only one of
// them is compiled against the other.
//
// The browser holds it so that clicking a two-gigabyte core dump is answered
// from the size the listing already carries, with no request at all. That is
// worth having and is not the enforcement -- the server's copy is. Which means
// the failure this pins is the quiet one: raise the server's limit alone and
// the panel keeps refusing files it would now serve, lower it alone and the
// panel offers files that come back 413.
//
// The same shape as the state enum and its three mirrors: a definition with a
// copy somewhere no compiler looks.
func TestThePreviewBoundIsTheSameOnBothSides(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "components", "panels", "preview.ts"))
	if err != nil {
		t.Fatalf("read the browser's copy: %v", err)
	}
	want := fmt.Sprintf("PREVIEW_MAX_BYTES = %d", previewMaxBytes)
	if !strings.Contains(string(src), want) {
		t.Errorf("preview.ts does not contain %q; the browser and the server disagree about "+
			"how big a file may be before a preview is refused", want)
	}
}
