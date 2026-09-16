package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A development build must not talk itself into installing a release.
//
// `dev` is what a build without ldflags reports, and it is what runs on the
// machine this is written on. A comparison that treats an unparseable string as
// "behind" would offer an update on every check, and taking it would replace a
// working local build with a release nobody asked for.
func TestOnlyRealVersionsCompare(t *testing.T) {
	for _, tc := range []struct {
		current, candidate string
		want               bool
	}{
		{"v0.4.0", "v0.5.0", true},
		{"v0.4.0", "v0.4.1", true},
		{"0.4.0", "v0.4.0", false},
		{"v0.5.0", "v0.4.9", false},
		{"v0.9.0", "v0.10.0", true},     // not a string comparison
		{"v1.0.0", "v1.0.0-rc1", false}, // the same numbers
		{"v1.0.0-rc1", "v1.0.1", true},
		{"dev", "v9.9.9", false},
		{"", "v1.0.0", false},
		{"v1.0.0", "dev", false},
		{"v1.0.0", "", false},
		{"v1.0", "v1.0.1", false},
		{"v1.0.0", "vNaN.0.0", false},
	} {
		if got := IsNewer(tc.current, tc.candidate); got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.candidate, got, tc.want)
		}
	}
}

// The checksum is the only thing standing between a corrupt download and a
// binary that will not start. It has to be checked before anything is written.
func TestAnArchiveThatDoesNotMatchItsChecksumIsRefused(t *testing.T) {
	good := tarball(t, "#!/bin/sh\necho new\n")
	srv := releaseServer(t, "v9.0.0", good, sha256hex(append(good, 'x')))
	defer srv.Close()

	c := &Client{API: srv.URL}
	rel, err := c.Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := c.Fetch(context.Background(), rel); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Fetch accepted an archive whose sum does not match: %v", err)
	}
}

func TestAMatchingArchiveYieldsTheBinary(t *testing.T) {
	body := "#!/bin/sh\necho new\n"
	good := tarball(t, body)
	srv := releaseServer(t, "v9.0.0", good, sha256hex(good))
	defer srv.Close()

	c := &Client{API: srv.URL}
	rel, err := c.Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if !rel.Newer {
		t.Errorf("v9.0.0 is not reported as newer than v1.0.0")
	}
	bin, err := c.Fetch(context.Background(), rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(bin) != body {
		t.Errorf("got %q, want %q", bin, body)
	}
}

// A release built for another platform is an answer, not an error: somebody
// pressing "check" on a Mac should be told there is nothing for them rather
// than shown a failure.
func TestAReleaseWithoutThisPlatformsArchiveSaysSo(t *testing.T) {
	srv := releaseServerNamed(t, "v9.0.0", "vibepanel_9.0.0_plan9_386.tar.gz", nil, "")
	defer srv.Close()
	c := &Client{API: srv.URL}
	rel, err := c.Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Asset != "" {
		t.Errorf("found an asset for this platform in a release that has none: %q", rel.Asset)
	}
	if _, err := c.Fetch(context.Background(), rel); err == nil {
		t.Error("Fetch invented an archive that is not there")
	}
}

// A repository with no releases is the ordinary state of a new install.
func TestNoReleasesIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	rel, err := (&Client{API: srv.URL}).Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("a repository with no releases reported an error: %v", err)
	}
	if rel.Newer || rel.Version != "" {
		t.Errorf("invented a release: %+v", rel)
	}
}

