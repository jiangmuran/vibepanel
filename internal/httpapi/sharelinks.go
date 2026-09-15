package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Links you can copy again, links visitors can act through, and the one
// switch that turns every visitor write off. docs/page-backend.md §6 and §7.

// secrets is the panel's sealing key, opened on first use.
//
// Lazily, and the error remembered only until it succeeds: a data directory
// that cannot be written at startup should not stop a panel whose links do
// not need the key, and a key that could not be read a moment ago may be
// readable now.
type secrets struct {
	mu  sync.Mutex
	box *secret.Box
}

func (s *Server) secretBox() (*secret.Box, error) {
	s.pb.secrets.mu.Lock()
	defer s.pb.secrets.mu.Unlock()
	if s.pb.secrets.box != nil {
		return s.pb.secrets.box, nil
	}
	box, err := secret.Open(filepath.Join(s.Cfg.DataDir, secret.KeyFile))
	if err != nil {
		return nil, err
	}
	s.pb.secrets.box = box
	return box, nil
}

// shareTokenContext is what a link's sealed token is bound to, so a sealed
// token copied onto another row does not open there.
func shareTokenContext(linkID string) string { return "share-link:" + linkID }

// sealShareToken seals a new link's token, or returns nil when the key cannot
// be opened -- the link is still made, and says it cannot be copied again.
func (s *Server) sealShareToken(linkID, token string) []byte {
	box, err := s.secretBox()
	if err != nil {
		s.Log.Warn("share link address will not be copyable", "err", err)
		return nil
	}
	return box.Seal([]byte(token), shareTokenContext(linkID))
}

func (s *Server) shareAddress(r *http.Request, token string) string {
	return s.requestOrigin(r) + "/share/" + token + "/"
}

// handleShareURL answers a handed-out link's address, again.
func (s *Server) handleShareURL(w http.ResponseWriter, r *http.Request) {
	linkID := chi.URLParam(r, "shareID")
	enc, err := s.DB.ShareLinkTokenEnc(r.Context(), linkID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(enc) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "this link was made before addresses were kept; give it a new address",
			"rotatable": true,
		})
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the panel's secrets key cannot be read: "+err.Error())
		return
	}
	token, err := box.Unseal(enc, shareTokenContext(linkID))
	if err != nil {
		// A key replaced since the link was made. Said, and the answer is the
		// same as for an old link: a new address.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "this link's address does not open under the panel's current key; give it a new address",
			"rotatable": true,
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": s.shareAddress(r, string(token)), "token": string(token)})
}

// handleRotateShare gives a link a new address; the old one stops working.
//
// Allowed on a locked link: a lock fixes what a screen draws, and a leaked
// address is exactly the moment somebody must not have to unlock first.
func (s *Server) handleRotateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID := chi.URLParam(r, "shareID")
	link, err := s.DB.ShareLinkByID(ctx, linkID)
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.RotateShareLink(ctx, linkID, auth.HashToken(token), s.sealShareToken(linkID, token), token[:8]); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "share.rotated", u.Username, s.clientIP(r), link.Name)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": s.shareAddress(r, token), "token": token})
}

// linkMayAct is whether visitor actions may run through a link at all: an
// interactive handed-out link, or a preview link (whose actions write draft
// data), and never a view link; and never while visitor writes are off.
func (s *Server) linkMayAct(ctx context.Context, link store.ShareLink) bool {
	switch link.Purpose {
	case "":
		if !link.Interactive {
			return false
		}
	case store.SharePurposePreview:
	default:
		return false
	}
	return s.visitorWritesAllowed(ctx)
}

// ─── the panel-wide switch ────────────────────────────────────────────────

const visitorWritesKey = "sharing.visitor_writes"

// visitorWritesAllowed is read on every visitor action, from the database, so
// turning it off stops the next action rather than the next restart.
func (s *Server) visitorWritesAllowed(ctx context.Context) bool {
	v, err := s.DB.GetSetting(ctx, visitorWritesKey, "1")
	// A database that cannot say is a no: the write is the thing that would
	// have to be undone.
	return err == nil && v == "1"
}

func (s *Server) handleGetSharing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"visitorWrites": s.visitorWritesAllowed(r.Context())})
}

func (s *Server) handlePutSharing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VisitorWrites *bool `json:"visitorWrites"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.VisitorWrites == nil {
		writeErr(w, http.StatusBadRequest, "visitorWrites is required")
		return
	}
	v := "0"
	if *req.VisitorWrites {
		v = "1"
	}
	if err := s.DB.SetSetting(r.Context(), visitorWritesKey, v); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		state := "off"
		if *req.VisitorWrites {
			state = "on"
		}
		s.audit(r.Context(), "sharing.visitor_writes", u.Username, s.clientIP(r), state)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"visitorWrites": *req.VisitorWrites})
}

// ─── counting actions ─────────────────────────────────────────────────────

// actionDay counts visitor actions per link for today, in memory: a number the
// settings row shows, which a restart is allowed to reset. The day is the
// panel's (Server.today), passed in, so the count turns over when the
// spend figures beside it do.
type actionDay struct {
	mu     sync.Mutex
	day    string
	counts map[string]int
}

func (a *actionDay) add(linkID, day string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roll(day)
	a.counts[linkID]++
}

func (a *actionDay) today(linkID, day string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roll(day)
	return a.counts[linkID]
}

func (a *actionDay) roll(day string) {
	if a.day != day || a.counts == nil {
		a.day, a.counts = day, map[string]int{}
	}
}

// fillLinkCounts adds the in-memory figures a listed link carries.
func (s *Server) fillLinkCounts(ctx context.Context, links []store.ShareLink, now time.Time) {
	day := s.today(ctx)
	for i := range links {
		links[i].Viewers, links[i].ViewportWidth, links[i].ViewportHeight = s.viewers.count(links[i].ID, now)
		links[i].ActionsToday = s.pb.actions.today(links[i].ID, day)
	}
}
