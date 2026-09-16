// Package selfupdate replaces this binary with a newer release.
//
// This is the most dangerous thing in the product and the design says so out
// loud rather than reading as routine. What it does, in order: ask GitHub what
// the latest release of one hard-coded repository is; if it is newer than what
// is running, download that release's archive for this exact GOOS/GOARCH and
// its SHA256SUMS file; check the archive against the sums; unpack the binary;
// run it once to see that it is a vibepanel that starts on this machine; swap
// it into place; and then ask the service manager to restart the unit.
//
// What the checksum does and does not buy. It is fetched from the same release
// as the archive, so it detects a corrupt or truncated download and nothing
// else: whoever can publish a release can publish sums to match. That is the
// honest boundary, and it is the same trust anyone gets from `curl | tar`. What
// makes it defensible is that the repository is compiled in rather than
// configurable -- an update cannot be pointed somewhere else by a setting, a
// query parameter, or a value from the database.
//
// Nothing here runs on a timer. The panel does check on its own, but the way
// internal/git/warm.go refreshes a board: because somebody has a page open and
// the last answer is old, at most once every few hours, and never while nobody
// is looking. The scheduling lives in internal/httpapi; this package only ever
// does what it is asked, once, when it is asked.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where updates come from, and it is not configurable on purpose.
//
// A settable update source is a way to make a panel install a binary of
// somebody else's choosing with one database write. This is checked into the
// program that it updates.
const Repo = "jiangmuran/vibepanel"

// maxArchive bounds what will be pulled down.
//
// The release archives are a few megabytes; 128 MiB is far above anything real
// and far below anything that fills a disk while somebody watches a spinner. A
// download with no ceiling is a denial of service that the person being denied
// started themselves.
const maxArchive = 128 << 20

// Release is what the panel found upstream.
type Release struct {
	// Version is the tag, e.g. "v0.5.0".
	Version string `json:"version"`
	// Newer says whether it is ahead of what is running. False also covers
	// "cannot tell", which is why Current and Notes exist alongside it.
	Newer bool `json:"newer"`
	// Current is the running build's version, so the two can be shown together
	// rather than the answer alone.
	Current string `json:"current"`
	// URL is the release page, for reading before agreeing to anything.
	URL string `json:"url"`
	// Notes is the release body, trimmed. Somebody about to replace the binary
	// that is holding their sessions deserves to see what changed.
	Notes string `json:"notes"`
	// PublishedAt is when the release was cut, RFC 3339, empty when GitHub did
	// not say. A version number alone does not tell somebody whether they are
	// a week or a year behind.
	PublishedAt string `json:"publishedAt,omitempty"`
	// Asset is the archive for this GOOS/GOARCH, empty when the release does
	// not carry one -- which is not an error, it is an answer.
	Asset string `json:"asset"`
}

type ghRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Client fetches releases. The zero value works.
type Client struct {
	// HTTP is the client used for every request. Nil means a bounded default;
	// see defaultHTTP for what "bounded" has to mean here.
	HTTP *http.Client
	// API overrides the GitHub endpoint, for tests only. Production never sets
	// it, which is what keeps Repo meaningful: asset and checksum URLs come out
	// of the release GitHub returned, so a test server's release points at the
	// test server and nothing has to be rewritten afterwards.
	API string
}

// defaultHTTP is the client used when none is given.
//
// No overall Timeout, on purpose. The first version had `Timeout: 60s`, which
// bounds the whole exchange including the body -- so a seven-megabyte archive
// on a link slower than about a megabit was cut off mid-download and reported
// as a network failure, on exactly the machines a self-hosted panel tends to
// live on. The header timeout is what a hung server looks like, and the body
// is bounded by the caller's context, which the handler sets per call: half a
// minute for a question, minutes for a download.
var defaultHTTP = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	t.TLSHandshakeTimeout = 15 * time.Second
	return &http.Client{Transport: t}
}()

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) api() string {
	if c.API != "" {
		return c.API
	}
	return "https://api.github.com"
}

