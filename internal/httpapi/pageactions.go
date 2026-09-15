package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Actions: a screen that writes, and an admin page that does. See
// docs/page-backend.md §6.
//
// This is the one write a share token reaches, and it reaches it through a
// route that exists for nothing else. The interactive flag below is checked by
// this handler and consulted by no other: it does not widen what the token
// is, it decides whether this one route's effect -- on this page's own data --
// runs. That is why it is consistent with red line 8's "narrowed by route,
// never by a flag": the route is the narrowing, and every other route under
// the token still has no write to widen.

// Rate limits on visitor actions that do not come from the manifest.
const (
	linkActionsPerMinute  = 600
	panelActionsPerMinute = 3000
	// rateBookCap bounds the limiter's memory. Past it, a key the book has
	// not seen is refused rather than let through: a flood of fresh addresses
	// is exactly when the limiter must not forget.
	rateBookCap = 20000
)

// rateBook counts calls per key in fixed windows.
type rateBook struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	span  time.Duration
	n     int
}

// allow counts one call against key and reports whether it fits n per span,
// and if not how long until it would.
func (b *rateBook) allow(key string, n int, span time.Duration, now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.windows == nil {
		b.windows = map[string]*rateWindow{}
	}
	w, ok := b.windows[key]
	if ok && now.Sub(w.start) >= w.span {
		w.start, w.n = now, 0
	}
	if !ok {
		if len(b.windows) >= rateBookCap {
			for k, old := range b.windows {
				if now.Sub(old.start) >= old.span {
					delete(b.windows, k)
				}
			}
			if len(b.windows) >= rateBookCap {
				return false, span
			}
		}
		w = &rateWindow{start: now, span: span}
		b.windows[key] = w
	}
	if w.n >= n {
		return false, w.span - now.Sub(w.start)
	}
	w.n++
	return true, 0
}

// actionAudit coalesces share.action rows: one when an action starts being
// used in a window, and one with the count when the window it was used in
// has passed and another call arrives.
type actionAudit struct {
	mu      sync.Mutex
	windows map[string]*auditWindow
}

type auditWindow struct {
	start time.Time
	n     int
	ips   map[string]bool
}

// note records a call and returns the rows to write: the previous window's
// count when it has closed, and a first-of-window row.
func (a *actionAudit) note(key, ip string, now time.Time) (closed *auditWindow, first bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.windows == nil {
		a.windows = map[string]*auditWindow{}
	}
	w, ok := a.windows[key]
	if ok && now.Sub(w.start) >= time.Minute {
		if w.n > 1 {
			closed = w
		}
		ok = false
	}
	if !ok {
		if len(a.windows) > 4096 {
			a.windows = map[string]*auditWindow{}
		}
		w = &auditWindow{start: now, ips: map[string]bool{}}
		a.windows[key] = w
		first = true
	}
	w.n++
	if len(w.ips) < 16 {
		w.ips[ip] = true
	}
	return closed, first
}

