package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A session's own door: /api/me/v1/* (docs/cli.md, `vibepanel me`).
//
// Every session's environment already holds a credential that names it and
// nothing else -- VIBEPANEL_TOKEN, MAC(root hook token, session id), which
// the state reporter has used since report tokens exist. This is that
// credential given a second thing to do: let the agent in the session say
// what it is up to. The shape is the one red lines 8 and 10 describe. The
// token reaches this prefix and nothing else (TestASessionTokenReachesOnly
// TheseRoutes), currentUser does not consult it, and there is no target
// parameter: the session is the one the token was derived for, so an agent
// cannot speak for, read, or rename another session, and cannot reach a
// project, a note, a setting or the owner's anything.
//
// The owner does the same things to any session through /api/sessions/{id}
// (PATCH, and the input and screen routes); the handlers are shared.

const (
	meSessionHeader = "X-Vibepanel-Session"
)

func (s *Server) registerMeRoutes(r chi.Router) {
	// Flat rather than a sub-router, so the route is "/api/me/v1" to the
	// router, the walk that pins it and the documentation alike.
	r.With(s.requireSessionToken).Get("/me/v1", s.handleMe)
	r.With(s.requireSessionToken).Patch("/me/v1", s.handleMePatch)
	r.With(s.requireSessionToken).Post("/me/v1/say", s.handleSessionSay)
	r.With(s.requireSessionToken).Post("/me/v1/notify", s.handleSessionNotify)
	r.With(s.requireSessionToken).Post("/me/v1/send", s.handleSessionAttach)
}

type meKey struct{}

func meFrom(r *http.Request) store.Session {
	row, _ := r.Context().Value(meKey{}).(store.Session)
	return row
}

// requireSessionToken resolves the session the request speaks for: the id
// in the header, the derived token in Authorization, compared in constant
// time against what the panel derives for that id. The root token is not
// accepted here: it names no session, and a hook that holds it predates
// this door.
func (s *Server) requireSessionToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// The same rule every capability surface applies, for the same reason
		// as AUDIT.md #1: an address the operator excluded must not reach a
		// credential-resolving route just because it is new. The callers are
		// sessions on this machine, so loopback unless the operator says
		// otherwise -- exactly what --allow-from is for.
		if s.refuseAddress(ctx, w, r) {
			return
		}
		id := strings.TrimSpace(r.Header.Get(meSessionHeader))
		presented := bearerToken(r)
		if id == "" || presented == "" {
			writeErr(w, http.StatusUnauthorized, "a session's door takes "+meSessionHeader+" and its own VIBEPANEL_TOKEN as a Bearer")
			return
		}
		root, err := s.HookToken(ctx)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "hook token unavailable")
			return
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(hooks.ReportToken(root, id))) != 1 {
			s.auditFromOutside(ctx, "me.rejected", "", s.clientIP(r), "a session token that is not "+id+"'s")
			writeErr(w, http.StatusUnauthorized, "that token is not this session's")
			return
		}
		row, err := s.DB.GetSession(ctx, id)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "no such session")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, meKey{}, row)))
	})
}

// meView restates what a session may know about itself. Not store.Session:
// a field added there is not handed to every agent by default.
type meView struct {
	ID       string   `json:"id"`
	Project  string   `json:"project"`
	Title    string   `json:"title"`
	State    string   `json:"state"`
	Summary  string   `json:"summary"`
	Progress int      `json:"progress"`
	Labels   []string `json:"labels"`
	Pinned   bool     `json:"pinned"`
	Exited   bool     `json:"exited"`
}

func (s *Server) meViewFor(r *http.Request, row store.Session) meView {
	name := ""
	if p, err := s.DB.GetProject(r.Context(), row.ProjectID); err == nil {
		name = p.Name
	}
	return meView{ID: row.ID, Project: name, Title: row.Title, State: string(row.State), Summary: row.Summary,
		Progress: row.Progress, Labels: row.Labels, Pinned: row.Pinned, Exited: row.Exited}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meViewFor(r, meFrom(r)))
}

// mePatch is what a session may change about itself. Why is a line said
// with a state change -- "waiting for your decision on the schema" -- kept
// as a notice in the session's messages, which is what the chat card shows
// as the last thing the session said.
type mePatch struct {
	Title    *string  `json:"title"`
	State    *string  `json:"state"`
	Why      string   `json:"why"`
	Summary  *string  `json:"summary"`
	Progress *int     `json:"progress"`
	Labels   []string `json:"labels"`
}

func (s *Server) handleMePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row := meFrom(r)
	var req mePatch
	if !decode(w, r, &req) {
		return
	}
	if err := s.applySessionInfo(ctx, row, req, "session:"+row.ID, s.clientIP(r)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fresh, err := s.DB.GetSession(ctx, row.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.meViewFor(r, fresh))
}

// applySessionInfo is the one implementation behind the session's door and
// the owner's PATCH: title, state (with a why), summary, progress, labels.
func (s *Server) applySessionInfo(ctx context.Context, row store.Session, req mePatch, by, ip string) error {
	if req.Title != nil {
		if err := s.DB.SetSessionTitle(ctx, row.ID, *req.Title, store.TitleManual); err != nil {
			return err
		}
	}
	if req.Labels != nil {
		clean := []string{}
		for _, l := range req.Labels {
			l = strings.TrimSpace(session.TruncateTitle(l))
			if l == "" {
				continue
			}
			if len([]rune(l)) > store.MaxSessionLabel {
				l = string([]rune(l)[:store.MaxSessionLabel])
			}
			if !contains(clean, l) {
				clean = append(clean, l)
			}
		}
		if len(clean) > store.MaxSessionLabels {
			clean = clean[:store.MaxSessionLabels]
		}
		sort.Strings(clean)
		req.Labels = clean
	}
	if req.Summary != nil {
		trimmed := strings.TrimSpace(*req.Summary)
		if len([]rune(trimmed)) > store.MaxSessionSummary {
			trimmed = string([]rune(trimmed)[:store.MaxSessionSummary])
		}
		req.Summary = &trimmed
	}
	if req.Summary != nil || req.Progress != nil || req.Labels != nil {
		if err := s.DB.SetSessionInfo(ctx, row.ID, req.Summary, req.Progress, req.Labels); err != nil {
			return err
		}
	}
	if req.State != nil {
		st := session.State(*req.State)
		if !st.Valid() {
			return errors.New("unknown state " + *req.State)
		}
		if why := strings.TrimSpace(req.Why); why != "" {
			if len([]rune(why)) > 200 {
				why = string([]rune(why)[:200])
			}
			if m, err := s.DB.AddSessionMessage(ctx, store.SessionMessage{SessionID: row.ID, Kind: store.MessageNotice, Text: why}); err == nil && s.Chat != nil {
				s.Chat.SessionSaid(row, m)
			}
		}
		if st != row.State {
			if err := s.setSessionState(ctx, row, st, session.SourceManual); err != nil {
				return err
			}
			s.fireWebhooks(context.WithoutCancel(ctx), row, string(st))
		}
	}
	s.audit(ctx, "session.info", by, ip, row.ID)
	s.notifyState()
	return nil
}
