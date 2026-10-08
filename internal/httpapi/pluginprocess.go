package httpapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Rung 3, a process: docs/plugins.md §5. A command the panel supervises,
// started from the version's checked-out directory with a cleared
// environment, its own token in VIBEPANEL_PLUGIN_URL, a state directory, and
// the secrets the manifest names. This is the supervisor the earlier design
// said was worth building and worth building last, and its three tests are
// the ones it asked for: a process that never exits is ended at shutdown; one
// that crashes in a loop is stopped and reported; one that prints faster than
// anyone reads fills a ring and not the panel's memory.
//
// The process is the panel's child on purpose. It holds no session, so red
// line 2 does not apply to it, and a plugin that outlived the panel would be
// a daemon nobody supervises, which is the problem this rung exists to
// remove. Where a sessions scope exists the process is moved into a leaf of
// the pool beside the sessions, so a plugin that leaks is measured and
// squeezed with them rather than with the panel (red line 9).

const (
	pluginOutputRing      = 64 << 10
	pluginRestartMin      = time.Second
	pluginRestartMax      = time.Minute
	pluginFailureCap      = 10
	pluginFailureWindow   = 10 * time.Minute
	pluginStopGrace       = 5 * time.Second
	pluginProcessEnvLimit = 32
)

type pluginProc struct {
	id       string
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	pid      int
	since    time.Time
	restarts int
	failures []time.Time
	// stopped is the supervisor's decision: the cap was reached, or the
	// plugin was disabled. A stopped process is not restarted until the
	// owner asks.
	stopped  bool
	stopWhy  string
	lastExit string
	exitedAt time.Time
	ring     []byte
	version  int
	dev      bool
	// socket and proxySecret exist when the manifest mounts the process on
	// the panel's port: the unix socket the process listens on, and the
	// per-start secret every proxied request carries (pluginhttp.go).
	socket      string
	proxySecret string
	// env is the fingerprint of what the process was started with (the
	// version, dev mode, its secrets' values, which downloads were there):
	// a change restarts it, because a secret the owner just saved and a
	// model that just arrived are not things a running process can see.
	env       string
	done      chan struct{}
	ringMu    sync.Mutex
	stateMu   sync.Mutex
	manualEnd bool
}

type pluginProcessState struct {
	mu    sync.Mutex
	procs map[string]*pluginProc
	// lookPath is swapped by tests; nil is exec.LookPath.
	lookPath func(string) (string, error)
	// The backoff and the cap, as fields so a test can watch a crash loop
	// reach the cap in milliseconds rather than minutes. Zero is the default.
	restartMin, restartMax time.Duration
	failureCap             int
}

func (st *pluginProcessState) backoffs() (time.Duration, time.Duration, int) {
	lo, hi, cap := st.restartMin, st.restartMax, st.failureCap
	if lo <= 0 {
		lo = pluginRestartMin
	}
	if hi <= 0 {
		hi = pluginRestartMax
	}
	if cap <= 0 {
		cap = pluginFailureCap
	}
	return lo, hi, cap
}

// pluginProcessStatus is what the card shows.
type pluginProcessStatus struct {
	Declared bool   `json:"declared"`
	Running  bool   `json:"running"`
	PID      int    `json:"pid"`
	Since    int64  `json:"since"`
	Restarts int    `json:"restarts"`
	Stopped  bool   `json:"stopped"`
	StopWhy  string `json:"stopWhy"`
	LastExit string `json:"lastExit"`
	ExitedAt int64  `json:"exitedAt"`
	Output   string `json:"output"`
	Command  string `json:"command"`
	OnPath   bool   `json:"onPath"`
	// Mount is the path on the panel's port when the manifest asks for one;
	// SocketUp whether the process has opened its socket there.
	Mount    string `json:"mount"`
	Auth     string `json:"auth"`
	SocketUp bool   `json:"socketUp"`
	// WaitingFor is the required downloads not yet on disk: the process is
	// not started until they are.
	WaitingFor []string `json:"waitingFor"`
}

