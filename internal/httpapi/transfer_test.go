package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// Uploads and downloads reach the filesystem with a path from a URL, which is
// the one place in this program where getting containment wrong hands out the
// whole disk.
func TestFileTransfer(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "notes.txt"), []byte("hello from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file the project must never be able to reach, one level up.
	outside := filepath.Join(filepath.Dir(root), "vp-outside-"+filepath.Base(root))
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"files"}`)
	get := func(path string) *http.Response {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/download?path=" + path)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		return res
	}

	t.Run("downloads a file", func(t *testing.T) {
		res := get("sub/notes.txt")
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || string(body) != "hello from disk" {
			t.Fatalf("status %d body %q", res.StatusCode, body)
		}
		// A project can contain anything an agent wrote, including HTML. It
		// must arrive as a download, never as a page on the panel's origin.
		if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
			t.Errorf("Content-Disposition = %q, want an attachment", cd)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Error("missing nosniff; a browser may still decide to render it")
		}
	})

	t.Run("refuses to leave the project", func(t *testing.T) {
		for _, attempt := range []string{
			"../" + filepath.Base(outside),
			"..%2f" + filepath.Base(outside),
			"sub/../../" + filepath.Base(outside),
			"/etc/passwd",
		} {
			res := get(attempt)
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				t.Errorf("%s: served %d bytes from outside the project", attempt, len(body))
			}
			if bytes.Contains(body, []byte("secret")) {
				t.Errorf("%s: leaked the file above the project", attempt)
			}
		}
	})

	upload := func(dir, name, content string) *http.Response {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		mw.Close()
		res, err := ts.Client().Post(
			ts.URL+"/api/projects/"+project.ID+"/upload?path="+dir, mw.FormDataContentType(), &buf)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return res
	}

	t.Run("uploads into a directory and reports where", func(t *testing.T) {
		res := upload("sub", "shot.png", "PNGDATA")
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		var out struct {
			Paths []string `json:"paths"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		// The path comes back so it can be typed at the prompt; a relative one
		// would be wrong the moment the shell has cd'd somewhere else.
		want := filepath.Join(root, "sub", "shot.png")
		if len(out.Paths) != 1 || !strings.HasSuffix(out.Paths[0], filepath.Join("sub", "shot.png")) {
			t.Fatalf("paths = %v, want one ending in %s", out.Paths, want)
		}
		got, err := os.ReadFile(want)
		if err != nil || string(got) != "PNGDATA" {
			t.Fatalf("file on disk: %q, %v", got, err)
		}
	})

	t.Run("never overwrites", func(t *testing.T) {
		// An agent may be reading the file this would replace. Refusing is the
		// only safe answer; the caller renames.
		res := upload("sub", "notes.txt", "clobbered")
		defer res.Body.Close()
		if res.StatusCode != http.StatusConflict {
			t.Errorf("status %d, want 409", res.StatusCode)
		}
		got, _ := os.ReadFile(filepath.Join(root, "sub", "notes.txt"))
		if string(got) != "hello from disk" {
			t.Errorf("the existing file was replaced: %q", got)
		}
	})

	t.Run("a filename with a path in it keeps only its last element", func(t *testing.T) {
		// The browser supplies this string, and on some platforms it has
		// always contained a full path, so stripping it is the friendly
		// behaviour rather than rejecting the upload. What must not happen is
		// the path being honoured.
		res := upload("sub", "../../escaped.txt", "stripped")
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); err == nil {
			t.Error("a file was written above the project root")
		}
		if got, err := os.ReadFile(filepath.Join(root, "sub", "escaped.txt")); err != nil {
			t.Errorf("the file did not land in the requested directory: %v", err)
		} else if string(got) != "stripped" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("a climbing target directory lands inside, not outside", func(t *testing.T) {
		// Resolve cleans against "/" before joining, so "../.." is clamped to
		// the project root rather than rejected. That is deliberate — the
		// property to hold is where the bytes end up, not which status code
		// comes back.
		res := upload("../..", "anywhere.txt", "landed")
		defer res.Body.Close()
		var out struct {
			Paths []string `json:"paths"`
		}
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			for _, p := range out.Paths {
				if !strings.HasPrefix(p, root+string(filepath.Separator)) {
					t.Errorf("wrote %s, which is outside %s", p, root)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "anywhere.txt")); err == nil {
			t.Error("a file was written above the project root")
		}
	})

	_ = context.Background()
}
