package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Rung 2, a service: docs/plugins.md §5. A plugin's server.js runs inside the
// panel in the page runtime's shape -- a fresh goja runtime per call from a
// program compiled once per version, a budget enforced by interrupting it,
// data writes buffered and committed only on a clean return, a result of at
// most 64 KiB, a log of 200 lines -- with more hooks and a ctx that has one
// member per granted capability:
//
//	function onEvent(ev, ctx)        session.state, session.created, session.gone,
//	                                  project.archived, todo.changed, note.changed; ≤ 500 ms
//	function onSchedule(ctx)         every `every` (≥ 1m); ≤ 500 ms
//	function <route>(req, ctx)       a route from server.routes; ≤ 500 ms, four at a time
//	function onInbound(req, ctx)     the declared inbound route, after the signature check
//
// ctx.data, ctx.settings, ctx.secret, ctx.sources, ctx.now and ctx.log are
// every service's; ctx.panel.view exists only with read:panel, ctx.notes.set
// only with write:notes, ctx.fetch reaches only hosts with net:<host>. A
// member that was not granted is absent, not a stub that throws, so a script
// can be read for what it asks.
//
// Nothing here runs on the poller's goroutine. Events are a bounded channel
// per plugin (256, oldest dropped, the drop counted) fed by a non-blocking
// send from the same places the flow log is fed; there is no veto.

const (
	pluginEventBudget    = 500 * time.Millisecond
	pluginScheduleBudget = 500 * time.Millisecond
	pluginRouteBudget    = 500 * time.Millisecond
	pluginFetchMax       = 5 * time.Second
	pluginEventQueue     = 256
	pluginRouteParallel  = 4
	pluginServiceTick    = 30 * time.Second
	pluginInboundRate    = 60
	pluginLogLines       = 200
	pluginLogLineMax     = 1000
	pluginProgramEntries = 256
	pluginRouteBody      = 64 << 10
)

// pluginEvent is one thing that happened, before it is translated into a
// plugin's handles. Real ids here; a plugin never sees them.
type pluginEvent struct {
	Name      string
	At        int64
	SessionID string
	ProjectID string
	State     string
	Previous  string
	Kind      string
}

type pluginWorker struct {
	ch      chan pluginEvent
	dropped int
}

type pluginServiceState struct {
	mu       sync.Mutex
	programs map[string]compiledServer
	logs     map[string][]serverLogLine
	workers  map[string]*pluginWorker
	schedule map[string]time.Time
	inbound  map[string][]time.Time
	sources  map[string]*sourceResult
	srcRun   map[string]time.Time
	routes   map[string]chan struct{}
	// fetcher is swapped by tests to reach a server on loopback.
	fetcher *sourceFetcher
	tick    time.Duration
	started bool
}

// pluginServiceLoop is the service rung's clock: every tick it runs the
// schedules that are due and refreshes the sources that are due, for every
// enabled plugin with a server. Started from Poll, beside the event drain,
// and never on the poll goroutine itself.
func (s *Server) pluginServiceLoop(ctx context.Context) {
	tick := s.psv.tick
	if tick <= 0 {
		tick = pluginServiceTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pluginServiceOnce(ctx)
		}
	}
}

func (s *Server) pluginServiceOnce(ctx context.Context) {
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		return
	}
	for _, p := range list {
		c, ok, err := s.pluginServiceCred(ctx, p)
		if err != nil || !ok {
			continue
		}
		s.refreshPluginSources(ctx, c, false)
		s.runPluginSchedule(ctx, c)
	}
}

// pluginServiceCred is the credential a service runs under: the plugin's own,
// with the manifest that runs and the capabilities granted. nil when the
// plugin does not run or has no server.
func (s *Server) pluginServiceCred(ctx context.Context, p store.Plugin) (pluginCred, bool, error) {
	if !p.Enabled && !(p.Dev && p.SourceDir != "") {
		return pluginCred{}, false, nil
	}
	m, ns, err := s.pluginRunningManifest(ctx, p)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return pluginCred{}, false, nil
		}
		return pluginCred{}, false, err
	}
	caps, err := s.DB.PluginCaps(ctx, p.ID)
	if err != nil {
		return pluginCred{}, false, err
	}
	granted := map[string]bool{}
	wanted := m.AllCapabilities()
	for _, c := range caps {
		if contains(wanted, c.Cap) {
			granted[c.Cap] = true
		}
	}
	return pluginCred{plugin: p, manifest: m, ns: ns, caps: granted, user: "plugin:" + p.ID}, true, nil
}

// ─── events ───────────────────────────────────────────────────────────────

