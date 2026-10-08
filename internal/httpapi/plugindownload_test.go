package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A declared download: fetched through the guard with redirects followed,
// verified against the manifest's hash, unpacked with every entry kept
// under its directory, and the process waits for it. The server is this
// test's, reached through the fetcher's test hooks.
// downloadManifest names the test server's port: the fetcher's test hooks
// resolve example.com to loopback and the dial goes to the checked address
// at the URL's port.
func downloadManifest(sha string, size int, optional bool, port string) string {
	return `{
  "plugin": 1, "id": "dlp", "name": {"en": "DL"}, "version": "1.0.0",
  "process": {"command": ["sh", "cmd.sh"]},
  "downloads": [{"name": "model", "optional": ` + map[bool]string{true: "true", false: "false"}[optional] + `,
                 "label": {"en": "A model"}, "url": "https://example.com:` + port + `/release/model.tar.gz",
                 "sha256": "` + sha + `", "size": ` + itoa(size) + `, "unpack": "tar.gz"}]
}`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func waitDownload(t *testing.T, ts *httptest.Server, want func(row map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/dlp/downloads", "")
		var rows []map[string]any
		_ = json.Unmarshal(body, &rows)
		if len(rows) == 1 {
			last = rows[0]
			if want(last) {
				return last
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the download did not reach the wanted state; last %v", last)
	return nil
}

func TestADeclaredDownloadIsFollowedVerifiedUnpackedAndWaitedFor(t *testing.T) {
	archive := tarGz(map[string]string{"bin/run": "#!/bin/sh\necho hi\n", "data/weights.bin": strings.Repeat("w", 3000)})
	sha := sha256Hex(archive)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release/model.tar.gz":
			// What a release host does: a redirect to where the bytes are.
			http.Redirect(w, r, "https://objects.example.com:"+portOf(r.Host)+"/blob/1", http.StatusFound)
		case "/blob/1":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	ts, srv := newTestServer(t)
	srv.pdl.fetcher = testFetcher(up)
	srv.serviceCtx = t.Context()

	// Required: fetched at enable, and the process waits for it.
	status, body := postZip(t, ts, pluginZip(t, downloadManifest(sha, len(archive), false, portOf(strings.TrimPrefix(up.URL, "https://"))), map[string]string{"cmd.sh": "echo assets=$VIBEPANEL_PLUGIN_ASSETS; sleep 60"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	_, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/dlp", "")
	if !strings.Contains(string(body), "Needs A model") || !strings.Contains(string(body), "example.com") {
		t.Errorf("the screen does not say what will be downloaded: %s", body)
	}
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/dlp/install", `{"caps":[]}`)
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/dlp/enable", `{}`)
	row := waitDownload(t, ts, func(r map[string]any) bool { return r["ready"] == true })
	if row["host"] != "example.com" || row["status"] != "" {
		t.Errorf("row: %v", row)
	}
	dir := filepath.Join(srv.pluginAssetsDir("dlp"), "model")
	if info, err := os.Stat(filepath.Join(dir, "bin", "run")); err != nil || info.Mode()&0o100 == 0 {
		t.Errorf("bin/run: %v %v (the exec bit must survive)", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "weights.bin")); err != nil {
		t.Error(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(srv.pluginAssetsDir("dlp"), ".tmp")); len(entries) != 0 {
		t.Errorf("leftovers in .tmp: %v", entries)
	}
	srv.ensurePluginProcesses(t.Context())
	waitFor(t, "the process to start once the download is there", 5000, func() bool {
		_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/dlp/process", "")
		return strings.Contains(string(body), `"running":true`) && strings.Contains(string(body), "assets="+srv.pluginAssetsDir("dlp"))
	})

	// Removed: the directory goes, the process waits again.
	if status, _ := doJSON(t, ts, http.MethodDelete, "/api/settings/plugins/dlp/downloads/model", ""); status != http.StatusOK {
		t.Fatalf("remove: %d", status)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the directory is still there after removing")
	}
	// (pluginsChanged after the removal fetched it again, as a required download should be.)
	waitDownload(t, ts, func(r map[string]any) bool { return r["ready"] == true })
}

func TestADownloadWithTheWrongHashKeepsNothing(t *testing.T) {
	archive := tarGz(map[string]string{"a": "b"})
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
	defer up.Close()
	ts, srv := newTestServer(t)
	srv.pdl.fetcher = testFetcher(up)
	srv.serviceCtx = t.Context()
	wrong := strings.Repeat("ab", 32)
	postZip(t, ts, pluginZip(t, downloadManifest(wrong, len(archive), true, portOf(strings.TrimPrefix(up.URL, "https://"))), map[string]string{"cmd.sh": "sleep 60"}))
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/dlp/install", `{"caps":[]}`)
	// Optional: nothing happens at enable.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/dlp/enable", `{}`)
	time.Sleep(100 * time.Millisecond)
	_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/dlp/downloads", "")
	if !strings.Contains(string(body), `"status":""`) || strings.Contains(string(body), `"ready":true`) {
		t.Fatalf("an optional download started by itself: %s", body)
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/dlp/downloads/model", ""); status != http.StatusAccepted {
		t.Fatalf("start: %d %s", status, body)
	}
	row := waitDownload(t, ts, func(r map[string]any) bool { return r["status"] == "failed" })
	if !strings.Contains(row["error"].(string), "sha256 mismatch") {
		t.Errorf("error: %v", row["error"])
	}
	if _, err := os.Stat(filepath.Join(srv.pluginAssetsDir("dlp"), "model")); err == nil {
		t.Error("something was kept after a hash mismatch")
	}
	if entries, _ := os.ReadDir(filepath.Join(srv.pluginAssetsDir("dlp"), ".tmp")); len(entries) != 0 {
		t.Errorf("the temp file was not removed: %v", entries)
	}
	// The process of an optional download runs without it.
	srv.ensurePluginProcesses(t.Context())
	waitFor(t, "the process", 5000, func() bool {
		_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/dlp/process", "")
		return strings.Contains(string(body), `"running":true`)
	})
}

func TestAnArchiveThatClimbsOutIsRefused(t *testing.T) {
	for _, bad := range []map[string]string{{"../escape": "x"}, {"a/../../escape": "x"}, {"a/..": "x"}} {
		archive := tarGz(bad)
		dst := t.TempDir()
		err := unpackInto(writeTemp(t, archive), "tar.gz", dst, 1<<20)
		if err == nil {
			t.Errorf("%v: accepted", bad)
		}
		if _, serr := os.Stat(filepath.Join(filepath.Dir(dst), "escape")); serr == nil {
			t.Errorf("%v: wrote outside the directory", bad)
		}
	}
	// An absolute path is kept under the directory; a bomb is capped.
	if err := unpackInto(writeTemp(t, tarGz(map[string]string{"/abs/x": "y"})), "tar.gz", t.TempDir(), 1<<20); err != nil {
		t.Errorf("an absolute name should land under the directory: %v", err)
	}
	if err := unpackInto(writeTemp(t, tarGz(map[string]string{"big": strings.Repeat("x", 5000)})), "tar.gz", t.TempDir(), 1000); err == nil {
		t.Error("over the cap: accepted")
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tgz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
