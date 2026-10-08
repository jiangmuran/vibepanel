package httpapi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Declared downloads: docs/plugins.md §5. A plugin package carries text;
// what it needs that is large -- a model, a native runtime -- is declared
// in the manifest with a hash, and the panel fetches it, verifies it,
// unpacks it under <data>/plugins/<id>/assets/<name>/ and hands the
// process the directory in VIBEPANEL_PLUGIN_ASSETS.
//
// The hash on the install screen is the approval. The panel refuses any
// other bytes, so an author who replaces the upstream file later changes
// nothing on the owner's disk; and the URL is fetched through the same
// guard a page's sources use -- https, every resolved address public, the
// checked address dialled -- with one difference the sources do not have:
// redirects are followed, up to five, each hop through the guard again,
// because that is what every release host does and the hash makes the
// final host a detail. A download is streamed to a temporary file with the
// digest computed as it arrives and renamed only when the digest matches;
// nothing half-arrived is ever where the process looks.
//
// Unpacking is the other place a download can hurt: an entry whose path
// climbs out, a symlink pointing anywhere, a bomb. Every entry is kept
// under the target, links are refused, and the unpacked total is capped.

const (
	pluginDownloadRedirects = 5
	pluginDownloadTimeout   = 2 * time.Hour
	pluginDownloadHeader    = 30 * time.Second
	pluginUnpackEntries     = 50000
	pluginUnpackFactor      = 8         // unpacked bytes at most this many times the declared size...
	pluginUnpackFloor       = 256 << 20 // ...or this, for a small archive that compresses well
)

// pluginDownloadJob is one download in flight or recently finished.
type pluginDownloadJob struct {
	Status    string `json:"status"` // downloading | verifying | unpacking | failed
	Received  int64  `json:"received"`
	Error     string `json:"error"`
	StartedAt int64  `json:"startedAt"`
	Host      string `json:"host"`
	cancel    context.CancelFunc
}

type pluginDownloadState struct {
	mu   sync.Mutex
	jobs map[string]*pluginDownloadJob
	// fetcher is swapped by tests to reach a server on loopback.
	fetcher *sourceFetcher
}

// pluginDownloadRow is what the card shows for one declared download.
type pluginDownloadRow struct {
	Name     string       `json:"name"`
	Label    plugins.Text `json:"label"`
	Optional bool         `json:"optional"`
	Size     int64        `json:"size"`
	Host     string       `json:"host"`
	SHA256   string       `json:"sha256"`
	Unpack   string       `json:"unpack"`
	// Ready is on disk and verified; ReadyAt when. Status is the job's,
	// "" when nothing is running.
	Ready    bool   `json:"ready"`
	ReadyAt  int64  `json:"readyAt"`
	Status   string `json:"status"`
	Received int64  `json:"received"`
	Error    string `json:"error"`
	Dir      string `json:"dir"`
}

func (s *Server) registerPluginDownloadRoutes(r chi.Router) {
	r.Get("/settings/plugins/{pluginID}/downloads", s.handlePluginDownloads)
	r.Post("/settings/plugins/{pluginID}/downloads/{name}", s.handleStartPluginDownload)
	r.Delete("/settings/plugins/{pluginID}/downloads/{name}", s.handleRemovePluginDownload)
}

// pluginAssetsDir is where a plugin's downloads live.
func (s *Server) pluginAssetsDir(id string) string {
	return filepath.Join(s.pluginProcessDir(id), "assets")
}

func jobKey(pluginID, name string) string { return pluginID + "/" + name }

// pluginDownloadReady is whether a declared download is on disk with the
// declared hash: the marker names the hash, so a manifest that changes
// the hash makes the old file not ready rather than silently reused.
func (s *Server) pluginDownloadReady(id string, d plugins.DownloadSpec) (bool, int64) {
	raw, err := os.ReadFile(filepath.Join(s.pluginAssetsDir(id), d.Name, ".ready")) //nolint:gosec // our own marker
	if err != nil {
		return false, 0
	}
	hash, at, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	if hash != strings.ToLower(d.SHA256) {
		return false, 0
	}
	t, _ := time.Parse(time.RFC3339, at)
	return true, t.Unix()
}