// pluginEventRaised is called wherever something a plugin may subscribe to
// happened. It cannot block: the send is non-blocking and a full queue
// drops the oldest, which is counted. The panel's own state never waits.
func (s *Server) pluginEventRaised(ev pluginEvent) {
	if s.DB == nil {
		return
	}
	ev.At = time.Now().Unix()
	st := &s.psv
	st.mu.Lock()
	workers := make(map[string]*pluginWorker, len(st.workers))
	for k, w := range st.workers {
		workers[k] = w
	}
	st.mu.Unlock()
	for id, w := range workers {
		select {
		case w.ch <- ev:
		default:
			// Drop the oldest to make room, counted: a plugin that cannot keep
			// up loses its own events and nothing else.
			select {
			case <-w.ch:
			default:
			}
			st.mu.Lock()
			w.dropped++
			st.mu.Unlock()
			select {
			case w.ch <- ev:
			default:
			}
			_ = id
		}
	}
}

// ensurePluginWorkers starts a worker for every enabled plugin that
// subscribes to events, and stops the ones that no longer do. Called when
// the installed set changes and at startup.
func (s *Server) ensurePluginWorkers(ctx context.Context) {
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		return
	}
	want := map[string]bool{}
	for _, p := range list {
		c, ok, err := s.pluginServiceCred(ctx, p)
		if err != nil || !ok || c.manifest.Server == nil || len(c.manifest.Server.On) == 0 {
			continue
		}
		want[p.ID] = true
	}
	st := &s.psv
	st.mu.Lock()
	if st.workers == nil {
		st.workers = map[string]*pluginWorker{}
	}
	for id, w := range st.workers {
		if !want[id] {
			close(w.ch)
			delete(st.workers, id)
		}
	}
	var started []string
	for id := range want {
		if _, ok := st.workers[id]; !ok {
			w := &pluginWorker{ch: make(chan pluginEvent, pluginEventQueue)}
			st.workers[id] = w
			started = append(started, id)
			go s.pluginEventWorker(ctx, id, w)
		}
	}
	st.mu.Unlock()
	for _, id := range started {
		s.Log.Debug("plugin events", "plugin", id, "status", "subscribed")
	}
}

func (s *Server) pluginEventWorker(ctx context.Context, id string, w *pluginWorker) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.ch:
			if !ok {
				return
			}
			p, err := s.DB.PluginByID(ctx, id)
			if err != nil {
				return
			}
			c, ok, err := s.pluginServiceCred(ctx, p)
			if err != nil || !ok || c.manifest.Server == nil || !contains(c.manifest.Server.On, ev.Name) {
				continue
			}
			arg := map[string]any{"name": ev.Name, "at": ev.At}
			if ev.SessionID != "" {
				arg["session"] = s.pluginHandle(ctx, id, ev.SessionID)
			}
			if ev.ProjectID != "" {
				arg["project"] = s.pluginHandle(ctx, id, ev.ProjectID)
			}
			if ev.State != "" {
				arg["state"] = ev.State
			}
			if ev.Previous != "" {
				arg["previous"] = ev.Previous
			}
			if ev.Kind != "" {
				arg["kind"] = ev.Kind
			}
			if _, err := s.runPluginHook(ctx, c, "onEvent", []any{arg}, pluginEventBudget, "onEvent"); err != nil && !errors.Is(err, errNoHook) {
				s.Log.Debug("plugin onEvent", "plugin", id, "err", err)
			}
		}
	}
}

// pluginsChangedServices is called with pluginsChanged: workers follow the
// installed set, and a program cache for a version that no longer runs is
// left to age out.
func (s *Server) pluginsChangedServices() {
	ctx := s.serviceCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.ensurePluginWorkers(ctx)
}

// ─── schedule and sources ─────────────────────────────────────────────────

func (s *Server) runPluginSchedule(ctx context.Context, c pluginCred) {
	m := c.manifest
	if m.Server == nil || m.Server.Every == "" {
		return
	}
	minutes, err := plugins.ParseEvery(m.Server.Every)
	if err != nil {
		return
	}
	every := time.Duration(minutes) * time.Minute
	st := &s.psv
	key := c.plugin.ID + "|" + c.ns
	st.mu.Lock()
	if st.schedule == nil {
		st.schedule = map[string]time.Time{}
	}
	last, seen := st.schedule[key]
	due := !seen || time.Since(last) >= every
	if due {
		st.schedule[key] = time.Now()
	}
	st.mu.Unlock()
	if !due {
		return
	}
	if _, err := s.runPluginHook(ctx, c, "onSchedule", nil, pluginScheduleBudget, "onSchedule"); err != nil && !errors.Is(err, errNoHook) {
		s.Log.Debug("plugin onSchedule", "plugin", c.plugin.ID, "err", err)
	}
}

