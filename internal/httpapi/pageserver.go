package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// server.js: code a page's owner writes, run inside the panel in a JavaScript
// sandbox. docs/page-backend.md §5.
//
// What the sandbox is for, said plainly because it is easy to overstate: this
// is the owner's code, published with their page, and a visitor can make it
// run only through an action the page declared, with a payload checked
// against that action's schema. The sandbox is there so a mistake -- a loop
// that never ends, a result the size of a book -- costs one failed call and
// not the panel: no modules, no files, no network, no timers, a fresh runtime
// every call, a time budget enforced by interrupting it, and a cap on what it
// returns. It does not bound memory (goja cannot), which is why code visitors
// did not write is the only code that runs here.

// Time budgets per hook, and the log's length.
const (
	transformBudget   = 50 * time.Millisecond
	scheduleBudget    = 500 * time.Millisecond
	adminBudget       = 500 * time.Millisecond
	visitorBudget     = 200 * time.Millisecond
	serverLogLines    = 200
	serverLogLineMax  = 1000
	transformMemoTTL  = 5 * time.Second
	serverMemoEntries = 512
)

type serverState struct {
	mu        sync.Mutex
	programs  map[string]compiledServer
	logs      map[string][]serverLogLine
	transform map[string]cachedTransform
	schedule  map[string]time.Time
	// quiet is the CLI, which prints its log instead of writing the panel's.
	quiet bool
}

type compiledServer struct {
	program *goja.Program
	err     error
}

type cachedTransform struct {
	at    time.Time
	value any
}