func (w *auditWindow) addresses() string {
	out := make([]string, 0, len(w.ips))
	for ip := range w.ips {
		out = append(out, ip)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// actionAnswer is what an action call answers, in every case.
type actionAnswer struct {
	OK         bool   `json:"ok"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func writeAction(w http.ResponseWriter, status int, a actionAnswer) {
	w.Header().Set("Cache-Control", "no-store")
	if a.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(a.RetryAfter))
	}
	writeJSON(w, status, a)
}

// readActionPayload reads a body of at most MaxActionBody as a JSON object;
// an empty body is an empty object.
func readActionPayload(r *http.Request) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, pages.MaxActionBody+1))
	if err != nil {
		return nil, errors.New("could not read the request")
	}
	if len(raw) > pages.MaxActionBody {
		return nil, fmt.Errorf("an action's payload is at most %d bytes", pages.MaxActionBody)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil || dec.More() {
		return nil, errors.New("an action's payload is one JSON object")
	}
	return payload, nil
}

// handleActionPreflight answers the CORS preflight a page's POST needs: its
// origin is opaque, so its request is cross-origin, and the token in the path
// is the capability. Never credentials.
func (s *Server) handleActionPreflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "POST")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// handleVisitorAction runs a visitor action through a share link. The
// conditions are docs/page-backend.md §6's, in the order below.
func (s *Server) handleVisitorAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	sc, ok := shareFrom(r)
	if !ok {
		writeAction(w, http.StatusUnauthorized, actionAnswer{Error: "this link is not valid"})
		return
	}
	link := sc.link
	name := chi.URLParam(r, "name")

	// 2. The link is interactive: a handed-out link the owner switched on, or
	// a preview link (which writes draft data). Never a view link.
	if !(link.Purpose == "" && link.Interactive) && link.Purpose != store.SharePurposePreview {
		writeAction(w, http.StatusForbidden, actionAnswer{Error: "this link does not accept actions"})
		return
	}
	// 3. The panel allows visitor writes at all.
	if !s.visitorWritesAllowed(ctx) {
		writeAction(w, http.StatusForbidden, actionAnswer{Error: "actions are turned off on this panel"})
		return
	}
	// 4. Declared, for visitors, in the version this link draws.
	page, err := s.DB.SharePageByID(ctx, link.PageID)
	if err != nil {
		writeAction(w, http.StatusGone, actionAnswer{Error: "this page no longer exists"})
		return
	}
	ns := store.PageDataLive
	var m pages.Manifest
	if link.Purpose == store.SharePurposePreview {
		ns = store.PageDataDraft
		if m, err = draftManifest(page.SourceDir); err != nil {
			writeAction(w, http.StatusUnprocessableEntity, actionAnswer{Error: err.Error()})
			return
		}
	} else {
		v, verr := s.DB.SharePageVersionByNumber(ctx, page.ID, link.ResolvePageVersion(page.PublishedVersion, now.Unix()))
		if verr != nil {
			writeAction(w, http.StatusGone, actionAnswer{Error: "this page has no published version"})
			return
		}
		m = pages.DecodeStored(v.Manifest)
	}
	action := m.Actions[name]
	if action == nil || !action.VisitorMay() {
		writeAction(w, http.StatusNotFound, actionAnswer{Error: "this page has no action " + strconv.Quote(name)})
		return
	}
	// 5. Rate limits, before the body is read: a flood costs a map lookup.
	ip := s.clientIP(r)
	rate := action.RateOrDefault()
	for _, lim := range []struct {
		key  string
		n    int
		span time.Duration
	}{
		{"a|" + link.ID + "|" + name + "|" + ip, rate.N, rate.Window},
		{"l|" + link.ID, linkActionsPerMinute, time.Minute},
		{"p", panelActionsPerMinute, time.Minute},
	} {
		if okRate, wait := s.pb.rates.allow(lim.key, lim.n, lim.span, now); !okRate {
			secs := int(wait.Seconds() + 0.999)
			if secs < 1 {
				secs = 1
			}
			writeAction(w, http.StatusTooManyRequests, actionAnswer{Error: "too many; try again shortly", RetryAfter: secs})
			return
		}
	}
	// 6. The payload: at most 4 KiB, exactly the declared fields, text held
	// to the visitor's reading.
	payload, err := readActionPayload(r)
	if err != nil {
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: err.Error()})
		return
	}
	input, err := pages.CheckInput(m.InputFields(action), payload, true)
	if err != nil {
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: err.Error()})
		return
	}
	// 7. The effect, with its own limits.
	visitor := map[string]any{"id": shareID(sc.secret, "visitor:"+ip), "link": link.Name}
	result, err := s.runAction(ctx, page, ns, m, name, action, input, "visitor", visitor, true)
	var de dataError
	switch {
	case errors.As(err, &de):
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: de.msg})
		return
	case err != nil:
		s.Log.Warn("visitor action", "page", page.ID, "action", name, "err", err)
		writeAction(w, http.StatusInternalServerError, actionAnswer{Error: "the action could not be saved"})
		return
	}
	if link.Purpose == "" {
		s.pb.actions.add(link.ID, s.today(ctx))
		if closed, first := s.pb.audit.note(link.ID+"|"+name, ip, now); closed != nil || first {
			if closed != nil {
				s.auditFromOutside(ctx, "share.action", "", ip, fmt.Sprintf("%s: %s ran %d times in the minute from %s (%s)",
					link.Name, name, closed.n, closed.start.Format(time.RFC3339), closed.addresses()))
			}
			if first {
				s.auditFromOutside(ctx, "share.action", "", ip, link.Name+": "+name)
			}
		}
	}
	writeAction(w, http.StatusOK, actionAnswer{OK: true, Result: result})
}

// handleAdminAction runs an admin action through a grant.
func (s *Server) handleAdminAction(w http.ResponseWriter, r *http.Request) {
	a, page, ns, m, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	action := m.Actions[name]
	if action == nil || !action.AdminMay() {
		writeAction(w, http.StatusNotFound, actionAnswer{Error: "this page has no admin action " + strconv.Quote(name)})
		return
	}
	payload, err := readActionPayload(r)
	if err != nil {
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: err.Error()})
		return
	}
	input, err := pages.CheckInput(m.InputFields(action), payload, false)
	if err != nil {
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: err.Error()})
		return
	}
	user := s.adminUsername(r.Context(), a)
	result, err := s.runAction(r.Context(), page, ns, m, name, action, input, user, nil, false)
	var de dataError
	switch {
	case errors.As(err, &de):
		writeAction(w, http.StatusBadRequest, actionAnswer{Error: de.msg})
		return
	case err != nil:
		writeAction(w, http.StatusInternalServerError, actionAnswer{Error: "the action could not be saved"})
		return
	}
	s.audit(r.Context(), "page.data_changed", user, s.clientIP(r), page.Name+" "+ns+": action "+name)
	writeAction(w, http.StatusOK, actionAnswer{OK: true, Result: result})
}

// runAction applies an action's effect. visitor is nil for an admin.
func (s *Server) runAction(ctx context.Context, page store.SharePage, ns string, m pages.Manifest, name string,
	action *pages.ActionSpec, input map[string]any, by string, visitor map[string]any, fromVisitor bool) (any, error) {
	key := action.Effect.Key
	var op pageDataOp
	switch action.Effect.Kind {
	case pages.EffectIncrement:
		op = pageDataOp{Kind: "increment", Key: key, By: 1}
	case pages.EffectAppend:
		op = pageDataOp{Kind: "append", Key: key, Item: input}
	case pages.EffectSet:
		if fromVisitor {
			return nil, dataErrorf("a visitor cannot set data")
		}
		op = pageDataOp{Kind: "set", Key: key, Value: input["value"]}
	case pages.EffectServer:
		hook := "onAdminAction"
		if fromVisitor {
			hook = "onVisitorAction"
		}
		return s.runServerHook(ctx, page, ns, m, hook, name, input, visitor, action, by)
	default:
		return nil, dataErrorf("this action has no effect")
	}
	changed, err := s.applyPageData(ctx, page.ID, ns, m, []pageDataOp{op}, by, fromVisitor, false)
	if err != nil {
		return nil, err
	}
	return changed[key], nil
}
