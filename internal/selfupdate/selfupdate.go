// Package selfupdate replaces this binary with a newer release.
//
// This is the most dangerous thing in the product and the design says so out
// loud rather than reading as routine. What it does, in order: ask GitHub what
// the latest release of one hard-coded repository is; if it is newer than what
// is running, download that release's archive for this exact GOOS/GOARCH and
// its SHA256SUMS file; check the archive against the sums; unpack the binary;
// swap it into place; and then ask the service manager to restart the unit.
//
// What the checksum does and does not buy. It is fetched from the same release
// as the archive, so it detects a corrupt or truncated download and nothing
// else: whoever can publish a release can publish sums to match. That is the
// honest boundary, and it is the same trust anyone gets from `curl | tar`. What
// makes it defensible is that the repository is compiled in rather than
// configurable -- an update cannot be pointed somewhere else by a setting, a
// query parameter, or a value from the database.
//
// Nothing here runs on a timer. A self-hosted panel that phones home on its own
// schedule is a surprise; every request in this file happens because somebody
// pressed something.
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
	"net/http"
	"os"
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
	// Asset is the archive for this GOOS/GOARCH, empty when the release does
	// not carry one -- which is not an error, it is an answer.
	Asset string `json:"asset"`
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Client fetches releases. The zero value works.
type Client struct {
	// HTTP is the client used for every request. Nil means a bounded default:
	// an update that hangs must not hold a handler open forever.
	HTTP *http.Client
	// API overrides the GitHub endpoint, for tests only. Production never sets
	// it, which is what keeps Repo meaningful: asset and checksum URLs come out
	// of the release GitHub returned, so a test server's release points at the
	// test server and nothing has to be rewritten afterwards.
	API string
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) api() string {
	if c.API != "" {
		return c.API
	}
	return "https://api.github.com"
}

// AssetName is the archive this build would install.
func AssetName(version string) string {
	return fmt.Sprintf("vibepanel_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// Latest asks what the newest release is.
func (c *Client) Latest(ctx context.Context, current string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.api()+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.http().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("selfupdate: asking GitHub: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		// A repository with no releases yet. Not a failure, and saying
		// "not found" to somebody who pressed "check for updates" reads as one.
		return Release{Current: current}, nil
	}
	if res.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("selfupdate: GitHub answered %s", res.Status)
	}
	var gh ghRelease
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&gh); err != nil {
		return Release{}, fmt.Errorf("selfupdate: reading the release: %w", err)
	}

	out := Release{Version: gh.TagName, Current: current, URL: gh.HTMLURL, Notes: trim(gh.Body, 4000)}
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

// Fetch downloads the release archive, checks it, and returns the binary.
//
// The sums file is fetched first and on purpose: downloading a hundred
// megabytes and only then discovering there is nothing to check it against is
// the wrong order to find that out in.
func (c *Client) Fetch(ctx context.Context, rel Release) ([]byte, error) {
	if rel.Asset == "" {
		return nil, fmt.Errorf("selfupdate: release %s has no %s", rel.Version, AssetName(rel.Version))
	}
	sums, err := c.get(ctx, sumsURL(rel.Asset), 1<<20)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: SHA256SUMS: %w", err)
	}
	want, ok := sumFor(string(sums), AssetName(rel.Version))
	if !ok {
		return nil, fmt.Errorf("selfupdate: SHA256SUMS does not list %s", AssetName(rel.Version))
	}

	archive, err := c.get(ctx, rel.Asset, maxArchive)
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

func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
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
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
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

// Install writes the new binary over the running one.
//
// Rename, not truncate-and-write. A running program's file cannot be rewritten
// in place -- the kernel refuses with ETXTBSY -- but it can be renamed over,
// because the running process holds the old inode and keeps it until it exits.
// That is also what makes this recoverable: the old binary is moved aside
// first, so a panel that will not start again is one `mv` from working.
func Install(newBinary []byte) (oldPath string, err error) {
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
	return installAt(self, newBinary)
}

// installAt is Install with the path decided, so a test can exercise the swap
// without replacing the test binary that is running it.
func installAt(self string, newBinary []byte) (oldPath string, err error) {
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