// serverLogLine is one line of a page's server log.
type serverLogLine struct {
	At    int64  `json:"at"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// errNoHook is server.js not defining the hook asked for, which for transform
// and onSchedule is not a failure.
var errNoHook = errors.New("server.js does not define this hook")

// serverProgram compiles server.js for a namespace -- the published version's
// for live, the draft directory's for draft -- and returns it with a key that
// names that exact code. A compiled program is reused until the code changes;
// a runtime never is.
func (s *Server) serverProgram(ctx context.Context, page store.SharePage, ns string, m pages.Manifest) (*goja.Program, string, error) {
	if m.Server == nil {
		return nil, "", errors.New("this page has no server.js")
	}
	var src []byte
	var key string
	if ns == store.PageDataDraft {
		f, err := pages.ReadFile(page.SourceDir, m.Server.Entry)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", m.Server.Entry, err)
		}
		sum := sha256.Sum256(f.Data)
		src, key = f.Data, page.ID+"|draft|"+hex.EncodeToString(sum[:8])
	} else {
		_, data, err := s.DB.SharePageFileData(ctx, page.ID, page.PublishedVersion, m.Server.Entry)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", m.Server.Entry, err)
		}
		src, key = data, page.ID+"|v"+strconv.Itoa(page.PublishedVersion)
	}
	st := &s.pb.server
	st.mu.Lock()
	c, ok := st.programs[key]
	st.mu.Unlock()
	if !ok {
		c.program, c.err = goja.Compile(m.Server.Entry, string(src), true)
		st.mu.Lock()
		if st.programs == nil || len(st.programs) >= serverMemoEntries {
			st.programs = map[string]compiledServer{}
		}
		st.programs[key] = c
		st.mu.Unlock()
	}
	return c.program, key, c.err
}

// serverLog appends a line to a page's log, in memory. flushServerLog writes
// it out, once per call, so a hook that logs in a loop writes the file once.
func (s *Server) serverLog(pageID, level, text string) {
	if len(text) > serverLogLineMax {
		text = strings.ToValidUTF8(text[:serverLogLineMax], "") + "…"
	}
	st := &s.pb.server
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.logs == nil {
		st.logs = map[string][]serverLogLine{}
	}
	lines := append(st.logs[pageID], serverLogLine{At: time.Now().Unix(), Level: level, Text: text})
	if len(lines) > serverLogLines {
		lines = append([]serverLogLine(nil), lines[len(lines)-serverLogLines:]...)
	}
	st.logs[pageID] = lines
}

// flushServerLog writes a page's log to .vibepanel/server.log in its draft
// directory, where the agent building the page reads it.
func (s *Server) flushServerLog(page store.SharePage) {
	if page.SourceDir == "" || s.pb.server.quiet {
		return
	}
	st := &s.pb.server
	st.mu.Lock()
	var b strings.Builder
	for _, l := range st.logs[page.ID] {
		fmt.Fprintf(&b, "%s %s %s\n", time.Unix(l.At, 0).UTC().Format(time.RFC3339), l.Level, l.Text)
	}
	st.mu.Unlock()
	_ = pages.WriteMeta(page.SourceDir, "server.log", []byte(b.String()))
}

// serverCall is one call into server.js.
type serverCall struct {
	page   store.SharePage
	ns     string
	m      pages.Manifest
	hook   string
	args   []any
	budget time.Duration
	// noCtx is transform: it gets its input and nothing else, because it runs
	// on every reading of a snapshot, a visitor's included, and a read must
	// not be able to write.
	noCtx bool
	// visitor is ctx.visitor, set for onVisitorAction only; writes is then
	// the only keys ctx.data.set may change.
	visitor map[string]any
	writes  map[string]bool
	by      string
}

// runServer runs one hook in a fresh runtime and commits its data writes if,
// and only if, it returns normally inside its budget with a result under the
// cap. Writes are buffered against a working copy until then, so a hook that
// throws halfway has changed nothing.
func (s *Server) runServer(ctx context.Context, c serverCall) (any, error) {
	prog, _, err := s.serverProgram(ctx, c.page, c.ns, c.m)
	defer s.flushServerLog(c.page)
	if err != nil {
		s.serverLog(c.page.ID, "error", "compile: "+err.Error())
		return nil, dataErrorf("server.js does not compile: %s", firstLine(err.Error()))
	}

	vm := goja.New()
	vm.SetMaxCallStackSize(256)
	timer := time.AfterFunc(c.budget, func() {
		vm.Interrupt(fmt.Sprintf("%s ran past its %v budget", c.hook, c.budget))
	})
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { vm.Interrupt("the request ended") })
	defer stop()

	failed := func(err error) error {
		msg := jsError(err)
		s.serverLog(c.page.ID, "error", c.hook+": "+msg)
		return dataErrorf("server.js %s failed: %s", c.hook, firstLine(msg))
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, failed(err)
	}
	fn, ok := goja.AssertFunction(vm.Get(c.hook))
	if !ok {
		return nil, errNoHook
	}
	args := make([]goja.Value, 0, len(c.args)+1)
	for _, a := range c.args {
		args = append(args, vm.ToValue(jsonRoundTrip(a)))
	}
	var ops *[]pageDataOp
	if !c.noCtx {
		obj, buffered, err := s.serverCtx(ctx, vm, c)
		if err != nil {
			return nil, err
		}
		args, ops = append(args, obj), buffered
	}

	out, err := fn(goja.Undefined(), args...)
	if err != nil {
		return nil, failed(err)
	}
	result, err := s.serverResult(c, out)
	if err != nil {
		return nil, err
	}
	if ops != nil && len(*ops) > 0 {
		if _, err := s.applyPageData(ctx, c.page.ID, c.ns, c.m, *ops, c.by, false, true); err != nil {
			s.serverLog(c.page.ID, "error", c.hook+": "+err.Error())
			return nil, err
		}
	}
	return result, nil
}

// serverResult checks a hook's return value: JSON, at most 64 KiB encoded.
func (s *Server) serverResult(c serverCall, out goja.Value) (any, error) {
	if out == nil || goja.IsUndefined(out) || goja.IsNull(out) {
		return nil, nil
	}
	raw, err := json.Marshal(out.Export())
	if err != nil {
		s.serverLog(c.page.ID, "error", c.hook+": its result is not JSON")
		return nil, dataErrorf("server.js %s returned something that is not JSON", c.hook)
	}
	if len(raw) > pages.MaxServerResult {
		s.serverLog(c.page.ID, "error", fmt.Sprintf("%s: returned %d bytes, over the %d KiB cap", c.hook, len(raw), pages.MaxServerResult>>10))
		return nil, dataErrorf("server.js %s returned more than %d KiB", c.hook, pages.MaxServerResult>>10)
	}
	var result any
	_ = json.Unmarshal(raw, &result)
	return result, nil
}

// serverCtx builds the ctx a hook gets. Its data methods apply each change
// to a working copy -- so a get after a set reads what was set -- and record
// it; nothing reaches the store until runServer commits, where every change
// is checked again by the same code the settings routes use.
func (s *Server) serverCtx(ctx context.Context, vm *goja.Runtime, c serverCall) (*goja.Object, *[]pageDataOp, error) {
	rows, err := s.DB.PageData(ctx, c.page.ID, c.ns)
	if err != nil {
		return nil, nil, err
	}
	working, _ := resolvePageData(c.m, rows, true)
	ops := &[]pageDataOp{}
	throw := func(msg string) { panic(vm.NewTypeError(msg)) }

	record := func(op pageDataOp) {
		spec := c.m.Data[op.Key]
		if spec == nil {
			throw("data." + op.Key + " is not declared")
		}
		switch op.Kind {
		case "set":
			// The guard that makes `writes` mean something: a visitor's call
			// changes counters and logs as its action says, and any other key
			// only when the owner listed it.
			if c.visitor != nil && !c.writes[op.Key] {
				throw("onVisitorAction may set only the keys its action lists in writes, and " + op.Key + " is not one")
			}
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
	_ = obj.Set("sources", vm.ToValue(jsonRoundTrip(s.sourceResults(c.page.ID, c.ns, c.m))))
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
		s.serverLog(c.page.ID, "info", strings.Join(parts, " "))
		return goja.Undefined()
	})
	if c.visitor != nil {
		_ = obj.Set("visitor", vm.ToValue(jsonRoundTrip(c.visitor)))
	}
	return obj, ops, nil
}

// serverTransform is what server.js's transform returned for a snapshot, or
// nil. Memoised by the exact code and input: a wall polling every two seconds
// runs it again only when what it would see has changed.
func (s *Server) serverTransform(ctx context.Context, page store.SharePage, ns string, m pages.Manifest, snap shareSnapshot) any {
	if m.Server == nil {
		return nil
	}
	_, codeKey, err := s.serverProgram(ctx, page, ns, m)
	if err != nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"snapshot": snap, "data": snap.Data, "sources": snap.Sources})
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	key := codeKey + "|" + hex.EncodeToString(sum[:])
	st := &s.pb.server
	st.mu.Lock()
	if c, ok := st.transform[key]; ok && time.Since(c.at) < transformMemoTTL {
		st.mu.Unlock()
		return c.value
	}
	st.mu.Unlock()

	value, err := s.runServer(ctx, serverCall{page: page, ns: ns, m: m, hook: "transform",
		args: []any{json.RawMessage(raw)}, budget: transformBudget, noCtx: true})
	if err != nil {
		value = nil
	}
	st.mu.Lock()
	if st.transform == nil || len(st.transform) >= serverMemoEntries {
		st.transform = map[string]cachedTransform{}
	}
	st.transform[key] = cachedTransform{at: time.Now(), value: value}
	st.mu.Unlock()
	return value
}

// runServerHook runs onVisitorAction or onAdminAction for an action.
func (s *Server) runServerHook(ctx context.Context, page store.SharePage, ns string, m pages.Manifest,
	hook, name string, input, visitor map[string]any, action *pages.ActionSpec, by string) (any, error) {
	c := serverCall{page: page, ns: ns, m: m, hook: hook, args: []any{name, input}, budget: adminBudget, by: by}
	if hook == "onVisitorAction" {
		c.budget, c.visitor, c.writes = visitorBudget, visitor, map[string]bool{}
		if c.visitor == nil {
			c.visitor = map[string]any{}
		}
		for _, k := range action.Writes {
			c.writes[k] = true
		}
	}
	result, err := s.runServer(ctx, c)
	if errors.Is(err, errNoHook) {
		return nil, dataErrorf("server.js does not define %s", hook)
	}
	return result, err
}

// runSchedule runs server.js's onSchedule when it is due. Called from the
// page's watch loop, so it runs only while something is looking.
func (s *Server) runSchedule(ctx context.Context, page store.SharePage, ns string, m pages.Manifest) {
	if m.Server == nil || m.Server.Every == "" {
		return
	}
	every, err := pages.ParseDuration(m.Server.Every)
	if err != nil {
		return
	}
	st := &s.pb.server
	key := page.ID + "|" + ns
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
	if _, err := s.runServer(ctx, serverCall{page: page, ns: ns, m: m, hook: "onSchedule",
		budget: scheduleBudget, by: "server.js"}); err != nil && !errors.Is(err, errNoHook) {
		s.Log.Debug("server.js onSchedule", "page", page.ID, "err", err)
	}
}

// jsonRoundTrip is v as JSON would carry it: plain maps, slices, float64s.
// What a hook receives is never a Go value it could call methods on.
func jsonRoundTrip(v any) any {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return nil
		}
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// exportJSON is a JavaScript value as the JSON it would encode to.
func exportJSON(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) {
		return nil
	}
	return jsonRoundTrip(v.Export())
}

func jsError(err error) string {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return fmt.Sprint(interrupted.Value())
	}
	return err.Error()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + "…"
	}
	return s
}

// handlePageServerLog is GET /api/settings/pages/{pageID}/server/log.
func (s *Server) handlePageServerLog(w http.ResponseWriter, r *http.Request) {
	page, err := s.DB.SharePageByID(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	st := &s.pb.server
	st.mu.Lock()
	lines := append([]serverLogLine{}, st.logs[page.ID]...)
	st.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}