// refreshPluginSources fetches each of the plugin's sources that is due.
// A source's host must be granted (net:<host>, which the install screen
// shows as enforced); the fetch is the share page's guarded one.
func (s *Server) refreshPluginSources(ctx context.Context, c pluginCred, force bool) {
	m := c.manifest
	if len(m.Sources) == 0 {
		return
	}
	st := &s.psv
	for _, src := range m.Sources {
		k := c.plugin.ID + "|" + src.Key
		st.mu.Lock()
		if st.srcRun == nil {
			st.srcRun, st.sources = map[string]time.Time{}, map[string]*sourceResult{}
		}
		due := force || time.Since(st.srcRun[k]) >= src.Interval()
		if due {
			st.srcRun[k] = time.Now()
		}
		fetcher := st.fetcher
		st.mu.Unlock()
		if !due {
			continue
		}
		var res *sourceResult
		host := sourceHost(src.URL)
		if !c.has(plugins.CapNet + host) {
			res = &sourceResult{Error: "host not granted: net:" + host, FetchedAt: time.Now().Unix()}
		} else {
			res = s.fetchPluginSource(ctx, fetcher, c.plugin.ID, src)
		}
		st.mu.Lock()
		prev := st.sources[k]
		if !res.OK && prev != nil && prev.OK {
			res.Value = prev.Value
		}
		st.sources[k] = res
		st.mu.Unlock()
	}
}

func (s *Server) fetchPluginSource(ctx context.Context, f *sourceFetcher, pluginID string, src pages.SourceSpec) *sourceResult {
	var secrets []string
	lookup := func(name string) (string, bool) {
		v, err := s.pluginSecretValue(ctx, pluginID, name)
		if err != nil {
			return "", false
		}
		secrets = append(secrets, v)
		return v, true
	}
	headers := map[string]string{}
	for name, value := range src.Headers {
		expanded, err := pages.ExpandSecrets(value, lookup)
		if err != nil {
			return &sourceResult{Error: err.Error(), FetchedAt: time.Now().Unix()}
		}
		headers[name] = expanded
	}
	if f == nil {
		f = &sourceFetcher{}
	}
	res := f.fetch(ctx, src, headers)
	res.Error = redactSecrets(res.Error, secrets)
	return res
}

func (s *Server) pluginSourceResults(c pluginCred) map[string]*sourceResult {
	st := &s.psv
	st.mu.Lock()
	defer st.mu.Unlock()
	out := map[string]*sourceResult{}
	for _, src := range c.manifest.Sources {
		if r, ok := st.sources[c.plugin.ID+"|"+src.Key]; ok {
			out[src.Key] = r
		} else {
			out[src.Key] = &sourceResult{Error: "not fetched yet"}
		}
	}
	return out
}

// ─── the runtime ──────────────────────────────────────────────────────────

func (s *Server) pluginProgram(ctx context.Context, c pluginCred) (*goja.Program, error) {
	m := c.manifest
	if m.Server == nil {
		return nil, errors.New("this plugin has no server.js")
	}
	var src []byte
	var key string
	if c.ns == store.PageDataDraft {
		f, err := plugins.ReadFile(c.plugin.SourceDir, m.Server.Entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Server.Entry, err)
		}
		sum := sha256.Sum256(f.Data)
		src, key = f.Data, c.plugin.ID+"|draft|"+hex.EncodeToString(sum[:8])
	} else {
		_, data, err := s.DB.PluginFileData(ctx, c.plugin.ID, c.plugin.InstalledVersion, m.Server.Entry)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Server.Entry, err)
		}
		src, key = data, fmt.Sprintf("%s|v%d", c.plugin.ID, c.plugin.InstalledVersion)
	}
	st := &s.psv
	st.mu.Lock()
	cs, ok := st.programs[key]
	st.mu.Unlock()
	if !ok {
		cs.program, cs.err = goja.Compile(m.Server.Entry, string(src), true)
		st.mu.Lock()
		if st.programs == nil || len(st.programs) >= pluginProgramEntries {
			st.programs = map[string]compiledServer{}
		}
		st.programs[key] = cs
		st.mu.Unlock()
	}
	return cs.program, cs.err
}

func (s *Server) pluginLog(id, level, text string) {
	if len(text) > pluginLogLineMax {
		text = strings.ToValidUTF8(text[:pluginLogLineMax], "") + "…"
	}
	st := &s.psv
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.logs == nil {
		st.logs = map[string][]serverLogLine{}
	}
	lines := append(st.logs[id], serverLogLine{At: time.Now().Unix(), Level: level, Text: text})
	if len(lines) > pluginLogLines {
		lines = append([]serverLogLine(nil), lines[len(lines)-pluginLogLines:]...)
	}
	st.logs[id] = lines
}

