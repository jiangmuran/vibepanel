package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// fakeGitHub is a release API with one release on it, and a switch for the
// archive so a test can hold a download open.
type fakeGitHub struct {
	srv     *httptest.Server
	tag     string
	archive []byte
	sum     string
	// hits counts questions about the latest release.
	hits atomic.Int32
	// hold, when non-nil, is waited on before the archive is served.
	hold chan struct{}
}

func newFakeGitHub(t *testing.T, tag, binary string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{tag: tag}
	f.archive = tarballWith(t, binary)
	sum := sha256.Sum256(f.archive)
	f.sum = hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	asset := selfupdate.AssetName(tag)
	mux.HandleFunc("/repos/"+selfupdate.Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example/releases/%s","body":"## What changed\n\n- a thing\n",
			"published_at":"2026-09-01T10:00:00Z",
			"assets":[{"name":%q,"browser_download_url":%q}]}`,
			tag, tag, asset, f.srv.URL+"/dl/"+asset)
	})
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  ./%s\n", f.sum, asset)
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) {
		if f.hold != nil {
			select {
			case <-f.hold:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(f.archive)
	})
	return f
}

func tarballWith(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{
		Name: "vibepanel_v99/vibepanel", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// updatable is a test server that can install: a running version that
// parses, a writable binary, and stand-ins for the two calls that would
// otherwise replace and restart the test binary.
func updatable(t *testing.T, gh *fakeGitHub) (*httptest.Server, *Server, *installLog) {
	t.Helper()
	ts, srv := newTestServer(t)
	was := version.Version
	version.Version = "v1.0.0"
	t.Cleanup(func() { version.Version = was })
	srv.Updater.API = gh.srv.URL
	srv.installable = func() error { return nil }
	log := &installLog{}
	srv.install = func(bin []byte, ver string) (string, error) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.bin, log.version = string(bin), ver
		if log.refuse != nil {
			return "", log.refuse
		}
		return "/opt/vibepanel.old", nil
	}
	srv.restartCmd = func() (*exec.Cmd, error) {
		return nil, errors.New("not running under systemd in this test")
	}
	return ts, srv, log
}

type installLog struct {
	mu      sync.Mutex
	bin     string
	version string
	refuse  error
}

func status(t *testing.T, ts *httptest.Server) updateStatus {
	t.Helper()
	code, body := send(t, ts, http.MethodGet, "/api/update/status", "")
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var out updateStatus
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("status: %v: %s", err, body)
	}
	return out
}

// waitJob polls until the job leaves the active stages, and returns it.
func waitJob(t *testing.T, ts *httptest.Server) *updateJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st := status(t, ts)
		if st.Job != nil && !st.Job.active() {
			return st.Job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job never finished")
	return nil
}

// An update is a job the page watches, not a request the page waits on.
//
// The first version did the download on the request's context. A phone that
// put the tab to sleep during it cancelled the download, and a page that was
// reloaded had no way to learn what had happened. The job now runs on its own
// and both GETs report it, stage by stage, with the bytes so far.
func TestAnUpdateIsAJobThePageCanWatch(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", strings.Repeat("new binary ", 50_000))
	ts, _, log := updatable(t, gh)

	code, body := send(t, ts, http.MethodPost, "/api/update", `{"expected":"v99.0.0"}`)
	if code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", code, body)
	}
	var started struct {
		Job *updateJob `json:"job"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || started.Job == nil {
		t.Fatalf("apply did not answer with the job: %s", body)
	}
	if started.Job.Version != "v99.0.0" || !started.Job.active() {
		t.Errorf("the job answered with is %+v", started.Job)
	}

	job := waitJob(t, ts)
	if job.Stage != "installed" {
		t.Fatalf("job = %+v, want installed (this test's restart stand-in refuses)", job)
	}
	if job.Restarting || !strings.Contains(job.RestartWhy, "systemd") {
		t.Errorf("a panel that cannot restart itself has to say so: %+v", job)
	}
	if job.Previous == "" {
		t.Error("the job does not say where the old binary went")
	}
	if job.Total <= 0 || job.Done != job.Total {
		t.Errorf("progress ended at %d of %d", job.Done, job.Total)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.version != "v99.0.0" {
		t.Errorf("installed version %q, want v99.0.0 -- Verify would have checked for the wrong one", log.version)
	}
	if !strings.HasPrefix(log.bin, "new binary ") {
		t.Errorf("what was installed is not the archive's binary: %.40q", log.bin)
	}

	// Recorded, with the version, for the activity log.
	code, body = send(t, ts, http.MethodGet, "/api/settings/audit", "")
	if code != http.StatusOK || !strings.Contains(body, `"update.installed"`) || !strings.Contains(body, "v99.0.0") {
		t.Errorf("the install is not in the audit log: %d %s", code, body)
	}
}

// A second press while the first is running is the same request.
func TestASecondPressDuringAnUpdateIsRefused(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", "new")
	gh.hold = make(chan struct{})
	ts, _, _ := updatable(t, gh)

	code, body := send(t, ts, http.MethodPost, "/api/update", "")
	if code != http.StatusAccepted {
		t.Fatalf("first apply: %d %s", code, body)
	}
	code, body = send(t, ts, http.MethodPost, "/api/update", "")
	if code != http.StatusConflict || !strings.Contains(body, `"reason":"busy"`) {
		t.Errorf("second apply: %d %s, want 409 busy", code, body)
	}
	if !strings.Contains(body, `"stage":"downloading"`) {
		t.Errorf("the refusal does not carry the job the page should be watching: %s", body)
	}
	// And the status says so too, for a tab that did not press anything.
	if st := status(t, ts); st.Job == nil || st.Job.Stage != "downloading" {
		t.Errorf("status while downloading = %+v", st.Job)
	}
	close(gh.hold)
	if job := waitJob(t, ts); job.Stage != "installed" {
		t.Errorf("after the hold: %+v", job)
	}
}

// What was confirmed is what gets installed.
//
// The page shows a version and a confirmation names it. If a newer release
// lands between the check and the press, installing *that* is installing
// something nobody read the notes for. The request carries the version it
// showed; a mismatch is a 409 the page answers by checking again.
func TestTheVersionConfirmedIsTheVersionInstalled(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", "new")
	ts, srv, log := updatable(t, gh)

	code, body := send(t, ts, http.MethodPost, "/api/update", `{"expected":"v98.0.0"}`)
	if code != http.StatusConflict || !strings.Contains(body, `"reason":"changed"`) {
		t.Fatalf("a stale expected version: %d %s, want 409 changed", code, body)
	}
	if !strings.Contains(body, "v99.0.0") {
		t.Errorf("the refusal does not name the release that would be installed: %s", body)
	}
	if srv.currentJob() != nil {
		t.Error("a job was started for a version nobody confirmed")
	}
	log.mu.Lock()
	installed := log.version
	log.mu.Unlock()
	if installed != "" {
		t.Errorf("installed %s anyway", installed)
	}

	// A version the request could name but that is older is refused the same
	// way it always was: nothing newer than what runs.
	was := version.Version
	version.Version = "v99.0.0"
	defer func() { version.Version = was }()
	code, body = send(t, ts, http.MethodPost, "/api/update", `{"expected":"v99.0.0"}`)
	if code != http.StatusConflict || !strings.Contains(body, "nothing newer") {
		t.Errorf("up to date: %d %s, want 409 nothing newer", code, body)
	}
}

// Each way a job can fail is named, because the page says a different
// sentence for each and "update failed" alone sends people to the wrong place.
func TestAFailedUpdateSaysWhichStepFailed(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		gh := newFakeGitHub(t, "v99.0.0", "new")
		gh.sum = strings.Repeat("0", 64)
		ts, _, log := updatable(t, gh)
		if code, body := send(t, ts, http.MethodPost, "/api/update", ""); code != http.StatusAccepted {
			t.Fatalf("apply: %d %s", code, body)
		}
		job := waitJob(t, ts)
		if job.Stage != "failed" || job.Reason != "checksum" {
			t.Errorf("job = %+v, want failed/checksum", job)
		}
		log.mu.Lock()
		defer log.mu.Unlock()
		if log.bin != "" {
			t.Error("a binary that did not match its checksum was handed to the installer")
		}
	})
	t.Run("verify", func(t *testing.T) {
		gh := newFakeGitHub(t, "v99.0.0", "new")
		ts, _, log := updatable(t, gh)
		log.refuse = fmt.Errorf("%w: exec format error", selfupdate.ErrWillNotRun)
		if code, body := send(t, ts, http.MethodPost, "/api/update", ""); code != http.StatusAccepted {
			t.Fatalf("apply: %d %s", code, body)
		}
		job := waitJob(t, ts)
		if job.Stage != "failed" || job.Reason != "verify" || !strings.Contains(job.Error, "exec format") {
			t.Errorf("job = %+v, want failed/verify with the binary's complaint", job)
		}
	})
	t.Run("install", func(t *testing.T) {
		gh := newFakeGitHub(t, "v99.0.0", "new")
		ts, _, log := updatable(t, gh)
		log.refuse = errors.New("rename: read-only file system")
		if code, body := send(t, ts, http.MethodPost, "/api/update", ""); code != http.StatusAccepted {
			t.Fatalf("apply: %d %s", code, body)
		}
		if job := waitJob(t, ts); job.Stage != "failed" || job.Reason != "install" {
			t.Errorf("job = %+v, want failed/install", job)
		}
	})
	t.Run("network", func(t *testing.T) {
		gh := newFakeGitHub(t, "v99.0.0", "new")
		ts, srv, _ := updatable(t, gh)
		// The release is announced and then the archive is gone: a 404 on
		// the download, not a failure to reach GitHub.
		gh.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/dl/") {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"tag_name":"v99.0.0","assets":[{"name":%q,"browser_download_url":%q}]}`,
				selfupdate.AssetName("v99.0.0"), gh.srv.URL+"/dl/x")
		})
		srv.updates = updateState{}
		if code, body := send(t, ts, http.MethodPost, "/api/update", ""); code != http.StatusAccepted {
			t.Fatalf("apply: %d %s", code, body)
		}
		if job := waitJob(t, ts); job.Stage != "failed" || job.Reason != "network" {
			t.Errorf("job = %+v, want failed/network", job)
		}
	})
	// A failed job does not stand in the way of the next press.
	gh := newFakeGitHub(t, "v99.0.0", "new")
	ts, _, log := updatable(t, gh)
	log.refuse = errors.New("no")
	send(t, ts, http.MethodPost, "/api/update", "")
	waitJob(t, ts)
	log.mu.Lock()
	log.refuse = nil
	log.mu.Unlock()
	if code, body := send(t, ts, http.MethodPost, "/api/update", ""); code != http.StatusAccepted {
		t.Errorf("after a failure: %d %s, want the next press to run", code, body)
	}
	waitJob(t, ts)
}