// ensurePluginProcesses starts a process for every enabled plugin that
// declares one and stops the ones that should no longer run. Called at
// startup, from pluginsChanged, and from the owner's restart.
func (s *Server) ensurePluginProcesses(ctx context.Context) {
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		return
	}
	want := map[string]pluginCred{}
	for _, p := range list {
		c, ok, err := s.pluginServiceCred(ctx, p)
		if err != nil || !ok || c.manifest.Process == nil {
			continue
		}
		// A process whose required downloads are not there yet waits for
		// them; the card says so, and the download's end brings it here
		// again through pluginsChanged.
		if len(s.pluginDownloadsMissing(p.ID, c.manifest)) > 0 {
			continue
		}
		want[p.ID] = c
	}
	st := &s.ppr
	st.mu.Lock()
	if st.procs == nil {
		st.procs = map[string]*pluginProc{}
	}
	fps := map[string]string{}
	for id, c := range want {
		fps[id] = s.pluginEnvFingerprint(ctx, c)
	}
	var stop []*pluginProc
	for id, pr := range st.procs {
		c, ok := want[id]
		if !ok || pr.version != c.plugin.InstalledVersion || pr.dev != (c.ns == store.PageDataDraft) || pr.env != fps[id] {
			stop = append(stop, pr)
			delete(st.procs, id)
		}
	}
	var start []pluginCred
	for id, c := range want {
		if _, ok := st.procs[id]; !ok {
			start = append(start, c)
		}
	}
	st.mu.Unlock()
	for _, pr := range stop {
		s.stopPluginProc(pr, "the plugin was disabled or changed")
	}
	for _, c := range start {
		s.startPluginProc(ctx, c, fps[c.plugin.ID])
	}
}

