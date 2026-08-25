package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// Downloading a FIFO must not hang the server.
//
// os.Open on a FIFO blocks until somebody opens the write end. Nobody does, so
// the handler never returns: one goroutine and one descriptor are gone for the
// life of the process, and graceful shutdown, which waits for requests in
// flight, never finishes either. `mkfifo` needs no privileges and shell
// scripts make them routinely, so this is something a project directory
// contains rather than something an attacker has to arrange.
//
// The deadline is what makes this test useful rather than dangerous: without
// it, a regression does not fail the suite, it *wedges* it — the first version
// of this probe held the whole package at httptest.Server.Close() for the full
// five-minute timeout, because Close waits for the same request that cannot
// finish.
func TestDownloadRefusesNonRegularFiles(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"f"}`)

	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		c := ts.Client()
		c.Timeout = 5 * time.Second
		res, err := c.Get(ts.URL + "/api/projects/" + project.ID + "/download?path=pipe")
		if err != nil {
			done <- result{err: err}
			return
		}
		res.Body.Close()
		done <- result{status: res.StatusCode}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("the request never completed, so the handler blocked on the FIFO: %v", got.err)
		}
		if got.status != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", got.status, http.StatusBadRequest)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no response at all: the handler is blocked on the FIFO and shutdown will hang too")
	}

	// The ordinary case still works, so the guard has not simply refused
	// everything.
	res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/download?path=ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("regular file: status = %d, want 200", res.StatusCode)
	}
}