// flushPluginLog writes a plugin's log to .vibepanel/server.log in its draft
// directory, where the agent building it reads it.
func (s *Server) flushPluginLog(c pluginCred) {
	if c.plugin.SourceDir == "" || c.ns != store.PageDataDraft {
		return
	}
	st := &s.psv
	st.mu.Lock()
	var b strings.Builder
	for _, l := range st.logs[c.plugin.ID] {
		fmt.Fprintf(&b, "%s %s %s\n", time.Unix(l.At, 0).UTC().Format(time.RFC3339), l.Level, l.Text)
	}
	st.mu.Unlock()
	_ = pages.WriteMeta(c.plugin.SourceDir, "server.log", []byte(b.String()))
}

// runPluginHook runs one hook in a fresh runtime and commits its data writes
// if, and only if, it returns normally inside its budget with a result under
// the cap.
func (s *Server) runPluginHook(ctx context.Context, c pluginCred, hook string, args []any, budget time.Duration, what string) (any, error) {
	prog, err := s.pluginProgram(ctx, c)
	defer s.flushPluginLog(c)
	if err != nil {
		s.pluginLog(c.plugin.ID, "error", "compile: "+err.Error())
		return nil, dataErrorf("server.js does not compile: %s", firstLine(err.Error()))
	}
	vm := goja.New()
	vm.SetMaxCallStackSize(256)
	timer := time.AfterFunc(budget, func() {
		vm.Interrupt(fmt.Sprintf("%s ran past its %v budget", what, budget))
	})
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { vm.Interrupt("the request ended") })
	defer stop()

	failed := func(err error) error {
		msg := jsError(err)
		s.pluginLog(c.plugin.ID, "error", what+": "+msg)
		return dataErrorf("server.js %s failed: %s", what, firstLine(msg))
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, failed(err)
	}
	fn, ok := goja.AssertFunction(vm.Get(hook))
	if !ok {
		return nil, errNoHook
	}
	jsArgs := make([]goja.Value, 0, len(args)+1)
	for _, a := range args {
		jsArgs = append(jsArgs, vm.ToValue(jsonRoundTrip(a)))
	}
	obj, ops, err := s.pluginCtx(ctx, vm, c, budget)
	if err != nil {
		return nil, err
	}
	jsArgs = append(jsArgs, obj)

	out, err := fn(goja.Undefined(), jsArgs...)
	if err != nil {
		return nil, failed(err)
	}
	var result any
	if out != nil && !goja.IsUndefined(out) && !goja.IsNull(out) {
		raw, jerr := json.Marshal(out.Export())
		if jerr != nil {
			s.pluginLog(c.plugin.ID, "error", what+": its result is not JSON")
			return nil, dataErrorf("server.js %s returned something that is not JSON", what)
		}
		if len(raw) > pages.MaxServerResult {
			s.pluginLog(c.plugin.ID, "error", fmt.Sprintf("%s: returned %d bytes, over the %d KiB cap", what, len(raw), pages.MaxServerResult>>10))
			return nil, dataErrorf("server.js %s returned more than %d KiB", what, pages.MaxServerResult>>10)
		}
		_ = json.Unmarshal(raw, &result)
	}
	if len(*ops) > 0 {
		rows, err := s.DB.PluginData(ctx, c.plugin.ID, c.ns)
		if err != nil {
			return nil, err
		}
		pm := pages.Manifest{Data: c.manifest.Data}
		_, batch, err := applyDataOps(pm, rows, *ops, false, true)
		if err != nil {
			s.pluginLog(c.plugin.ID, "error", what+": "+err.Error())
			return nil, err
		}
		if len(batch) > 0 {
			if err := s.DB.WritePluginData(ctx, c.plugin.ID, c.ns, batch, "server.js", pages.MaxDataBytes, pages.MaxDataKeys); err != nil {
				s.pluginLog(c.plugin.ID, "error", what+": "+err.Error())
				return nil, err
			}
			s.bumpPluginWatchers()
		}
	}
	return result, nil
}