// AssetName is the archive this build would install.
//
// The `v` is put back rather than left alone, because the two strings this has
// to sit between disagree about it. A release tag is `v1.2.0` and that is what
// the API hands back in `tag_name`, but a build stamped from `git describe` on
// a branch, or `--version` typed by a person, is as likely to be `1.2.0`.
// Normalising to exactly one leading `v` is what `scripts/build-release.sh`
// produces, since it interpolates the tag verbatim.
//
// It stripped the `v` instead until v1.2.0, so the panel looked for
// `vibepanel_1.2.0_linux_amd64.tar.gz`, found nothing, and reported the
// release as having no archive for the platform it was running on -- for every
// platform, every release. The test suite named its fixtures with this same
// function and so agreed with the bug; TestTheAssetNameMatchesWhatTheRelease
// ScriptBuilds is what pins it to the script instead.
func AssetName(version string) string {
	return fmt.Sprintf("vibepanel_v%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// Platform is the GOOS/GOARCH pair an archive is looked up by, for the page
// that has to say which platform a release is missing one for.
func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// ErrRateLimited means GitHub refused the question, not the answer.
//
// The unauthenticated release API allows sixty requests an hour per address.
// A panel behind a shared NAT -- an office, a university, a cloud provider's
// egress -- can find that budget already spent by somebody else, and the
// resulting 403 read as "GitHub answered 403 Forbidden", which sends people
// looking for a permissions problem that does not exist.
var ErrRateLimited = errors.New("selfupdate: GitHub's rate limit for this address is used up")

// Latest asks what the newest release is.
func (c *Client) Latest(ctx context.Context, current string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.api()+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Named, so that whoever reads GitHub's logs for this repository can see
	// what is asking. The version is the only thing about the panel in it.
	req.Header.Set("User-Agent", "vibepanel/"+strings.TrimSpace(current)+" (+https://github.com/"+Repo+")")
	res, err := c.http().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("selfupdate: asking GitHub: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		// A repository with no releases yet. Not a failure, and saying
		// "not found" to somebody who pressed "check for updates" reads as one.
		return Release{Current: current}, nil
	case (res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests) &&
		res.Header.Get("X-RateLimit-Remaining") == "0":
		return Release{}, fmt.Errorf("%w%s", ErrRateLimited, resetIn(res.Header.Get("X-RateLimit-Reset")))
	case res.StatusCode != http.StatusOK:
		return Release{}, &StatusError{URL: "GitHub", Status: res.Status}
	}
	var gh ghRelease
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&gh); err != nil {
		return Release{}, fmt.Errorf("selfupdate: reading the release: %w", err)
	}

	out := Release{
		Version: gh.TagName, Current: current, URL: gh.HTMLURL,
		Notes: trim(gh.Body, 4000), PublishedAt: gh.PublishedAt,
	}
	want := AssetName(gh.TagName)
	for _, a := range gh.Assets {
		if a.Name == want {
			out.Asset = a.URL
			break
		}
	}
	out.Newer = IsNewer(current, gh.TagName)
	return out, nil
}

// resetIn turns GitHub's X-RateLimit-Reset (unix seconds) into "; try again in
// N minutes", or nothing when the header is missing or already past.
func resetIn(header string) string {
	at, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return ""
	}
	left := time.Until(time.Unix(at, 0)).Round(time.Minute)
	if left <= 0 {
		return ""
	}
	return fmt.Sprintf("; it resets in %s", left)
}

// Kind classifies why a check or a download did not get an answer, for a page
// that has to say something shorter than the error.
//
//   - "rateLimited": GitHub refused the question, see ErrRateLimited.
//   - "timeout": the other side did not answer in time.
//   - "offline": no route at all -- DNS, a refused connection, no network.
//   - "http": GitHub answered, with something other than the release.
//   - "": anything else, or nil.
//
// The error itself still travels alongside, as the detail. This exists because
// the message underneath these is a Go net error, and
// `Get "https://api.github.com/...": dial tcp: lookup api.github.com: no such
// host` is not what a person needs to read to learn that the box is offline.
func Kind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRateLimited):
		return "rateLimited"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "offline"
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "offline"
	}
	var st *StatusError
	if errors.As(err, &st) {
		return "http"
	}
	return ""
}

// StatusError is an answer that was not the one asked for: a 5xx from GitHub,
// a 404 for an asset the release page listed. Its own type so Kind can tell
// "GitHub is having a bad day" from "there is no network".
type StatusError struct {
	URL    string
	Status string
}

func (e *StatusError) Error() string { return "selfupdate: " + e.URL + " answered " + e.Status }

// IsNewer compares two version strings.
//
// Deliberately small: tags here are vMAJOR.MINOR.PATCH and anything else --
// "dev", a hash, an empty string from a build without ldflags -- is not a
// version and cannot be ahead of or behind one. Answering "yes, update" for a
// string nobody can parse is how a development build talks itself into
// overwriting itself with a release.
func IsNewer(current, candidate string) bool {
	c, ok := parse(current)
	if !ok {
		return false
	}
	n, ok := parse(candidate)
	if !ok {
		return false
	}
	for i := range c {
		if n[i] != c[i] {
			return n[i] > c[i]
		}
	}
	return false
}

