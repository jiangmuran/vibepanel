package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Archiving a project hides it, and nothing else.
//
// Its sessions keep running in tmux, keep being polled and keep having their
// scrollback captured; its notes and todos stay. What changes is who is told
// about it: the snapshot, the chat bridge and share walls stop listing it, so
// the sidebar is the projects somebody is working on rather than every
// directory they have ever pointed the panel at.
//
// Freeing the memory those sessions hold was considered and left out on
// purpose. The sessions scope runs with MemorySwapMax=0 (build log,
// 2026-09-14), so the kernel cannot page an archived session out; reopening
// that needs root and undoes a measured decision. A user-space pager would
// need ptrace and userfaultfd on processes the panel is not the parent of, and
// would leave every archived process hanging on the panel being up -- the
// opposite of red line 2. Killing and resuming the agent would free it, and
// loses whatever was not in the transcript. Hiding is what was asked for.

// archivedProject is what the snapshot says about a project that is not in
// the sidebar: enough to list it, find it in the new-project picker, and see
// that something in it wants attention.
//
// Its own shape rather than store.Project plus counts, because the sidebar's
// Project is pinned field by field against wire.ts and a project in this list
// is not one the sidebar may render.
type archivedProject struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	ArchivedAt   int64  `json:"archivedAt"`
	ArchivedAuto bool   `json:"archivedAuto"`
	LastActiveAt int64  `json:"lastActiveAt"`
	// Sessions counts the project's sessions that are still running. Hiding
	// a project is not ending anything, and the list says so in a number.
	Sessions int `json:"sessions"`
	// Waiting counts those among them waiting for a person. An agent that
	// stops for a decision inside an archived project would otherwise wait in
	// a place nobody looks.
	Waiting int `json:"waiting"`
}

// splitArchived divides the session rows between the sidebar and the archived
// list, and builds that list.
func splitArchived(archived []store.Project, sessions []store.Session) (visible []store.Session, out []archivedProject) {
	idx := make(map[string]int, len(archived))
	out = make([]archivedProject, 0, len(archived))
	for _, p := range archived {
		idx[p.ID] = len(out)
		var at int64
		if p.ArchivedAt != nil {
			at = *p.ArchivedAt
		}
		out = append(out, archivedProject{
			ID: p.ID, Name: p.Name, Path: p.Path, ArchivedAt: at,
			ArchivedAuto: p.ArchivedAuto, LastActiveAt: p.LastActiveAt,
		})
	}
	visible = make([]store.Session, 0, len(sessions))
	for _, row := range sessions {
		i, hidden := idx[row.ProjectID]
		if !hidden {
			visible = append(visible, row)
			continue
		}
		if row.Scratch || row.Exited {
			continue
		}
		out[i].Sessions++
		if row.State == session.StateWaiting {
			out[i].Waiting++
		}
	}
	return visible, out
}

func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := chi.URLParam(r, "id")
	err := s.DB.ArchiveProject(ctx, pid, false)
	if errors.Is(err, store.ErrAlreadyArchived) {
		// Two tabs, one click each: the second one got what it asked for.
		err = nil
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	p, err := s.DB.GetProject(ctx, pid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "project.archived", u.Username, s.clientIP(r), p.Name+" ("+p.Path+")")
		s.pluginEventRaised(pluginEvent{Name: "project.archived", ProjectID: p.ID})
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRestoreProject(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.restoreProject(w, r, chi.URLParam(r, "id")); ok {
		writeJSON(w, http.StatusOK, p)
	}
}

// restoreProject brings a project back and answers for it, or writes the
// error and reports false. Shared with creating a project on an archived
// one's directory, which is the same act reached from the picker.
func (s *Server) restoreProject(w http.ResponseWriter, r *http.Request, pid string) (store.Project, bool) {
	ctx := r.Context()
	if err := s.DB.RestoreProject(ctx, pid); err != nil {
		s.writeStoreErr(w, err)
		return store.Project{}, false
	}
	p, err := s.DB.GetProject(ctx, pid)
	if err != nil {
		s.writeStoreErr(w, err)
		return store.Project{}, false
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "project.restored", u.Username, s.clientIP(r), p.Name+" ("+p.Path+")")
	}
	s.notifyState()
	return p, true
}

// archiveIdleKey is how many days without activity archive a project. Unset
// or 0 is off, which is the default: a project disappearing from the sidebar
// is something a person should have chosen.
const archiveIdleKey = "projects.archive_idle_days"

// archiveIdleChoices are the values the settings page offers. A fixed set
// rather than any number, because "1" would archive the projects of anyone
// who took a weekend off, and a setting that can do that by a typo should not.
var archiveIdleChoices = []int{0, 14, 30, 60, 90}

func (s *Server) archiveIdleDays(ctx context.Context) int {
	raw, _ := s.DB.GetSetting(ctx, archiveIdleKey, "0")
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	for _, c := range archiveIdleChoices {
		if n == c {
			return n
		}
	}
	return 0
}

func (s *Server) handlePutArchiveIdle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &req) {
		return
	}
	allowed := false
	for _, c := range archiveIdleChoices {
		allowed = allowed || req.Days == c
	}
	if !allowed {
		writeErr(w, http.StatusBadRequest, "days must be one of 0, 14, 30, 60, 90")
		return
	}
	if err := s.DB.SetSetting(r.Context(), archiveIdleKey, strconv.Itoa(req.Days)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "project.archive_idle", u.Username, s.clientIP(r), strconv.Itoa(req.Days))
	}
	writeJSON(w, http.StatusOK, map[string]int{"days": req.Days})
}

// archiveIdleInterval is how often the idle rule looks. The rule is in days,
// so an hour late is nothing, and one query an hour costs nothing either.
const archiveIdleInterval = time.Hour

// archiveIdleOnce archives the projects nothing has touched for the set number
// of days. Returns how many it archived.
func (s *Server) archiveIdleOnce(ctx context.Context, now time.Time) int {
	days := s.archiveIdleDays(ctx)
	if days <= 0 {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	idle, err := s.DB.IdleProjects(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Warn("idle projects", "err", err)
		}
		return 0
	}
	n := 0
	for _, p := range idle {
		// Asked again at the write, not taken from the list: a project that
		// printed, or was archived by hand, since the query is left as it is.
		archived, err := s.DB.ArchiveIfIdle(ctx, p.ID, cutoff)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Warn("archive idle project", "project", p.ID, "err", err)
			}
			continue
		}
		if !archived {
			continue
		}
		s.audit(ctx, "project.archived_idle", "", "", p.Name+" ("+p.Path+"), "+strconv.Itoa(days)+" days")
		n++
	}
	if n > 0 {
		s.notifyState()
	}
	return n
}