// pluginCtx builds the ctx a hook gets: the members every service has, and
// one more per granted capability. Data changes go to a working copy and a
// list of ops, committed by runPluginHook.
func (s *Server) pluginCtx(ctx context.Context, vm *goja.Runtime, c pluginCred, budget time.Duration) (*goja.Object, *[]pageDataOp, error) {
	rows, err := s.DB.PluginData(ctx, c.plugin.ID, c.ns)
	if err != nil {
		return nil, nil, err
	}
	pm := pages.Manifest{Data: c.manifest.Data}
	working, _ := resolvePageData(pm, rows, true)
	ops := &[]pageDataOp{}
	throw := func(msg string) { panic(vm.NewTypeError(msg)) }
	pid := c.plugin.ID

	record := func(op pageDataOp) {
		spec := c.manifest.Data[op.Key]
		if spec == nil {
			throw("data." + op.Key + " is not declared")
		}
		switch op.Kind {
		case "set":
			if spec.Type == pages.DataLog {
				throw("data." + op.Key + " is a log: append to it")
			}
			clean, err := spec.Check(op.Value, false)
			if err != nil {
				throw("data." + op.Key + " " + err.Error())
			}
			working[op.Key] = clean
		case "increment":
			if spec.Type != pages.DataCounter {
				throw("data." + op.Key + " is not a counter")
			}
			n, _ := working[op.Key].(float64)
			if n += op.By; n < 0 {
				n = 0
			}
			working[op.Key] = n
		case "append":
			if spec.Type != pages.DataLog {
				throw("data." + op.Key + " is not a log")
			}
			entries, _ := working[op.Key].([]any)
			working[op.Key] = append(append([]any{}, entries...), pages.NewLogEntry(op.Item, time.Now().Unix()))
		case "reset":
			working[op.Key] = spec.Zero()
		}
		*ops = append(*ops, op)
	}

	data := vm.NewObject()
	_ = data.Set("get", func(key string) goja.Value {
		v, ok := working[key]
		if !ok {
			return goja.Undefined()
		}
		return vm.ToValue(jsonRoundTrip(v))
	})
	_ = data.Set("set", func(key string, value goja.Value) {
		record(pageDataOp{Kind: "set", Key: key, Value: exportJSON(value)})
	})
	_ = data.Set("increment", func(key string, by goja.Value) {
		n := 1.0
		if by != nil && !goja.IsUndefined(by) {
			n = by.ToFloat()
		}
		record(pageDataOp{Kind: "increment", Key: key, By: n})
	})
	_ = data.Set("append", func(key string, item goja.Value) {
		fields, ok := exportJSON(item).(map[string]any)
		if !ok {
			throw("data." + key + ": append takes an object")
		}
		record(pageDataOp{Kind: "append", Key: key, Item: fields})
	})
	_ = data.Set("reset", func(key string) { record(pageDataOp{Kind: "reset", Key: key}) })

	obj := vm.NewObject()
	_ = obj.Set("data", data)
	settings, _ := s.pluginSettingsBody(ctx, pid, c.manifest)
	_ = obj.Set("settings", vm.ToValue(jsonRoundTrip(settings.Values)))
	_ = obj.Set("secret", func(name string) goja.Value {
		if !contains(c.manifest.SecretNames(), name) {
			throw("secret " + name + " is not one this plugin declares")
		}
		v, err := s.pluginSecretValue(ctx, pid, name)
		if err != nil {
			return goja.Undefined()
		}
		return vm.ToValue(v)
	})
	_ = obj.Set("sources", vm.ToValue(jsonRoundTrip(s.pluginSourceResults(c))))
	_ = obj.Set("now", func() goja.Value {
		d, _ := vm.New(vm.Get("Date"), vm.ToValue(time.Now().UnixMilli()))
		return d
	})
	_ = obj.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			if _, isString := a.Export().(string); !isString {
				if b, err := json.Marshal(a.Export()); err == nil {
					parts = append(parts, string(b))
					continue
				}
			}
			parts = append(parts, a.String())
		}
		s.pluginLog(pid, "info", strings.Join(parts, " "))
		return goja.Undefined()
	})
	_ = obj.Set("caps", vm.ToValue(s.pluginCapsList(c)))

	// ctx.fetch: only https, only a host with net:<host> granted, through the
	// share page's guarded fetcher (resolved address checked, no redirects,
	// bounded), within what is left of the budget.
	hasNet := false
	for cap := range c.caps {
		if strings.HasPrefix(cap, plugins.CapNet) {
			hasNet = true
		}
	}
	if hasNet {
		_ = obj.Set("fetch", func(rawURL string, opts goja.Value) goja.Value {
			u, err := url.Parse(rawURL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				throw("fetch: only https://host/… is fetched")
			}
			if !c.has(plugins.CapNet + u.Host) {
				throw("fetch: this plugin was not granted net:" + u.Host)
			}
			spec := pages.SourceSpec{Key: "fetch", URL: rawURL, Every: "1m", Headers: map[string]string{}}
			timeout := pluginFetchMax
			if budget < timeout {
				timeout = budget
			}
			spec.Timeout = timeout.String()
			if o, ok := exportJSON(opts).(map[string]any); ok {
				if h, ok := o["headers"].(map[string]any); ok {
					for k, v := range h {
						if sv, ok := v.(string); ok {
							spec.Headers[k] = sv
						}
					}
				}
			}
			st := &s.psv
			st.mu.Lock()
			fetcher := st.fetcher
			st.mu.Unlock()
			res := s.fetchPluginSource(ctx, fetcher, pid, spec)
			return vm.ToValue(jsonRoundTrip(res))
		})
	}

	if c.has(plugins.CapReadPanel) || c.has(plugins.CapReadPaths) {
		panel := vm.NewObject()
		_ = panel.Set("view", func() goja.Value {
			v, err := s.buildPluginView(ctx, c)
			if err != nil {
				throw("panel.view: " + err.Error())
			}
			return vm.ToValue(jsonRoundTrip(v))
		})
		_ = obj.Set("panel", panel)
	}
	if c.has(plugins.CapReadNotes) || c.has(plugins.CapWriteNotes) {
		notes := vm.NewObject()
		if c.has(plugins.CapReadNotes) {
			_ = notes.Set("get", func(h string) goja.Value {
				p, ok, err := s.pluginProjectByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("notes.get: no such project")
				}
				n, err := s.DB.GetNote(ctx, p.ID)
				if err != nil {
					throw("notes.get: " + err.Error())
				}
				return vm.ToValue(jsonRoundTrip(pluginNote{ProjectID: h, Content: n.Content, Rev: n.Rev, UpdatedAt: n.UpdatedAt}))
			})
		}
		if c.has(plugins.CapWriteNotes) {
			_ = notes.Set("set", func(h, content string) goja.Value {
				p, ok, err := s.pluginProjectByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("notes.set: no such project")
				}
				if len(content) > maxNoteBytes {
					throw("notes.set: the note is too long")
				}
				n, err := s.DB.SetNote(ctx, p.ID, content)
				if err != nil {
					throw("notes.set: " + err.Error())
				}
				s.notifyPanel(p.ID, "note")
				return vm.ToValue(jsonRoundTrip(pluginNote{ProjectID: h, Content: n.Content, Rev: n.Rev, UpdatedAt: n.UpdatedAt}))
			})
		}
		_ = obj.Set("notes", notes)
	}
	if c.has(plugins.CapReadTodos) || c.has(plugins.CapWriteTodos) {
		todos := vm.NewObject()
		if c.has(plugins.CapReadTodos) {
			_ = todos.Set("list", func(h string) goja.Value {
				p, ok, err := s.pluginProjectByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("todos.list: no such project")
				}
				list, err := s.DB.ListTodos(ctx, p.ID)
				if err != nil {
					throw("todos.list: " + err.Error())
				}
				out := make([]pluginTodo, 0, len(list))
				for _, t := range list {
					out = append(out, s.pluginTodoRow(ctx, pid, t))
				}
				return vm.ToValue(jsonRoundTrip(out))
			})
		}
		if c.has(plugins.CapWriteTodos) {
			_ = todos.Set("add", func(h, text string) goja.Value {
				p, ok, err := s.pluginProjectByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("todos.add: no such project")
				}
				text = strings.TrimSpace(text)
				if text == "" || len(text) > maxTodoBytes {
					throw("todos.add: text is one line of at most 2000 bytes")
				}
				t, err := s.DB.CreateTodo(ctx, id.New(), p.ID, text)
				if err != nil {
					throw("todos.add: " + err.Error())
				}
				s.notifyPanel(p.ID, "todos")
				return vm.ToValue(jsonRoundTrip(s.pluginTodoRow(ctx, pid, t)))
			})
			_ = todos.Set("set", func(h string, patch goja.Value) goja.Value {
				t, ok, err := s.pluginTodoByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("todos.set: no such item")
				}
				fields, _ := exportJSON(patch).(map[string]any)
				if text, ok := fields["text"].(string); ok {
					text = strings.TrimSpace(text)
					if text == "" || len(text) > maxTodoBytes {
						throw("todos.set: text is one line of at most 2000 bytes")
					}
					if err := s.DB.SetTodoText(ctx, t.ID, text); err != nil {
						throw("todos.set: " + err.Error())
					}
				}
				if done, ok := fields["done"].(bool); ok {
					if err := s.DB.SetTodoDone(ctx, t.ID, done); err != nil {
						throw("todos.set: " + err.Error())
					}
				}
				t, _ = s.DB.GetTodo(ctx, t.ID)
				s.notifyPanel(t.ProjectID, "todos")
				return vm.ToValue(jsonRoundTrip(s.pluginTodoRow(ctx, pid, t)))
			})
			_ = todos.Set("remove", func(h string) goja.Value {
				t, ok, err := s.pluginTodoByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("todos.remove: no such item")
				}
				if err := s.DB.DeleteTodo(ctx, t.ID); err != nil {
					throw("todos.remove: " + err.Error())
				}
				s.notifyPanel(t.ProjectID, "todos")
				return goja.Undefined()
			})
		}
		_ = obj.Set("todos", todos)
	}
	if c.has(plugins.CapWriteState) || c.has(plugins.CapReadTerminal) {
		sessions := vm.NewObject()
		if c.has(plugins.CapWriteState) {
			_ = sessions.Set("state", func(h, state string) goja.Value {
				sess, ok, err := s.pluginSessionByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("sessions.state: no such session")
				}
				st := session.State(state)
				if !st.Valid() {
					throw("sessions.state: unknown state " + state)
				}
				if s.Detector != nil {
					s.Detector.SetManual(sess.ID, st, time.Now())
				}
				if err := s.setSessionStateByID(ctx, sess.ID, st, session.SourceManual); err != nil {
					throw("sessions.state: " + err.Error())
				}
				s.notifyState()
				return vm.ToValue(state)
			})
		}
		if c.has(plugins.CapReadTerminal) {
			_ = sessions.Set("screen", func(h string) goja.Value {
				sess, ok, err := s.pluginSessionByHandle(ctx, pid, h)
				if err != nil || !ok {
					throw("sessions.screen: no such session")
				}
				text, err := s.Tmux.Screen(ctx, sess.TmuxName, false)
				if err != nil {
					throw("sessions.screen: the pane cannot be read right now")
				}
				return vm.ToValue(text)
			})
		}
		_ = obj.Set("sessions", sessions)
	}
	return obj, ops, nil
}