// IsVersion reports whether a string is something IsNewer can compare, so a
// page can tell "up to date" from "a build with nothing to compare against"
// without carrying its own copy of the rule.
func IsVersion(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return out, false
	}
	// A tag may carry a suffix -- v1.2.3-rc1 -- and the numbers before it are
	// still the comparison. Everything after the first dash is dropped.
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ErrChecksum means the archive is not the one the release says it is.
var ErrChecksum = errors.New("selfupdate: the archive does not match the published checksum")

// Progress is told how a download is going: bytes so far, and the total when
// the server said one, else -1. Called from the download's goroutine, so it
// has to be quick and may not block.
type Progress func(done, total int64)

// Fetch downloads the release archive, checks it, and returns the binary.
func (c *Client) Fetch(ctx context.Context, rel Release) ([]byte, error) {
	return c.Download(ctx, rel, nil)
}

// Download is Fetch with somebody watching.
//
// The sums file is fetched first and on purpose: downloading a hundred
// megabytes and only then discovering there is nothing to check it against is
// the wrong order to find that out in.
func (c *Client) Download(ctx context.Context, rel Release, progress Progress) ([]byte, error) {
	if rel.Asset == "" {
		return nil, fmt.Errorf("selfupdate: release %s has no %s", rel.Version, AssetName(rel.Version))
	}
	sums, err := c.get(ctx, sumsURL(rel.Asset), 1<<20, nil)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: SHA256SUMS: %w", err)
	}
	want, ok := sumFor(string(sums), AssetName(rel.Version))
	if !ok {
		return nil, fmt.Errorf("selfupdate: SHA256SUMS does not list %s", AssetName(rel.Version))
	}

	archive, err := c.get(ctx, rel.Asset, maxArchive, progress)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: downloading: %w", err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, ErrChecksum
	}
	return binaryFromArchive(archive)
}

// sumsURL is the checksums file beside an asset in the same release.
func sumsURL(asset string) string {
	i := strings.LastIndexByte(asset, '/')
	if i < 0 {
		return asset
	}
	return asset[:i+1] + "SHA256SUMS"
}

func sumFor(sums, name string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		// sha256sum writes "./name" when it was run over a glob in a directory,
		// which is exactly how the release script produces this file.
		if strings.TrimPrefix(f[1], "./") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

func (c *Client) get(ctx context.Context, url string, limit int64, progress Progress) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &StatusError{URL: url, Status: res.Status}
	}
	if progress == nil {
		return io.ReadAll(io.LimitReader(res.Body, limit))
	}
	// Reported by the chunk rather than by a wrapper around every Read: the
	// callback publishes to a struct under a lock, and a 7 MiB body arrives
	// in tens of thousands of reads.
	total := res.ContentLength
	if total > limit {
		return nil, fmt.Errorf("%s is %d bytes, more than the %d this will take", url, total, limit)
	}
	progress(0, total)
	var buf bytes.Buffer
	if total > 0 {
		buf.Grow(int(total))
	}
	chunk := make([]byte, 256<<10)
	r := io.LimitReader(res.Body, limit)
	for {
		n, rerr := r.Read(chunk)
		buf.Write(chunk[:n])
		if n > 0 {
			progress(int64(buf.Len()), total)
		}
		if errors.Is(rerr, io.EOF) {
			return buf.Bytes(), nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

// binaryFromArchive pulls the one file that matters out of the tarball.
//
// The archive holds a directory with the binary, the deploy files and the
// READMEs. Only the binary is taken: replacing the unit files or the installer
// under a running service is a separate decision, and one nobody asked for by
// pressing "update".
func binaryFromArchive(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: not a gzip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: reading the archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "vibepanel" {
			continue
		}
		// Bounded like everything else: a tar header can claim any size.
		b, err := io.ReadAll(io.LimitReader(tr, maxArchive))
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, errors.New("selfupdate: the archive's binary is empty")
		}
		return b, nil
	}
	return nil, errors.New("selfupdate: no vibepanel binary in the archive")
}

// ErrNotWritable means the running binary cannot be replaced where it is.
var ErrNotWritable = errors.New("selfupdate: this binary is in a directory it cannot write to")

