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

	old, err := installAt(self, []byte("new"))
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