// pluginDownloadsMissing is the required downloads a plugin has not got:
// what its process waits for.
func (s *Server) pluginDownloadsMissing(id string, m plugins.Manifest) []string {
	var out []string
	for _, d := range m.Downloads {
		if d.Optional {
			continue
		}
		if ok, _ := s.pluginDownloadReady(id, d); !ok {
			out = append(out, d.Name)
		}
	}
	return out
}

func (s *Server) pluginDownloadRows(ctx context.Context, p store.Plugin) []pluginDownloadRow {
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		return []pluginDownloadRow{}
	}
	out := make([]pluginDownloadRow, 0, len(m.Downloads))
	st := &s.pdl
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, d := range m.Downloads {
		row := pluginDownloadRow{Name: d.Name, Label: d.Label, Optional: d.Optional, Size: d.Size, Host: d.Host(),
			SHA256: strings.ToLower(d.SHA256), Unpack: d.Unpack, Dir: filepath.Join(s.pluginAssetsDir(p.ID), d.Name)}
		row.Ready, row.ReadyAt = s.pluginDownloadReady(p.ID, d)
		if j := st.jobs[jobKey(p.ID, d.Name)]; j != nil {
			row.Status, row.Received, row.Error = j.Status, j.Received, j.Error
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) handlePluginDownloads(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.PluginByID(r.Context(), chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginDownloadRows(r.Context(), p))
}

func (s *Server) handleStartPluginDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	d, ok := downloadNamed(m, chi.URLParam(r, "name"))
	if !ok {
		writeErr(w, http.StatusNotFound, "this plugin declares no such download")
		return
	}
	if ready, _ := s.pluginDownloadReady(p.ID, d); ready {
		writeJSON(w, http.StatusOK, s.pluginDownloadRows(ctx, p))
		return
	}
	if started := s.startPluginDownload(p.ID, d, u.Username); !started {
		writeErr(w, http.StatusConflict, "that download is already running")
		return
	}
	s.audit(ctx, "plugin.download", u.Username, s.clientIP(r), p.ID+" "+d.Name+" from "+d.Host())
	writeJSON(w, http.StatusAccepted, s.pluginDownloadRows(ctx, p))
}

func (s *Server) handleRemovePluginDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	if !plugins.ValidID(name) {
		writeErr(w, http.StatusBadRequest, "not a download's name")
		return
	}
	st := &s.pdl
	st.mu.Lock()
	if j := st.jobs[jobKey(p.ID, name)]; j != nil && j.cancel != nil {
		j.cancel()
	}
	delete(st.jobs, jobKey(p.ID, name))
	st.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.pluginAssetsDir(p.ID), name)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(ctx, "plugin.download_removed", u.Username, s.clientIP(r), p.ID+" "+name)
	s.pluginsChanged()
	writeJSON(w, http.StatusOK, s.pluginDownloadRows(ctx, p))
}

func downloadNamed(m plugins.Manifest, name string) (plugins.DownloadSpec, bool) {
	for _, d := range m.Downloads {
		if d.Name == name {
			return d, true
		}
	}
	return plugins.DownloadSpec{}, false
}

// ensurePluginDownloads starts every required download an enabled plugin
// has not got. From pluginsChanged, so enabling a plugin fetches what it
// needs without a second click.
func (s *Server) ensurePluginDownloads(ctx context.Context) {
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		return
	}
	for _, p := range list {
		c, ok, err := s.pluginServiceCred(ctx, p)
		if err != nil || !ok {
			continue
		}
		for _, d := range c.manifest.Downloads {
			if d.Optional {
				continue
			}
			if ready, _ := s.pluginDownloadReady(p.ID, d); !ready {
				s.startPluginDownload(p.ID, d, "panel")
			}
		}
	}
}