// The panel asks on its own only because a page asked and the answer was
// old, and never while the setting is off.
//
// Nothing here has a ticker, and this is the test that would notice one: the
// count of questions to GitHub is the whole assertion.
func TestTheStatusEndpointAsksOnItsOwnOnlyWhenTheAnswerIsOld(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", "new")
	ts, srv, _ := updatable(t, gh)

	// Nothing checked yet: the first status starts a check in the background
	// and answers at once.
	st := status(t, ts)
	if st.CheckedAt != 0 && st.Version != "" && !st.Checking {
		t.Errorf("the first status waited for GitHub: %+v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for st.Version == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		st = status(t, ts)
	}
	if st.Version != "v99.0.0" || !st.Newer || st.CheckedAt == 0 {
		t.Fatalf("after the background check: %+v", st)
	}
	if st.PublishedAt == "" || st.URL == "" || !strings.Contains(st.Notes, "What changed") {
		t.Errorf("the release's date, page and notes are not all there: %+v", st)
	}
	if !st.AutoCheck {
		t.Error("autoCheck is off by default; it is meant to be on until somebody says otherwise")
	}
	if n := gh.hits.Load(); n != 1 {
		t.Fatalf("GitHub was asked %d times for one status", n)
	}

	// Fresh: asked again and again, GitHub hears nothing.
	for range 5 {
		status(t, ts)
	}
	if n := gh.hits.Load(); n != 1 {
		t.Errorf("a fresh answer was refreshed: %d hits", n)
	}

	// Old: one more question, and only one however many pages poll.
	srv.updates.mu.Lock()
	srv.updates.last.At = time.Now().Add(-autoCheckEvery - time.Minute).Unix()
	srv.updates.mu.Unlock()
	for range 5 {
		status(t, ts)
	}
	deadline = time.Now().Add(5 * time.Second)
	for gh.hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Let the in-flight check land before counting.
	time.Sleep(50 * time.Millisecond)
	if n := gh.hits.Load(); n != 2 {
		t.Errorf("an old answer polled five times cost %d questions, want exactly 1 more", n-1)
	}

	// Off: old again, and nobody asks.
	if code, body := send(t, ts, http.MethodPut, "/api/update/settings", `{"autoCheck":false}`); code != http.StatusOK {
		t.Fatalf("turning it off: %d %s", code, body)
	}
	srv.updates.mu.Lock()
	srv.updates.last.At = time.Now().Add(-24 * time.Hour).Unix()
	srv.updates.mu.Unlock()
	st = status(t, ts)
	time.Sleep(50 * time.Millisecond)
	if st.AutoCheck {
		t.Error("the setting did not turn off")
	}
	if n := gh.hits.Load(); n != 2 {
		t.Errorf("with the setting off, GitHub was still asked: %d hits", n)
	}
	// The old answer is still shown, dated, rather than thrown away.
	if st.Version != "v99.0.0" || st.CheckedAt == 0 {
		t.Errorf("the last answer went missing when the setting went off: %+v", st)
	}
	// And the button still works with it off: that is what the setting
	// leaves you with, not nothing.
	if code, body := send(t, ts, http.MethodGet, "/api/update", ""); code != http.StatusOK {
		t.Errorf("the button with auto-check off: %d %s", code, body)
	}
	if n := gh.hits.Load(); n != 3 {
		t.Errorf("the button did not ask: %d hits", n)
	}
}

// The last answer survives the restart that the update causes.
//
// Without this, every panel came back from its own upgrade knowing nothing,
// asked again, and a page open across the restart saw the badge go away and
// come back. With it, the record written before the restart is read after
// it -- and says "not newer", because the comparison is against what runs
// now rather than what ran when the answer was recorded.
func TestTheLastCheckOutlivesTheProcess(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", "new")
	ts, srv, _ := updatable(t, gh)
	if code, body := send(t, ts, http.MethodGet, "/api/update", ""); code != http.StatusOK {
		t.Fatalf("check: %d %s", code, body)
	}
	at := status(t, ts).CheckedAt
	if at == 0 {
		t.Fatal("the check was not recorded")
	}

	// A new process: the in-memory state is gone and the database is not.
	srv.updates = updateState{}
	st := status(t, ts)
	if st.CheckedAt != at || st.Version != "v99.0.0" {
		t.Errorf("after a restart the record is %+v, want the one written before it", st)
	}
	if n := gh.hits.Load(); n != 1 {
		t.Errorf("a restart cost a question to GitHub: %d hits", n)
	}

	// And after the upgrade it announced, the same record says "not newer".
	version.Version = "v99.0.0"
	srv.updates = updateState{}
	st = status(t, ts)
	if st.Newer {
		t.Errorf("the record still says v99.0.0 is newer than the v99.0.0 that is running")
	}
	if st.Version != "v99.0.0" {
		t.Errorf("the record was dropped rather than re-read: %+v", st)
	}
}

// Two checks at once are one question.
func TestConcurrentChecksAreOneQuestion(t *testing.T) {
	gh := newFakeGitHub(t, "v99.0.0", "new")
	ts, _, _ := updatable(t, gh)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(t, ts, http.MethodGet, "/api/update", "")
		}()
	}
	wg.Wait()
	// Each request either asked or waited on the one that did. Fewer than
	// eight is the point; exactly one is what single-flight promises when
	// they overlap, but the test cannot make them overlap, so "fewer than
	// all of them" is what can be asserted without a flake.
	if n := gh.hits.Load(); n >= 8 {
		t.Errorf("eight simultaneous checks were %d questions to GitHub", n)
	}
}
