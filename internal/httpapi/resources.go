package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/resources"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// The resources page and the question the panel asks when memory runs short.
//
// Every read here is of what the governor's own goroutine last produced; the
// only thing a request reads fresh is the process list of each session, and
// that only when the page is open. Nothing on this path asks tmux anything,
// because the moment this page matters most is the moment tmux is slowest to
// answer.

// resourcesPolicyKey is where the policy is stored, as JSON.
const resourcesPolicyKey = "resources.policy"

// resourcesState is the glue between the poller, which knows the sessions,
// and the governor, which runs on its own clock.
type resourcesState struct {
	mu        sync.Mutex
	metas     []resources.SessionMeta
	lastInput map[string]time.Time
	policy    *resources.Policy
	// write serialises changes to the stored policy. Saving a mode and
	// starting a boost each read, change and write the whole policy, and two
	// at once lost one of them.
	write sync.Mutex
}

// frozenNow is the paused sessions, for the snapshot.
func (s *Server) frozenNow() []string {
	if s.Resources == nil {
		return nil
	}
	return s.Resources.Frozen()
}

func (s *Server) registerResourceRoutes(r chi.Router) {
	r.Get("/resources", s.handleResources)
	r.Get("/resources/alert", s.handleResourceAlert)
	r.Put("/resources/policy", s.handlePutResourcePolicy)
	r.Post("/resources/boost", s.handleResourceBoost)
	r.Post("/resources/kill", s.handleResourceKill)
	r.Post("/resources/sessions/{id}/freeze", s.handleResourceFreeze(true))
	r.Post("/resources/sessions/{id}/thaw", s.handleResourceFreeze(false))
	r.Post("/resources/alerts/{id}/snooze", s.handleResourceSnooze)
}

// ResourcePanes is the poller's last tmux listing, as the governor wants it.
//
// Nothing when the listing is old. After the server restarts, pane ids start
// again from %0, and a listing from before it maps a new pane's TMUX_PANE to
// an old session -- which sorted a process into the wrong session's cgroup for
// good, since a process already in a session's leaf is never looked at again.
func (s *Server) ResourcePanes() []resources.Pane {
	s.tmuxListMu.Lock()
	infos := s.tmuxList
	at := s.tmuxListAt
	s.tmuxListMu.Unlock()
	if time.Since(at) > 10*time.Second {
		return nil
	}
	out := make([]resources.Pane, 0, len(infos))
	for _, i := range infos {
		out = append(out, resources.Pane{TmuxName: i.Name, PID: i.PID, ID: i.PaneID})
	}
	return out
}

// ResourceSessions is the poller's last reading of the sessions table.
func (s *Server) ResourceSessions() []resources.SessionMeta {
	s.res.mu.Lock()
	defer s.res.mu.Unlock()
	out := make([]resources.SessionMeta, len(s.res.metas))
	for i, m := range s.res.metas {
		m.LastInput = s.res.lastInput[m.ID]
		out[i] = m
	}
	return out
}

// publishResourceSessions is called by the poller with the rows it just read.
func (s *Server) publishResourceSessions(rows []store.Session) {
	metas := make([]resources.SessionMeta, 0, len(rows))
	for _, row := range rows {
		metas = append(metas, resources.SessionMeta{
			ID: row.ID, TmuxName: row.TmuxName, Title: row.Title, Waiting: row.State == session.StateWaiting,
		})
	}
	s.res.mu.Lock()
	s.res.metas = metas
	for id := range s.res.lastInput {
		found := false
		for _, m := range metas {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			delete(s.res.lastInput, id)
		}
	}
	s.res.mu.Unlock()
}

// noteResourceInput records that somebody typed into a session, which is what
// makes it one of the sessions that keep their CPU when the others compete.
func (s *Server) noteResourceInput(sessionID string) {
	s.res.mu.Lock()
	if s.res.lastInput == nil {
		s.res.lastInput = map[string]time.Time{}
	}
	s.res.lastInput[sessionID] = time.Now()
	s.res.mu.Unlock()
}

// ResourcePolicy reads the stored policy, cached: the governor asks every two
// seconds and the answer changes when somebody presses a button.
func (s *Server) ResourcePolicy(ctx context.Context) resources.Policy {
	s.res.mu.Lock()
	if s.res.policy != nil {
		p := *s.res.policy
		s.res.mu.Unlock()
		return p
	}
	s.res.mu.Unlock()
	raw, err := s.DB.GetSetting(ctx, resourcesPolicyKey, "")
	if err != nil {
		// Not cached. A database that is not answering is this feature's own
		// symptom -- "context canceled" -- and caching the default here
		// switched somebody's Performance, or their Custom with auto off, to
		// Balanced with auto on until the next save.
		s.Log.Warn("reading the resources policy", "err", err)
		return resources.DefaultPolicy()
	}
	p := resources.DefaultPolicy()
	if raw != "" {
		var stored resources.Policy
		if json.Unmarshal([]byte(raw), &stored) == nil && stored.Validate() == nil {
			p = stored
		}
	}
	s.res.mu.Lock()
	s.res.policy = &p
	s.res.mu.Unlock()
	return p
}

func (s *Server) savePolicy(ctx context.Context, p resources.Policy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.DB.SetSetting(ctx, resourcesPolicyKey, string(b)); err != nil {
		return err
	}
	s.res.mu.Lock()
	s.res.policy = &p
	s.res.mu.Unlock()
	return nil
}