// ─── routes: the plugin's own, two doors ──────────────────────────────────

// pluginRouteRequest is what a route handler receives.
type pluginRouteRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   any               `json:"body"`
	Caller string            `json:"caller"`
}

// callPluginRoute runs the handler server.routes names for method and path,
// four at a time per plugin. 404 for a route the manifest does not declare.
func (s *Server) callPluginRoute(w http.ResponseWriter, r *http.Request, c pluginCred, rel, caller string) {
	m := c.manifest
	if m.Server == nil {
		writeErr(w, http.StatusNotFound, "this plugin has no server.js")
		return
	}
	rel = "/" + strings.Trim(rel, "/")
	fn, ok := m.Server.Routes[r.Method+" "+rel]
	if !ok {
		writeErr(w, http.StatusNotFound, "this plugin declares no route "+r.Method+" "+rel)
		return
	}
	var body any
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginRouteBody))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "a route's body is at most 64 KiB")
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeErr(w, http.StatusBadRequest, "a route's body is JSON")
				return
			}
		}
	}
	query := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	// Four at a time per plugin; a fifth waits, up to the budget. A plugin
	// whose handlers take their whole budget is a slow plugin, not a slow
	// panel.
	st := &s.psv
	st.mu.Lock()
	if st.routes == nil {
		st.routes = map[string]chan struct{}{}
	}
	sem, ok := st.routes[c.plugin.ID]
	if !ok {
		sem = make(chan struct{}, pluginRouteParallel)
		st.routes[c.plugin.ID] = sem
	}
	st.mu.Unlock()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-time.After(pluginRouteBudget):
		writeErr(w, http.StatusTooManyRequests, "this plugin's routes are busy")
		return
	case <-r.Context().Done():
		return
	}
	req := pluginRouteRequest{Method: r.Method, Path: rel, Query: query, Body: body, Caller: caller}
	result, err := s.runPluginHook(r.Context(), c, fn, []any{req}, pluginRouteBudget, "route "+r.Method+" "+rel)
	if errors.Is(err, errNoHook) {
		writeErr(w, http.StatusNotFound, "server.js does not define "+fn)
		return
	}
	var de dataError
	if errors.As(err, &de) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// handlePluginFrameRoute is the frame's door: /api/plugin/{cred}/v1/x/*.