// pluginEnvFingerprint names everything a process is started with that
// can change under it. Hashed, never kept in the clear: it holds secrets.
func (s *Server) pluginEnvFingerprint(ctx context.Context, c pluginCred) string {
	h := sha256.New()
	fmt.Fprintf(h, "v%d dev=%v\n", c.plugin.InstalledVersion, c.ns == store.PageDataDraft)
	names := append([]string{}, c.manifest.Process.Env...)
	if c.manifest.Process.HTTP != nil && c.manifest.Process.HTTP.Secret != "" {
		names = append(names, c.manifest.Process.HTTP.Secret)
	}
	for _, name := range names {
		v, err := s.pluginSecretValue(ctx, c.plugin.ID, name)
		if err != nil {
			v = ""
		}
		fmt.Fprintf(h, "%s=%s\n", name, v)
	}
	for _, d := range c.manifest.Downloads {
		ready, at := s.pluginDownloadReady(c.plugin.ID, d)
		fmt.Fprintf(h, "download %s %v %d\n", d.Name, ready, at)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// StopPluginProcesses ends every plugin process, for shutdown: SIGTERM, five
// seconds, SIGKILL. A plugin never outlives the panel.
func (s *Server) StopPluginProcesses(ctx context.Context) {
	st := &s.ppr
	st.mu.Lock()
	procs := make([]*pluginProc, 0, len(st.procs))
	for _, pr := range st.procs {
		procs = append(procs, pr)
	}
	st.procs = map[string]*pluginProc{}
	st.mu.Unlock()
	var wg sync.WaitGroup
	for _, pr := range procs {
		wg.Add(1)
		go func(pr *pluginProc) {
			defer wg.Done()
			s.stopPluginProc(pr, "the panel is stopping")
		}(pr)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// pluginProcessDir is where a plugin's checked-out versions and its state
// live: <data dir>/plugins/<id>/.
func (s *Server) pluginProcessDir(id string) string {
	return filepath.Join(s.Cfg.DataDir, "plugins", id)
}

// checkoutPluginVersion writes an installed version's files to disk, once,
// where its process runs from. A directory that already holds the version is
// left alone: the files are immutable by construction.
func (s *Server) checkoutPluginVersion(ctx context.Context, c pluginCred) (string, error) {
	dir := filepath.Join(s.pluginProcessDir(c.plugin.ID), "v"+strconv.Itoa(c.plugin.InstalledVersion))
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	files, err := s.DB.PluginFiles(ctx, c.plugin.ID, c.plugin.InstalledVersion)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, f := range files {
		_, data, err := s.DB.PluginFileData(ctx, c.plugin.ID, c.plugin.InstalledVersion, f.Path)
		if err != nil {
			return "", err
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return "", err
		}
	}
	if raw, err := c.manifest.Encode(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, plugins.ManifestFile), raw, 0o600)
	}
	return dir, os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}

func (s *Server) startPluginProc(ctx context.Context, c pluginCred, env string) {
	spec := c.manifest.Process
	pr := &pluginProc{id: c.plugin.ID, version: c.plugin.InstalledVersion, dev: c.ns == store.PageDataDraft, env: env}
	st := &s.ppr
	st.mu.Lock()
	if _, ok := st.procs[c.plugin.ID]; ok {
		st.mu.Unlock()
		return
	}
	st.procs[c.plugin.ID] = pr
	st.mu.Unlock()
	go s.supervisePlugin(ctx, c, pr, spec)
}

// supervisePlugin runs the process and brings it back, with backoff and a
// cap, until stopped. One goroutine per plugin, for the life of the plugin.
func (s *Server) supervisePlugin(ctx context.Context, c pluginCred, pr *pluginProc, spec *plugins.ProcessSpec) {
	restartMin, restartMax, failureCap := s.ppr.backoffs()
	backoff := restartMin
	for {
		if ctx.Err() != nil {
			return
		}
		pr.stateMu.Lock()
		stopped := pr.stopped
		pr.stateMu.Unlock()
		if stopped {
			return
		}
		exitErr, started := s.runPluginProcOnce(ctx, c, pr, spec)
		pr.stateMu.Lock()
		pr.exitedAt = time.Now()
		pr.pid = 0
		if exitErr != nil {
			pr.lastExit = firstLine(exitErr.Error())
		} else if started {
			pr.lastExit = "exited 0"
		}
		manual := pr.manualEnd
		pr.manualEnd = false
		stopped = pr.stopped
		pr.stateMu.Unlock()
		if stopped || ctx.Err() != nil {
			return
		}
		if manual {
			// Ended by the owner's restart: straight back, no backoff.
			backoff = restartMin
			continue
		}
		clean := started && exitErr == nil
		restart := spec.Restart
		if restart == "" {
			restart = "on-failure"
		}
		if restart == "never" || (restart == "on-failure" && clean) {
			s.stopPluginProcStateOnly(pr, "the process exited and restart is "+restart)
			return
		}
		// The cap: ten failures in ten minutes stops the plugin and says so.
		now := time.Now()
		pr.stateMu.Lock()
		kept := pr.failures[:0]
		for _, t := range pr.failures {
			if now.Sub(t) < pluginFailureWindow {
				kept = append(kept, t)
			}
		}
		pr.failures = append(kept, now)
		failures := len(pr.failures)
		pr.restarts++
		pr.stateMu.Unlock()
		if failures >= failureCap {
			s.stopPluginProcStateOnly(pr, fmt.Sprintf("crashed %d times in %v", failures, pluginFailureWindow))
			s.audit(ctx, "plugin.crashed", "panel", "", fmt.Sprintf("%s stopped after %d failures: %s", c.plugin.ID, failures, pr.lastExit))
			s.pluginLog(c.plugin.ID, "error", "process stopped: "+pr.stopWhy)
			return
		}
		s.pluginLog(c.plugin.ID, "error", fmt.Sprintf("process %s; restarting in %v", pr.lastExit, backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > restartMax {
			backoff = restartMax
		}
	}
}

// runPluginProcOnce starts the process and waits for it. started is false
// when it could not be started at all (a program not on PATH), which counts
// as a failure for the cap.
func (s *Server) runPluginProcOnce(ctx context.Context, c pluginCred, pr *pluginProc, spec *plugins.ProcessSpec) (error, bool) {
	var dir string
	var err error
	if c.ns == store.PageDataDraft {
		dir = c.plugin.SourceDir
	} else if dir, err = s.checkoutPluginVersion(ctx, c); err != nil {
		return fmt.Errorf("checkout: %w", err), false
	}
	stateDir := filepath.Join(s.pluginProcessDir(c.plugin.ID), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err, false
	}
	// A token of its own, minted at every start; the previous one dies with
	// the previous process.
	token, err := auth.NewToken()
	if err != nil {
		return err, false
	}
	if err := s.DB.CreatePluginToken(ctx, auth.HashToken(token), c.plugin.ID); err != nil {
		return err, false
	}
	lookPath := s.ppr.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	bin, err := lookPath(spec.Command[0])
	if err != nil {
		return fmt.Errorf("%s is not on PATH", spec.Command[0]), false
	}
	pctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(pctx, bin, spec.Command[1:]...)
	cmd.Dir = dir
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + firstNonEmpty(os.Getenv("LANG"), "C.UTF-8"),
		"VIBEPANEL_PLUGIN_URL=" + strings.TrimSuffix(s.Cfg.LoopbackURL(), "/") + "/api/plugin/" + token + "/v1/",
		"VIBEPANEL_PLUGIN_STATE=" + stateDir,
		"VIBEPANEL_PLUGIN_ID=" + c.plugin.ID,
	}
	if len(c.manifest.Downloads) > 0 {
		env = append(env, "VIBEPANEL_PLUGIN_ASSETS="+s.pluginAssetsDir(c.plugin.ID))
	}
	for i, name := range spec.Env {
		if i >= pluginProcessEnvLimit {
			break
		}
		if v, err := s.pluginSecretValue(ctx, c.plugin.ID, name); err == nil {
			env = append(env, name+"="+v)
		}
	}
	var sock, proxySecret string
	if spec.HTTP != nil {
		sock = s.pluginSocketPath(c.plugin.ID)
		if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
			cancel()
			return err, false
		}
		_ = os.Remove(sock)
		if proxySecret, err = auth.NewToken(); err != nil {
			cancel()
			return err, false
		}
		env = append(env,
			"VIBEPANEL_PLUGIN_SOCKET="+sock,
			"VIBEPANEL_PLUGIN_PROXY_SECRET="+proxySecret,
			"VIBEPANEL_PLUGIN_MOUNT="+pluginMountPath(c.plugin.ID))
	}
	cmd.Env = env
	// Its own process group, so a shutdown signal reaches what it forked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = pluginStopGrace
	cmd.Cancel = func() error {
		// SIGTERM first; WaitDelay sends the SIGKILL.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err, false
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return err, false
	}
	pr.stateMu.Lock()
	pr.cmd, pr.cancel, pr.pid, pr.since = cmd, cancel, cmd.Process.Pid, time.Now()
	pr.socket, pr.proxySecret = sock, proxySecret
	pr.done = make(chan struct{})
	pr.stateMu.Unlock()
	if sock != "" {
		defer os.Remove(sock) //nolint:errcheck // the next start removes it too
	}
	if s.Resources != nil {
		s.Resources.PlacePlugin(c.plugin.ID, cmd.Process.Pid)
	}
	s.pluginLog(c.plugin.ID, "info", fmt.Sprintf("process started, pid %d", cmd.Process.Pid))

	// The output ring: dropped oldest-first, so a process printing in a loop
	// fills 64 KiB and not the disk the projects live on.
	reader := bufio.NewReaderSize(stdout, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			pr.ringMu.Lock()
			pr.ring = append(pr.ring, buf[:n]...)
			if len(pr.ring) > pluginOutputRing {
				pr.ring = append([]byte(nil), pr.ring[len(pr.ring)-pluginOutputRing:]...)
			}
			pr.ringMu.Unlock()
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()
	cancel()
	pr.stateMu.Lock()
	close(pr.done)
	pr.stateMu.Unlock()
	_ = s.DB.DeletePluginTokens(context.WithoutCancel(ctx), c.plugin.ID)
	if s.Resources != nil {
		s.Resources.EndPlugin(c.plugin.ID)
	}
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return fmt.Errorf("exited %d", ee.ExitCode()), true
		}
		return werr, true
	}
	return nil, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) stopPluginProcStateOnly(pr *pluginProc, why string) {
	pr.stateMu.Lock()
	pr.stopped, pr.stopWhy = true, why
	pr.stateMu.Unlock()
}

// stopPluginProc marks a process stopped and ends it: SIGTERM, the grace,
// SIGKILL.
func (s *Server) stopPluginProc(pr *pluginProc, why string) {
	s.stopPluginProcStateOnly(pr, why)
	pr.stateMu.Lock()
	cancel, done := pr.cancel, pr.done
	pr.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(pluginStopGrace + 2*time.Second):
		}
	}
}