// EmitResources tells every viewer the question changed, or what the panel did.
func (s *Server) EmitResources(e resources.Event) {
	if s.Hub == nil {
		return
	}
	payload, err := json.Marshal(struct {
		T string `json:"t"`
		resources.Event
	}{T: ws.MsgResources, Event: e})
	if err != nil {
		return
	}
	// An event, not a snapshot: a question that is replaced by the next
	// snapshot before a browser sees it is a question nobody was asked.
	s.Hub.BroadcastEvent(payload)
}

// AuditResources records a governor action the panel took or was asked to.
//
// On its own goroutine with its own deadline: the governor calls this from
// its tick, and a tick that waits on SQLite is a tick that stops while the
// machine is stalled -- which is when it is needed.
func (s *Server) AuditResources(event, who, detail string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.audit(ctx, event, who, "", detail)
	}()
}

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	if s.Resources == nil {
		writeJSON(w, http.StatusOK, resources.View{
			Supported: false, Isolation: resources.Isolation{State: "none", Reason: resources.NonePlatform},
			Sessions: []resources.SessionView{}, Actions: []resources.Action{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.Resources.View())
}

// handleResourceAlert is the question alone, for a page that has just opened.
// The whole view reads every session's processes out of /proc to answer, and a
// page load does not need any of that.
func (s *Server) handleResourceAlert(w http.ResponseWriter, r *http.Request) {
	var a *resources.Alert
	if s.Resources != nil {
		a = s.Resources.Alert()
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": a})
}

func (s *Server) handlePutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req resources.Policy
	if !decode(w, r, &req) {
		return
	}
	s.res.write.Lock()
	defer s.res.write.Unlock()
	// A boost is its own route, with its own bound. Carried over rather than
	// taken from the body, so saving the mode does not end one or start one.
	req.BoostUntil = s.ResourcePolicy(r.Context()).BoostUntil
	if err := req.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Mode != resources.Custom {
		// The preset's numbers are the preset's; storing whatever the page
		// happened to send beside the mode would show stale custom values the
		// next time somebody picks Custom. Keep the custom ones as they were.
		prev := s.ResourcePolicy(r.Context())
		req.PoolPercent, req.AskPercent, req.AutoAct, req.GraceSeconds =
			prev.PoolPercent, prev.AskPercent, prev.AutoAct, prev.GraceSeconds
	}
	if err := s.savePolicy(r.Context(), req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "resources.policy", u.Username, s.clientIP(r), string(req.Mode))
	}
	s.handleResources(w, r)
}

func (s *Server) handleResourceBoost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Minutes int `json:"minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	d := time.Duration(req.Minutes) * time.Minute
	if req.Minutes < 0 || d > resources.MaxBoost {
		writeErr(w, http.StatusBadRequest, resources.ErrBoostTooLong.Error())
		return
	}
	s.res.write.Lock()
	defer s.res.write.Unlock()
	p := s.ResourcePolicy(r.Context())
	until := time.Now().Add(d)
	p.BoostUntil = until.Unix()
	if req.Minutes == 0 {
		p.BoostUntil = 0
	}
	if err := s.savePolicy(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	who := ""
	if u, ok := currentUserFrom(r); ok {
		who = u.Username
		if req.Minutes == 0 {
			s.audit(r.Context(), "resources.boost_ended", who, s.clientIP(r), "")
		} else {
			s.audit(r.Context(), "resources.boost", who, s.clientIP(r), until.Format(time.RFC3339))
		}
	}
	if s.Resources != nil {
		s.Resources.NoteBoost(who, req.Minutes == 0)
	}
	s.handleResources(w, r)
}

func (s *Server) handleResourceKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionId"`
		PID       int    `json:"pid"`
		Start     uint64 `json:"start"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.Resources == nil {
		writeErr(w, http.StatusNotImplemented, "not available on this platform")
		return
	}
	who := ""
	if u, ok := currentUserFrom(r); ok {
		who = u.Username
	}
	act, err := s.Resources.Kill(req.SessionID, req.PID, req.Start, who)
	if err != nil {
		writeResourceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, act)
}

func (s *Server) handleResourceFreeze(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Resources == nil {
			writeErr(w, http.StatusNotImplemented, "not available on this platform")
			return
		}
		who := ""
		if u, ok := currentUserFrom(r); ok {
			who = u.Username
		}
		act, err := s.Resources.Freeze(chi.URLParam(r, "id"), on, who)
		if err != nil {
			writeResourceErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, act)
	}
}

func (s *Server) handleResourceSnooze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"sessionId"`
		Minutes   int    `json:"minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Minutes < 1 || req.Minutes > 24*60 {
		writeErr(w, http.StatusBadRequest, "minutes must be 1–1440")
		return
	}
	if s.Resources == nil {
		writeErr(w, http.StatusNotImplemented, "not available on this platform")
		return
	}
	if err := s.Resources.Snooze(chi.URLParam(r, "id"), req.SessionID, time.Duration(req.Minutes)*time.Minute); err != nil {
		writeResourceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeResourceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, resources.ErrUnknownSession):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, resources.ErrGone), errors.Is(err, resources.ErrNoAlert):
		writeErr(w, http.StatusGone, err.Error())
	case errors.Is(err, resources.ErrNotInSession):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, resources.ErrNoFreezer):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}