func (s *Server) handlePluginFrameRoute(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	s.callPluginRoute(w, r, c, chi.URLParam(r, "*"), "frame")
}

// registerPluginExtRoutes is the owner's door: /api/ext/{pluginID}/*, under
// the session like every settings route.
func (s *Server) registerPluginExtRoutes(r chi.Router) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r.Method(method, "/ext/{pluginID}/*", http.HandlerFunc(s.handlePluginExtRoute))
	}
}

func (s *Server) handlePluginExtRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	c, ok, err := s.pluginServiceCred(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusConflict, "this plugin is not enabled")
		return
	}
	s.callPluginRoute(w, r, c, chi.URLParam(r, "*"), "owner")
}

// ─── inbound: the internet calls, under a secret ──────────────────────────

// registerPluginInboundRoute is the open door: POST /api/plugin-hook/{id}/{path},
// verified here against the declared secret before anything runs, the
// chat bridge's door with the verification moved into the panel.
func (s *Server) registerPluginInboundRoute(r chi.Router) {
	r.Post("/plugin-hook/{pluginID}/{path}", s.handlePluginInbound)
}

func (s *Server) handlePluginInbound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such route")
		return
	}
	c, ok, err := s.pluginServiceCred(ctx, p)
	if err != nil || !ok || c.manifest.Inbound == nil || c.manifest.Inbound.Path != chi.URLParam(r, "path") {
		writeErr(w, http.StatusNotFound, "no such route")
		return
	}
	// The rate limit before the body: a flood costs a lookup.
	st := &s.psv
	st.mu.Lock()
	if st.inbound == nil {
		st.inbound = map[string][]time.Time{}
	}
	now := time.Now()
	recent := st.inbound[p.ID][:0]
	for _, t := range st.inbound[p.ID] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	over := len(recent) >= pluginInboundRate
	if !over {
		recent = append(recent, now)
	}
	st.inbound[p.ID] = recent
	st.mu.Unlock()
	if over {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginRouteBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "a body is at most 64 KiB")
		return
	}
	secret, err := s.pluginSecretValue(ctx, p.ID, c.manifest.Inbound.Secret)
	if err != nil {
		s.auditFromOutside(ctx, "plugin.inbound_rejected", "", ip, p.ID+": its secret is not set")
		writeErr(w, http.StatusUnauthorized, "not verified")
		return
	}
	if !inboundVerified(r, raw, secret) {
		s.auditFromOutside(ctx, "plugin.inbound_rejected", "", ip, p.ID+": bad signature")
		writeErr(w, http.StatusUnauthorized, "not verified")
		return
	}
	var body any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			body = string(raw)
		}
	}
	headers := map[string]string{}
	for _, k := range []string{"Content-Type", "User-Agent", "X-Event", "X-GitHub-Event", "X-Hub-Signature-256"} {
		if v := r.Header.Get(k); v != "" {
			headers[k] = v
		}
	}
	req := map[string]any{"method": r.Method, "path": "/" + c.manifest.Inbound.Path, "body": body, "headers": headers, "caller": "inbound"}
	result, err := s.runPluginHook(ctx, c, "onInbound", []any{req}, pluginRouteBudget, "onInbound")
	if errors.Is(err, errNoHook) {
		writeErr(w, http.StatusNotFound, "server.js does not define onInbound")
		return
	}
	var de dataError
	if errors.As(err, &de) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// inboundVerified accepts either of the two ways a caller can prove it holds