// Install must leave the old binary somewhere, because the failure it cannot
// prevent is the new one not starting.
func TestInstallKeepsTheOldBinary(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "vibepanel")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// os.Executable() reports this test binary, so the swap is exercised
	// through the same code with a stand-in path.
	t.Setenv("VIBEPANEL_TEST_SELF", self)

	old, err := installAt(self, []byte("new"), nil)
	if err != nil {
		t.Fatalf("installAt: %v", err)
	}
	if got, _ := os.ReadFile(self); string(got) != "new" {
		t.Errorf("the binary in place is %q", got)
	}
	if got, _ := os.ReadFile(old); string(got) != "old" {
		t.Errorf("the kept-aside binary is %q, want the old one", got)
	}
	info, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the new binary is not executable: %v", info.Mode())
	}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tarball(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct {
		name, body string
	}{
		{"vibepanel_9.0.0_linux_amd64/README.md", "not the binary"},
		{"vibepanel_9.0.0_linux_amd64/vibepanel", body},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func releaseServer(t *testing.T, tag string, archive []byte, sum string) *httptest.Server {
	t.Helper()
	return releaseServerNamed(t, tag, AssetName(tag), archive, sum)
}

func releaseServerNamed(t *testing.T, tag, asset string, archive []byte, sum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example/releases","body":"notes",
			"assets":[{"name":%q,"browser_download_url":%q}]}`,
			tag, asset, srv.URL+"/dl/"+asset)
	})
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  ./%s\n", sum, AssetName(tag))
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	return srv
}

var _ = strings.TrimSpace

// A binary that does not start here is refused before the running one is
// touched.
//
// The checksum says the bytes are the published ones; it says nothing about
// whether they run on this machine. An archive built for the wrong
// architecture, or a binary directory on a noexec mount, passed every check
// and was found out by the restart -- by everybody, at once, with the panel
// gone. The one run of `--version` is what stands in front of that, and this
// test removes it: a swap that goes ahead over a refusal is a failure here.
func TestABinaryThatWillNotRunIsNotInstalled(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "vibepanel")
	if err := os.WriteFile(self, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("no")
	_, err := installAt(self, []byte("new"), func(string) error { return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("installAt = %v, want the verifier's refusal", err)
	}
	if got, _ := os.ReadFile(self); string(got) != "old" {
		t.Errorf("the binary in place is %q after a refusal; the old one should be untouched", got)
	}
	if _, err := os.Stat(self + ".old"); err == nil {
		t.Error("a .old was left behind by an install that did not happen")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".vibepanel-update-") {
			t.Errorf("the refused binary was left behind as %s", e.Name())
		}
	}
}

// Verify wants the version in the output, not a zero exit.
//
// `true` exits zero. So does a shell script that prints nothing, and so does
// a vibepanel of the wrong version if a release's archive was mislabelled --
// and each of those is a wrong file in the right place, which is the case a
// zero exit alone would wave through.
func TestVerifyWantsTheVersionNotJustAZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	right := write("right", "#!/bin/sh\necho 'vibepanel v9.0.0 (abc, built today)'\n")
	if err := Verify(right, "v9.0.0"); err != nil {
		t.Errorf("a binary that reports the version was refused: %v", err)
	}
	silent := write("silent", "#!/bin/sh\nexit 0\n")
	if err := Verify(silent, "v9.0.0"); !errors.Is(err, ErrWillNotRun) {
		t.Errorf("a zero exit with no version was accepted: %v", err)
	}
	other := write("other", "#!/bin/sh\necho 'vibepanel v8.0.0'\n")
	if err := Verify(other, "v9.0.0"); !errors.Is(err, ErrWillNotRun) {
		t.Errorf("a different version was accepted: %v", err)
	}
	crash := write("crash", "#!/bin/sh\necho 'cannot load shared library' >&2\nexit 127\n")
	err := Verify(crash, "v9.0.0")
	if !errors.Is(err, ErrWillNotRun) {
		t.Fatalf("a binary that fails to start was accepted: %v", err)
	}
	// What it said is in the error, because that is the only line the person
	// reading the settings page gets to see.
	if !strings.Contains(err.Error(), "shared library") {
		t.Errorf("err = %q, want it to carry what the binary said", err)
	}
	notThere := filepath.Join(dir, "missing")
	if err := Verify(notThere, "v9.0.0"); !errors.Is(err, ErrWillNotRun) {
		t.Errorf("a missing file was accepted: %v", err)
	}
}

// GitHub's rate limit is named as such, with when it resets.
//
// Sixty unauthenticated requests an hour per address, shared with everything
// else behind the same NAT. The refusal is a 403 with a body about API rate
// limits, and "GitHub answered 403 Forbidden" sent people looking for a
// permissions problem.
func TestARateLimitIsNamedAsOne(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(20*time.Minute).Unix()))
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &Client{API: srv.URL}
	_, err := c.Latest(context.Background(), "v1.0.0")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Latest = %v, want ErrRateLimited", err)
	}
	if Kind(err) != "rateLimited" {
		t.Errorf("Kind = %q, want rateLimited", Kind(err))
	}
	if !strings.Contains(err.Error(), "resets in") {
		t.Errorf("err = %q, want it to say when the limit resets", err)
	}
	// A 403 that is not the rate limit is still a 403.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	_, err = (&Client{API: srv2.URL}).Latest(context.Background(), "v1.0.0")
	if errors.Is(err, ErrRateLimited) {
		t.Errorf("a plain 403 was reported as the rate limit: %v", err)
	}
	if Kind(err) != "http" {
		t.Errorf("Kind(plain 403) = %q, want http", Kind(err))
	}
}

// The kinds a page can say something short about.
func TestTheKindOfAFailureIsReadable(t *testing.T) {
	// No route: a closed port on localhost is a refused connection, which is
	// what a box with no network reports for everything.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	_, err := (&Client{API: url}).Latest(context.Background(), "v1.0.0")
	if got := Kind(err); got != "offline" {
		t.Errorf("Kind(refused connection) = %q, want offline: %v", got, err)
	}
	_, err = (&Client{API: "http://no-such-host.invalid"}).Latest(context.Background(), "v1.0.0")
	if got := Kind(err); got != "offline" {
		t.Errorf("Kind(no such host) = %q, want offline: %v", got, err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = (&Client{API: slow.URL}).Latest(ctx, "v1.0.0")
	if got := Kind(err); got != "timeout" {
		t.Errorf("Kind(deadline) = %q, want timeout: %v", got, err)
	}
	if Kind(nil) != "" {
		t.Error("Kind(nil) is not empty")
	}
}

// A download reports how far it is, and the total when the server said one,
// so the page can draw a bar rather than a spinner over a seven-megabyte
// wait.
func TestADownloadReportsItsProgress(t *testing.T) {
	body := strings.Repeat("x", 600<<10)
	good := tarball(t, body)
	srv := releaseServer(t, "v9.0.0", good, sha256hex(good))
	defer srv.Close()

	c := &Client{API: srv.URL}
	rel, err := c.Latest(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var last, total int64
	calls := 0
	bin, err := c.Download(context.Background(), rel, func(done, tot int64) {
		calls++
		if done < last {
			t.Errorf("progress went backwards: %d after %d", done, last)
		}
		last, total = done, tot
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(bin) != body {
		t.Error("the binary that came out is not the one that went in")
	}
	if calls < 2 {
		t.Errorf("progress was reported %d times; the page needs a start and an end at least", calls)
	}
	if total != int64(len(good)) {
		t.Errorf("total = %d, want the archive's %d bytes", total, len(good))
	}
	if last != total {
		t.Errorf("the last report was %d of %d", last, total)
	}

	// And a body the server declares as larger than the ceiling is refused
	// before a byte of it is read.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(int64(maxArchive)+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer big.Close()
	if _, err := c.get(context.Background(), big.URL, maxArchive, func(int64, int64) {}); err == nil {
		t.Error("a download larger than the ceiling was started")
	}
}

// TestTheAssetNameMatchesWhatTheReleaseScriptBuilds pins the updater's idea of
// an archive name to the shell that actually produces one.
//
// The rest of this file is not able to catch that drift. The fake release
// server names its asset with AssetName and writes its SHA256SUMS line with
// AssetName, so the suite asserts the checker agrees with itself and stays
// green for any convention, including one nothing else in the repository uses.
// That is what happened: the panel asked github for
// vibepanel_1.2.0_linux_amd64.tar.gz for four releases while every archive
// ever published was named vibepanel_v1.2.0_linux_amd64.tar.gz, and the only
// symptom was "has no archive for this platform" on a release that had one.
//
// So this reads the script instead of a constant, and it reads the two lines
// that decide the name together: the `name=` assignment and the `tar -czf`
// that appends the suffix. A rename in either is a failure here.
func TestTheAssetNameMatchesWhatTheReleaseScriptBuilds(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)

	name := regexp.MustCompile(`(?m)^\s*name="([^"]+)"`).FindStringSubmatch(script)
	if name == nil {
		t.Fatal("scripts/build-release.sh no longer has a name=\"...\" line; this test cannot see what it builds")
	}
	tarLine := regexp.MustCompile(`(?m)^\s*tar -czf "dist/([^"]+)"`).FindStringSubmatch(script)
	if tarLine == nil {
		t.Fatal("scripts/build-release.sh no longer has a tar -czf \"dist/...\" line")
	}
	if tarLine[1] != "${name}.tar.gz" {
		t.Fatalf("the archive is no longer ${name}.tar.gz but %q; AssetName has to follow", tarLine[1])
	}

	// The script's own substitutions, with the values a v1.2.0 linux/amd64
	// build would have. VERSION is the tag verbatim, which is why it has a v.
	shell := strings.NewReplacer(
		"${VERSION}", "v1.2.0",
		"${os}", "linux",
		"${arch}", "amd64",
	)
	want := shell.Replace(name[1]) + ".tar.gz"
	if strings.Contains(want, "${") {
		t.Fatalf("the name template has a substitution this test does not know: %q", want)
	}

	got := AssetName("v1.2.0")
	got = strings.Replace(got, "_"+runtime.GOOS+"_"+runtime.GOARCH+".", "_linux_amd64.", 1)
	if got != want {
		t.Errorf("AssetName builds %q; scripts/build-release.sh publishes %q", got, want)
	}

	// And the same for a version handed over without the v, which is what a
	// development build stamped from git describe looks like.
	if bare := AssetName("1.2.0"); bare != AssetName("v1.2.0") {
		t.Errorf("AssetName(%q) = %q, AssetName(%q) = %q: one release, one archive", "1.2.0", bare, "v1.2.0", AssetName("v1.2.0"))
	}
}