// pluginProcessStatusFor is the card's view of a plugin's process.
func (s *Server) pluginProcessStatusFor(ctx context.Context, p store.Plugin) pluginProcessStatus {
	out := pluginProcessStatus{}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil || m.Process == nil {
		return out
	}
	out.Declared = true
	out.Command = strings.Join(m.Process.Command, " ")
	if m.Process.HTTP != nil {
		out.Mount, out.Auth = pluginMountPath(p.ID), m.Process.HTTP.Auth
	}
	out.WaitingFor = s.pluginDownloadsMissing(p.ID, m)
	if out.WaitingFor == nil {
		out.WaitingFor = []string{}
	}
	lookPath := s.ppr.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, lerr := lookPath(m.Process.Command[0])
	out.OnPath = lerr == nil
	st := &s.ppr
	st.mu.Lock()
	pr := st.procs[p.ID]
	st.mu.Unlock()
	if pr == nil {
		return out
	}
	pr.stateMu.Lock()
	out.Running = pr.pid > 0
	if pr.socket != "" {
		_, serr := os.Stat(pr.socket)
		out.SocketUp = serr == nil
	}
	out.PID = pr.pid
	if !pr.since.IsZero() {
		out.Since = pr.since.Unix()
	}
	out.Restarts, out.Stopped, out.StopWhy, out.LastExit = pr.restarts, pr.stopped, pr.stopWhy, pr.lastExit
	if !pr.exitedAt.IsZero() {
		out.ExitedAt = pr.exitedAt.Unix()
	}
	pr.stateMu.Unlock()
	pr.ringMu.Lock()
	out.Output = strings.ToValidUTF8(string(pr.ring), "�")
	pr.ringMu.Unlock()
	return out
}