// Installable reports whether an update could be applied at all.
//
// Asked before the download rather than discovered after it. A system install
// puts the binary in /usr/local/bin, owned by root, and the panel runs as the
// user -- so the swap fails with
//
//	open /usr/local/bin/.vibepanel-update-2717682195: permission denied
//
// after a seven-megabyte download, as a raw errno, on a page whose button said
// "install". The capability is knowable up front and the answer does not
// change while somebody reads it.
//
// Probed by writing, not by reading mode bits. The mode is not the answer: a
// read-only mount, an ACL, an immutable flag and a full disk all present as a
// writable directory and fail at the same call this is standing in for.
func Installable() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("selfupdate: finding this binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("selfupdate: resolving %s: %w", self, err)
	}
	return installableAt(self)
}

func installableAt(self string) error {
	dir := filepath.Dir(self)
	f, err := os.CreateTemp(dir, ".vibepanel-update-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotWritable, dir)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// ErrWillNotRun means the downloaded binary was refused before the swap
// because it did not start here.
var ErrWillNotRun = errors.New("selfupdate: the new binary does not run on this machine")

// verifyWait bounds the one run of the new binary. `--version` returns before
// it opens anything; a binary that takes longer than this to print a line is
// not going to serve a panel either.
var verifyWait = 20 * time.Second

// Verify runs the new binary once, as `--version`, and refuses it unless it
// says it is the release that was asked for.
//
// The checksum proves the bytes are the ones published. It does not prove they
// run *here*: an archive labelled for this platform that was built for
// another, a binary directory on a `noexec` mount, a libc the static build
// turned out not to be static against -- each of those is a panel that will
// not come back after the restart, found out by everybody at once. Running it
// costs a few milliseconds and is the same thing a person would do by hand
// before replacing something that is holding their sessions.
//
// The version in the output is checked rather than the exit status alone. A
// zero exit from something that is not vibepanel -- `true`, say, or a shell
// -- is exactly the shape a wrong file in a right place takes.
func Verify(path, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), verifyWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		reason := strings.TrimSpace(string(out))
		if reason == "" {
			reason = err.Error()
		}
		return fmt.Errorf("%w: %s", ErrWillNotRun, reason)
	}
	said := strings.TrimSpace(string(out))
	if !strings.Contains(said, strings.TrimPrefix(want, "v")) {
		return fmt.Errorf("%w: it reports %q, not %s", ErrWillNotRun, firstLine(said), want)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Install writes the new binary over the running one, after Verify has seen
// it start.
//
// Rename, not truncate-and-write. A running program's file cannot be rewritten
// in place -- the kernel refuses with ETXTBSY -- but it can be renamed over,
// because the running process holds the old inode and keeps it until it exits.
// That is also what makes this recoverable: the old binary is moved aside
// first, so a panel that will not start again is one `mv` from working.
func Install(newBinary []byte, version string) (oldPath string, err error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("selfupdate: finding this binary: %w", err)
	}
	// The symlink, not the link. `~/.local/bin/vibepanel` is often a symlink to
	// somewhere versioned, and renaming over the link would replace the link
	// with a file and leave the real binary orphaned.
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("selfupdate: resolving %s: %w", self, err)
	}
	return installAt(self, newBinary, func(path string) error { return Verify(path, version) })
}

// installAt is Install with the path decided, so a test can exercise the swap
// without replacing the test binary that is running it. `verify` is run on
// the new file before anything is moved; nil skips it.
func installAt(self string, newBinary []byte, verify func(path string) error) (oldPath string, err error) {
	// Same directory, so the rename is on one filesystem. Across filesystems
	// rename fails and the copy that replaces it is not atomic -- which is the
	// one property this needs.
	tmp, err := os.CreateTemp(filepath.Dir(self), ".vibepanel-update-*")
	if err != nil {
		return "", fmt.Errorf("selfupdate: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(newBinary); err != nil {
		tmp.Close()
		return "", fmt.Errorf("selfupdate: writing the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	// Before the running binary is touched. A refusal here costs nothing: the
	// temp file is removed by the defer and the panel is exactly as it was.
	if verify != nil {
		if err := verify(tmpName); err != nil {
			return "", err
		}
	}

	backup := self + ".old"
	if err := os.Rename(self, backup); err != nil {
		return "", fmt.Errorf("selfupdate: moving the running binary aside: %w", err)
	}
	if err := os.Rename(tmpName, self); err != nil {
		// Put it back. A directory with no binary at all is the one outcome
		// worse than not updating.
		_ = os.Rename(backup, self)
		return "", fmt.Errorf("selfupdate: installing the new binary: %w", err)
	}
	return backup, nil
}