// startPluginDownload runs one download in the background; false when one
// is already running for that name.
func (s *Server) startPluginDownload(pluginID string, d plugins.DownloadSpec, by string) bool {
	st := &s.pdl
	st.mu.Lock()
	if st.jobs == nil {
		st.jobs = map[string]*pluginDownloadJob{}
	}
	key := jobKey(pluginID, d.Name)
	if j := st.jobs[key]; j != nil && j.Status != "failed" {
		st.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithTimeout(s.serviceContext(), pluginDownloadTimeout)
	job := &pluginDownloadJob{Status: "downloading", StartedAt: time.Now().Unix(), Host: d.Host(), cancel: cancel}
	st.jobs[key] = job
	st.mu.Unlock()
	go func() {
		defer cancel()
		err := s.runPluginDownload(ctx, pluginID, d, job)
		st.mu.Lock()
		if err != nil {
			job.Status, job.Error = "failed", firstLine(err.Error())
		} else {
			delete(st.jobs, key)
		}
		st.mu.Unlock()
		if err != nil {
			s.pluginLog(pluginID, "error", "download "+d.Name+": "+firstLine(err.Error()))
			s.audit(context.Background(), "plugin.download_failed", by, "", pluginID+" "+d.Name+": "+firstLine(err.Error()))
			return
		}
		s.pluginLog(pluginID, "info", "download "+d.Name+" ready")
		s.audit(context.Background(), "plugin.download_ready", by, "", pluginID+" "+d.Name+" sha256 "+strings.ToLower(d.SHA256))
		// A process waiting for it starts now; one running sees it next
		// start (the env fingerprint, pluginprocess.go).
		s.pluginsChanged()
	}()
	return true
}

func (s *Server) runPluginDownload(ctx context.Context, pluginID string, d plugins.DownloadSpec, job *pluginDownloadJob) error {
	base := s.pluginAssetsDir(pluginID)
	tmpDir := filepath.Join(base, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, d.Name+"-*.part")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // gone after the rename, or on failure

	f := s.pdl.fetcher
	if f == nil {
		f = &sourceFetcher{}
	}
	resp, err := f.fetchFollowing(ctx, d.URL, func(host string) {
		s.pdl.mu.Lock()
		job.Host = host
		s.pdl.mu.Unlock()
	})
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("the host answered %d", resp.StatusCode)
	}
	// The declared size caps the transfer, with room for an author who
	// rounded; the hash is the real check.
	limit := d.Size + d.Size/10 + 1<<20
	sum := sha256.New()
	counted := &countingWriter{job: job, st: &s.pdl}
	n, err := io.Copy(io.MultiWriter(tmp, sum, counted), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("transfer failed after %d bytes: %s", n, trimNetError(err))
	}
	if n > limit {
		tmp.Close()
		return fmt.Errorf("more than the declared %d bytes arrived; refused", d.Size)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	s.pdl.mu.Lock()
	job.Status = "verifying"
	s.pdl.mu.Unlock()
	got := hex.EncodeToString(sum.Sum(nil))
	if got != strings.ToLower(d.SHA256) {
		return fmt.Errorf("sha256 mismatch: the file is %s…, the manifest says %s…; nothing was kept", got[:12], strings.ToLower(d.SHA256)[:12])
	}

	s.pdl.mu.Lock()
	job.Status = "unpacking"
	s.pdl.mu.Unlock()
	target := filepath.Join(base, d.Name)
	staging := filepath.Join(tmpDir, d.Name+".new")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	if d.Unpack == "" {
		if err := os.Rename(tmpPath, filepath.Join(staging, path.Base(urlPath(d.URL)))); err != nil {
			return err
		}
	} else if err := unpackInto(tmpPath, d.Unpack, staging, max(d.Size*pluginUnpackFactor, pluginUnpackFloor)); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("unpack: %w", err)
	}
	marker := strings.ToLower(d.SHA256) + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(staging, ".ready"), []byte(marker), 0o600); err != nil {
		return err
	}
	_ = os.RemoveAll(target)
	return os.Rename(staging, target)
}

type countingWriter struct {
	job *pluginDownloadJob
	st  *pluginDownloadState
	n   int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	c.st.mu.Lock()
	c.job.Received = c.n
	c.st.mu.Unlock()
	return len(p), nil
}

func urlPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" || u.Path == "/" {
		return "download"
	}
	return u.Path
}

