package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

func TestResolveAndPreviewServerFiles(t *testing.T) {
	ts, srv := newTestServer(t)

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	projRoot := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(projRoot, "internal", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projRoot, "internal", "sub", "handler.go"), []byte("package sub\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	geminiDir := filepath.Join(fakeHome, ".gemini", "antigravity-cli", "brain", "conv-1")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	geminiFile := filepath.Join(geminiDir, "walkthrough.md")
	if err := os.WriteFile(geminiFile, []byte("# Walkthrough\n\nAll good.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outsideDir := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spaceFile := filepath.Join(outsideDir, "report 2026 中文.txt")
	if err := os.WriteFile(spaceFile, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	colonFile := filepath.Join(outsideDir, "weird:12")
	if err := os.WriteFile(colonFile, []byte("literal colon"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink inside the project pointing at an outside directory: must be
	// refused as a folder (outside_dir), while a symlink to an outside file
	// resolves as a server file (inProject: false).
	escapeDirLink := filepath.Join(projRoot, "escape-dir")
	if err := os.Symlink(outsideDir, escapeDirLink); err != nil {
		t.Fatal(err)
	}
	escapeFileLink := filepath.Join(projRoot, "escape-file.md")
	if err := os.Symlink(geminiFile, escapeFileLink); err != nil {
		t.Fatal(err)
	}

	fifoPath := filepath.Join(outsideDir, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+projRoot+`","name":"demo"}`)
	sess, err := srv.DB.CreateSession(context.Background(), store.Session{
		ID:        "sess_resolve_test",
		ProjectID: project.ID,
		TmuxName:  "vp_resolve_test",
		Title:     "codex",
		Command:   "codex",
		CWD:       filepath.Join(projRoot, "internal"),
		State:     "working",
	})
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(q url.Values) resolvedPathResponse {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/fs/resolve?" + q.Encode())
		if err != nil {
			t.Fatalf("GET /api/fs/resolve: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status = %d, body = %s", res.StatusCode, body)
		}
		var out resolvedPathResponse
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	t.Run("resolves cwd-relative and project-relative files inside the project with line numbers", func(t *testing.T) {
		got := resolve(url.Values{"target": {"sub/handler.go:3:1"}, "sessionId": {sess.ID}})
		if got.Kind != "file" || !got.InProject || got.ProjectID != project.ID || got.Path != "internal/sub/handler.go" || got.Line != 3 {
			t.Fatalf("cwd-relative got %+v", got)
		}
		// Even though sess.Cwd is projRoot/internal, repo-root-relative paths
		// still resolve against projRoot when not under Cwd.
		gotRootRel := resolve(url.Values{"target": {"internal/sub/handler.go#L2-L3"}, "sessionId": {sess.ID}})
		if gotRootRel.Kind != "file" || !gotRootRel.InProject || gotRootRel.Path != "internal/sub/handler.go" || gotRootRel.Line != 2 {
			t.Fatalf("project-relative got %+v", gotRootRel)
		}
		gotColAnchor := resolve(url.Values{"target": {"internal/sub/handler.go#L3C5"}, "sessionId": {sess.ID}})
		if gotColAnchor.Kind != "file" || gotColAnchor.Line != 3 {
			t.Fatalf("#L3C5 anchor got %+v", gotColAnchor)
		}
		gotParenLine := resolve(url.Values{"target": {"sub/handler.go(3,1)"}, "sessionId": {sess.ID}})
		if gotParenLine.Kind != "file" || gotParenLine.Line != 3 {
			t.Fatalf("(3,1) anchor got %+v", gotParenLine)
		}
	})

	t.Run("resolves ~/.gemini and arbitrary server files outside the project, and previews them", func(t *testing.T) {
		got := resolve(url.Values{
			"target":    {"~/.gemini/antigravity-cli/brain/conv-1/walkthrough.md#L3"},
			"sessionId": {sess.ID},
		})
		if got.Kind != "file" || got.InProject || got.Name != "walkthrough.md" || got.Line != 3 {
			t.Fatalf("tilde file got %+v", got)
		}
		if !filepath.IsAbs(got.Path) {
			t.Fatalf("expected absolute path for server file, got %q", got.Path)
		}

		res, err := ts.Client().Get(ts.URL + "/api/fs/preview?path=" + url.QueryEscape(got.Path) + "&meta=1")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("X-Preview-Kind") != "text" || string(body) != "# Walkthrough\n\nAll good.\n" {
			t.Fatalf("preview status=%d kind=%q body=%q", res.StatusCode, res.Header.Get("X-Preview-Kind"), body)
		}

		// file:// URI with percent-encoded spaces and CJK characters.
		u := (&url.URL{Scheme: "file", Path: spaceFile, Fragment: "L2"}).String()
		gotURI := resolve(url.Values{"target": {u}, "sessionId": {sess.ID}})
		if gotURI.Kind != "file" || gotURI.InProject || gotURI.Name != "report 2026 中文.txt" || gotURI.Line != 2 {
			t.Fatalf("file URI got %+v", gotURI)
		}

		// Exact filename containing :12 wins over stripping :12.
		gotColon := resolve(url.Values{"target": {colonFile}, "sessionId": {sess.ID}})
		if gotColon.Kind != "file" || gotColon.Name != "weird:12" || gotColon.Line != 0 {
			t.Fatalf("colon file got %+v", gotColon)
		}
	})

	t.Run("falls back to single-line target when wrapped continuation does not exist", func(t *testing.T) {
		got := resolve(url.Values{
			"target":    {"~/.gemini/antigravity-cli/brain/conv-1/walkthrough.mdNEXTLINE"},
			"fallback":  {"~/.gemini/antigravity-cli/brain/conv-1/walkthrough.md"},
			"sessionId": {sess.ID},
		})
		if got.Kind != "file" || got.Name != "walkthrough.md" {
			t.Fatalf("fallback got %+v", got)
		}
	})

	t.Run("allows directories inside the project and refuses directories outside it", func(t *testing.T) {
		inDir := resolve(url.Values{"target": {"internal/sub/"}, "sessionId": {sess.ID}})
		if inDir.Kind != "dir" || !inDir.InProject || inDir.ProjectID != project.ID || inDir.Path != "internal/sub" {
			t.Fatalf("in-project dir got %+v", inDir)
		}
		rootDir := resolve(url.Values{"target": {projRoot}, "sessionId": {sess.ID}})
		if rootDir.Kind != "dir" || !rootDir.InProject || rootDir.Path != "" {
			t.Fatalf("project root dir got %+v", rootDir)
		}

		for _, out := range []string{"~/.gemini", outsideDir, escapeDirLink} {
			got := resolve(url.Values{"target": {out}, "sessionId": {sess.ID}})
			if got.Kind != "outside_dir" || got.InProject || got.Path != "" {
				t.Errorf("outside dir %q got %+v, want outside_dir with empty path", out, got)
			}
		}

		// A symlink inside the project pointing at a file outside resolves as an
		// external file rather than pretending to be inside the project.
		escapedFile := resolve(url.Values{"target": {"escape-file.md"}, "sessionId": {sess.ID}})
		if escapedFile.Kind != "file" || escapedFile.InProject {
			t.Errorf("escaped file symlink got %+v, want external file", escapedFile)
		}
	})

	t.Run("refuses FIFOs, directories, relative paths and unauthenticated callers on /api/fs", func(t *testing.T) {
		gotFIFO := resolve(url.Values{"target": {fifoPath}, "sessionId": {sess.ID}})
		if gotFIFO.Kind != "not_regular" {
			t.Errorf("FIFO resolve got %+v, want not_regular", gotFIFO)
		}

		for _, endpoint := range []string{
			"/api/fs/preview?path=" + url.QueryEscape(fifoPath),
			"/api/fs/preview?path=" + url.QueryEscape(outsideDir),
			"/api/fs/preview?path=relative/file.txt",
		} {
			res, err := ts.Client().Get(ts.URL + endpoint)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("GET %s: status %d, want 400", endpoint, res.StatusCode)
			}
		}

		anon := &http.Client{}
		res, err := anon.Get(ts.URL + "/api/fs/preview?path=" + url.QueryEscape(geminiFile))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated /api/fs/preview: status %d, want 401", res.StatusCode)
		}
	})
}