// A binary in a directory it cannot write to says so before downloading
// anything.
//
// The system install puts it in /usr/local/bin owned by root and runs the
// panel as the user, so the swap cannot work. It used to find that out after
// fetching the archive, and reported the raw errno from a temp file nobody had
// heard of:
//
//	open /usr/local/bin/.vibepanel-update-2717682195: permission denied
//
// What this pins is the answer, not the technique. `installableAt` probes by
// writing, because the mode is not the answer -- a read-only mount, an ACL and
// a full disk all present as a writable directory and fail at the same call --
// but on an ordinary filesystem as an ordinary user the two agree, so
// replacing the probe with a mode check leaves this test green. Verified by
// doing it. The write probe stays because it is right, not because anything
// here would catch its removal.
func TestABinaryItCannotReplaceIsRefusedBeforeTheDownload(t *testing.T) {
	if os.Geteuid() == 0 {
		// root can write anywhere, so there is nothing to observe.
		t.Skip("running as root")
	}
	dir := t.TempDir()
	self := filepath.Join(dir, "vibepanel")
	if err := os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := installableAt(self); err != nil {
		t.Fatalf("a writable directory reported %v", err)
	}

	// The shape of a system install: the binary is there and the directory is
	// not writable by this account.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := installableAt(self)
	if err == nil {
		t.Fatal("a read-only directory reported that an update could be installed")
	}
	if !errors.Is(err, ErrNotWritable) {
		t.Errorf("err = %v, want it to wrap ErrNotWritable so the handler can name the command", err)
	}
	// And the message says where, because "permission denied" without a path
	// is what made the original report unactionable.
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("err = %q, want it to name %s", err, dir)
	}

	// The probe leaves nothing behind in the writable case.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installableAt(self); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".vibepanel-update-probe") {
			t.Errorf("the probe left %s behind", e.Name())
		}
	}
}