// the secret: `Authorization: Bearer <secret>`, or `X-Signature-256:
// sha256=<hex HMAC of the body>` (GitHub's shape, so a repository webhook
// can point here without a shim).
func inboundVerified(r *http.Request, body []byte, secret string) bool {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return hmac.Equal([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(secret))
	}
	sig := r.Header.Get("X-Signature-256")
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256")
	}
	sig = strings.TrimPrefix(sig, "sha256=")
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.ToLower(sig)))
}

// ─── the owner's view of a service ────────────────────────────────────────

func (s *Server) registerPluginServiceRoutes(r chi.Router) {
	r.Get("/settings/plugins/{pluginID}/server/log", s.handlePluginServerLog)
	r.Get("/settings/plugins/{pluginID}/sources", s.handlePluginSources)
}

func (s *Server) handlePluginServerLog(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.PluginByID(r.Context(), chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	st := &s.psv
	st.mu.Lock()
	lines := append([]serverLogLine{}, st.logs[p.ID]...)
	dropped := 0
	if w, ok := st.workers[p.ID]; ok {
		dropped = w.dropped
	}
	st.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "dropped": dropped})
}

// pluginSourceRow is a source as the owner sees it: no secret values.
type pluginSourceRow struct {
	Key       string `json:"key"`
	URL       string `json:"url"`
	Host      string `json:"host"`
	Granted   bool   `json:"granted"`
	Every     string `json:"every"`
	OK        bool   `json:"ok"`
	FetchedAt int64  `json:"fetchedAt"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
}

func (s *Server) handlePluginSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	c, ok, err := s.pluginServiceCred(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := []pluginSourceRow{}
	if ok {
		results := s.pluginSourceResults(c)
		for _, src := range c.manifest.Sources {
			row := pluginSourceRow{Key: src.Key, URL: src.URL, Host: sourceHost(src.URL),
				Granted: c.has(plugins.CapNet + sourceHost(src.URL)), Every: src.Every}
			if res := results[src.Key]; res != nil {
				row.OK, row.FetchedAt, row.Status, row.Error = res.OK, res.FetchedAt, res.Status, res.Error
			}
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, http.StatusOK, out)
}