func (s *Server) registerPluginProcessRoutes(r chi.Router) {
	r.Get("/settings/plugins/{pluginID}/process", s.handlePluginProcess)
	r.Post("/settings/plugins/{pluginID}/process/restart", s.handlePluginProcessRestart)
}

func (s *Server) handlePluginProcess(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.PluginByID(r.Context(), chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.pluginProcessStatusFor(r.Context(), p))
}

// handlePluginProcessRestart ends the process and lets the supervisor bring
// it back at once, clearing a stop: the owner's answer to "crashed ten
// times" after fixing whatever it was.
func (s *Server) handlePluginProcessRestart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	st := &s.ppr
	st.mu.Lock()
	pr := st.procs[p.ID]
	if pr != nil {
		delete(st.procs, p.ID)
	}
	st.mu.Unlock()
	if pr != nil {
		pr.stateMu.Lock()
		pr.manualEnd = true
		pr.stateMu.Unlock()
		s.stopPluginProc(pr, "restarted by the owner")
	}
	s.audit(ctx, "plugin.process_restarted", u.Username, s.clientIP(r), p.ID)
	s.ensurePluginProcesses(s.serviceContext())
	writeJSON(w, http.StatusOK, s.pluginProcessStatusFor(ctx, p))
}

// serviceContext is the context long-lived plugin work runs under: Poll's,
// or the background when Poll is not running (tests, the CLI).
func (s *Server) serviceContext() context.Context {
	if s.serviceCtx != nil {
		return s.serviceCtx
	}
	return context.Background()
}

// pluginProcessRows is each plugin's process, for the list.
func (s *Server) pluginProcessRows(ctx context.Context) map[string]pluginProcessStatus {
	out := map[string]pluginProcessStatus{}
	st := &s.ppr
	st.mu.Lock()
	ids := make([]string, 0, len(st.procs))
	for id := range st.procs {
		ids = append(ids, id)
	}
	st.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		if p, err := s.DB.PluginByID(ctx, id); err == nil {
			out[id] = s.pluginProcessStatusFor(ctx, p)
		}
	}
	return out
}