// fetchFollowing is one GET through the guard with up to five redirects,
// each hop a fresh guarded client for the new URL. onHost reports each
// host so the card can say where the bytes are actually coming from.
func (f *sourceFetcher) fetchFollowing(ctx context.Context, rawURL string, onHost func(string)) (*http.Response, error) {
	current := rawURL
	for hop := 0; hop <= pluginDownloadRedirects; hop++ {
		client, problem := f.guardedClient(ctx, current, pluginDownloadHeader)
		if problem != "" {
			return nil, errors.New(problem)
		}
		// The 3xx itself, unfollowed: the next hop is built here, through
		// the guard again, rather than by the client.
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if u, err := url.Parse(current); err == nil && onHost != nil {
			onHost(strings.ToLower(u.Hostname()))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, errors.New("bad URL")
		}
		req.Header.Set("User-Agent", "vibepanel-download/1")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch failed: %s", trimNetError(err))
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if loc == "" {
				return nil, fmt.Errorf("the host answered %d without a location", resp.StatusCode)
			}
			next, err := url.Parse(current)
			if err != nil {
				return nil, errors.New("bad URL")
			}
			ref, err := url.Parse(loc)
			if err != nil {
				return nil, errors.New("the redirect is not a URL")
			}
			current = next.ResolveReference(ref).String()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("more than %d redirects", pluginDownloadRedirects)
}

// unpackInto writes an archive's regular files and directories under dst,
// refusing anything else. The checks are the whole point of doing this in
// the panel rather than handing the process a tarball.
func unpackInto(archive, kind, dst string, maxBytes int64) error {
	in, err := os.Open(archive) //nolint:gosec // our own temp file
	if err != nil {
		return err
	}
	defer in.Close()
	var total int64
	entries := 0
	place := func(name string) (string, error) {
		entries++
		if entries > pluginUnpackEntries {
			return "", fmt.Errorf("more than %d entries", pluginUnpackEntries)
		}
		// `..` is refused as written, before any cleaning: an archive that
		// says it is hostile, and Clean would quietly make it polite. An
		// absolute name is kept under the directory.
		slashed := strings.ReplaceAll(name, "\\", "/")
		for _, seg := range strings.Split(slashed, "/") {
			if seg == ".." {
				return "", fmt.Errorf("%q climbs out of the directory", name)
			}
		}
		clean := path.Clean("/" + slashed)
		if clean == "/" {
			return "", errors.New("an entry with no name")
		}
		return filepath.Join(dst, filepath.FromSlash(clean)), nil
	}
	writeFile := func(p string, mode os.FileMode, r io.Reader, size int64) error {
		if size < 0 || total+size > maxBytes {
			return fmt.Errorf("unpacked bytes over the %d cap", maxBytes)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		perm := os.FileMode(0o600)
		if mode&0o111 != 0 {
			perm = 0o700
		}
		out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm) //nolint:gosec // under dst, checked
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(r, maxBytes-total+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		total += n
		if total > maxBytes {
			return fmt.Errorf("unpacked bytes over the %d cap", maxBytes)
		}
		return err
	}
	switch kind {
	case "zip":
		info, err := in.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(in, info.Size())
		if err != nil {
			return err
		}
		for _, zf := range zr.File {
			p, err := place(zf.Name)
			if err != nil {
				return err
			}
			mode := zf.Mode()
			if mode&os.ModeSymlink != 0 || (mode&os.ModeType != 0 && !mode.IsDir()) {
				return fmt.Errorf("%q is not a file or a directory", zf.Name)
			}
			if mode.IsDir() {
				if err := os.MkdirAll(p, 0o700); err != nil {
					return err
				}
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			err = writeFile(p, mode, rc, int64(zf.UncompressedSize64)) //nolint:gosec // bounded by maxBytes inside
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	case "tar.gz", "tgz", "tar.bz2":
		var r io.Reader
		if kind == "tar.bz2" {
			r = bzip2.NewReader(in)
		} else {
			gz, err := gzip.NewReader(in)
			if err != nil {
				return err
			}
			defer gz.Close()
			r = gz
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeDir:
				p, err := place(h.Name)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(p, 0o700); err != nil {
					return err
				}
			case tar.TypeReg:
				p, err := place(h.Name)
				if err != nil {
					return err
				}
				if err := writeFile(p, os.FileMode(h.Mode), tr, h.Size); err != nil { //nolint:gosec // mode bits only
					return err
				}
			case tar.TypeXGlobalHeader, tar.TypeXHeader:
				continue
			default:
				return fmt.Errorf("%q is a %q entry, not a file or a directory", h.Name, string(h.Typeflag))
			}
		}
	}
	return fmt.Errorf("unknown archive kind %q", kind)
}

// For tests: an archive in memory.
func tarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
