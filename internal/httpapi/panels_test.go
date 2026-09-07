package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second paste of the same name gets a new name, not an error.
//
// Pasting a screenshot at an agent produces `image.png` every time, from every
// operating system, so the second paste of a session always failed with
// `409 image.png already exists` and the suggested fix was to go and rename a
// file the person never named. 「粘贴文件不应当以文件重复为由报错 应该自动加-1 -2」.
//
// What must survive the change is the reason the refusal existed: an upload
// may never quietly replace a file an agent is working on.
func TestASecondUploadOfTheSameNameIsRenamedNotRefused(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("the original"), 0o600); err != nil {
		t.Fatal(err)
	}

	var names []string
	for i := 0; i < 3; i++ {
		f, target, err := createUnique(dir, "image.png")
		if err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
		if _, err := f.WriteString("new"); err != nil {
			t.Fatal(err)
		}
		f.Close()
		names = append(names, filepath.Base(target))
	}

	want := []string{"image-1.png", "image-2.png", "image-3.png"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("upload %d landed at %q, want %q", i, names[i], want[i])
		}
	}

	// The suffix goes before the extension: image-1.png is a picture and
	// image.png-1 is not.
	for _, n := range names {
		if filepath.Ext(n) != ".png" {
			t.Errorf("%q lost its extension", n)
		}
	}

	// And the file that was already there is untouched. This is the property
	// the 409 existed to protect and the only one that may not be traded away.
	got, err := os.ReadFile(filepath.Join(dir, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the original" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
}

// A name with no extension keeps the suffix at the end.
func TestUniqueNamesWithoutAnExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, target, err := createUnique(dir, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "notes-1" {
		t.Errorf("landed at %q, want notes-1", filepath.Base(target))
	}
}

// The picker opens at home and can leave it.
//
// It could not. The browse root *was* the home directory, so containment and
// "where to start" were one decision, and a panel running as root listed
// /root and nothing else -- 「为什么我的只能识别root文件夹下的文件和文件夹，我
// 无法打开根目录」. Every repository on a server lives somewhere else.
//
// The two are separate now, and the seam is the query parameter: absent means
// home, present-and-empty means the filesystem root. Get() answers "" to both,
// so the handler has to ask Has(), and getting that wrong sends everyone who
// clicked the first crumb back to their home directory instead of to "/".
func TestBrowseOpensAtHomeAndCanLeaveIt(t *testing.T) {
	home := t.TempDir()
	// Through EvalSymlinks because browse.Dirs reports its path relative to a
	// resolved root, and /tmp is a symlink on more than one platform.
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(realHome, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", realHome)

	// Somewhere outside home entirely, which is the case that used to be
	// unreachable by anything but typing the path from memory.
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(elsewhere, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := &Server{}
	get := func(t *testing.T, url string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleBrowse(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", url, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return out
	}
	names := func(body map[string]any) []string {
		var out []string
		entries, _ := body["entries"].([]any)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m["name"].(string))
			}
		}
		return out
	}

	t.Run("no path opens at home", func(t *testing.T) {
		body := get(t, "/api/browse")
		if body["root"] != "/" {
			t.Errorf("root = %v, want /: the picker is rooted at the filesystem now", body["root"])
		}
		if body["home"] != realHome {
			t.Errorf("home = %v, want %q", body["home"], realHome)
		}
		// Relative to "/", which is what every path on this wire is.
		if want := strings.TrimPrefix(realHome, "/"); body["path"] != want {
			t.Errorf("path = %v, want %q", body["path"], want)
		}
		if got := names(body); len(got) != 1 || got[0] != "projects" {
			t.Errorf("listed %v, want the home directory's contents", got)
		}
	})

	t.Run("an empty path is the filesystem root", func(t *testing.T) {
		body := get(t, "/api/browse?path=")
		if body["path"] != "" {
			t.Errorf("path = %v, want the empty path: ?path= is / and not home", body["path"])
		}
		if body["parent"] != nil {
			t.Errorf("the root reports a parent: %v", body["parent"])
		}
		if len(names(body)) == 0 {
			t.Error("nothing under /, which cannot be true on any machine this runs on")
		}
	})

	t.Run("an absolute path outside home is listed", func(t *testing.T) {
		body := get(t, "/api/browse?path="+url.QueryEscape(elsewhere))
		if got := names(body); len(got) != 1 || got[0] != "repo" {
			t.Errorf("listed %v, want the contents of %s", got, elsewhere)
		}
	})
}
