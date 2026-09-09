package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Launch profiles.
//
// Mounted at /api/launch-profiles rather than under /api/settings, where the
// other list-of-named-things endpoints live, because this one is not a settings
// read: the session picker fetches it on every page load. A main-screen control
// polling /api/settings/... reads as administration and is the kind of thing
// somebody later moves for tidiness.
func (s *Server) registerLaunchProfileRoutes(r chi.Router) {
	r.Get("/launch-profiles", s.handleListProfiles)
	r.Post("/launch-profiles", s.handleCreateProfile)
	r.Patch("/launch-profiles/{profileID}", s.handleUpdateProfile)
	r.Delete("/launch-profiles/{profileID}", s.handleDeleteProfile)
	r.Post("/launch-profiles/reorder", s.handleReorderProfiles)
	r.Post("/launch-profiles/restore", s.handleRestoreProfiles)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListLaunchProfiles(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(list))
}

// The request body for both create and update.
//
// One struct because the two take the same thing: a profile is written whole.
// A partial edit would have to decide what an absent `env` means, and the
// answer somebody would reach for -- leave it alone -- is how a rename ends up
// being the request that keeps a key the user thought they had removed.
type profileRequest struct {
	Name    string               `json:"name"`
	Command []string             `json:"command"`
	Env     []store.LaunchEnvVar `json:"env"`
}

func (req profileRequest) profile() store.LaunchProfile {
	return store.LaunchProfile{Name: req.Name, Command: req.Command, Env: req.Env}
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	n, err := s.DB.CountLaunchProfiles(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if n >= store.MaxLaunchProfiles {
		writeErr(w, http.StatusRequestEntityTooLarge, "that is as many profiles as the picker can stay useful with")
		return
	}
	p, err := store.ValidateLaunchProfile(req.profile())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := s.DB.CreateLaunchProfile(ctx, id.New(), p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// The name and nothing else. A profile's variables are the reason this
	// feature holds credentials at all, and the audit log is read on a settings
	// page, printed into a journal and shipped to whatever collects journals.
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "profile.created", u.Username, s.clientIP(r), rec.Name)
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "profileID")
	var req profileRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	// Editing a built-in writes a row at the built-in's own id, and the row is
	// what the list shows from then on.
	//
	// This used to be a 400, and the reason given was a good one: copy-on-write
	// would mean the catalogue a release ships stops being the catalogue people
	// have, one panel at a time, with nothing on screen saying so. That is an
	// argument against an *invisible* override, not against the edit. The row
	// comes back marked `overridden`, the settings page says so, and deleting
	// it puts the built-in back -- so a release that corrects a variable name
	// is one click away from being adopted rather than lost.
	//
	// The id stays the built-in's, which is the other half. A session records
	// the profile it was started with; turning an edit into a new profile with
	// a new id would leave every existing session pointing at something that no
	// longer exists.
	if store.IsBuiltinLaunchProfile(profileID) {
		p, verr := store.ValidateLaunchProfile(req.profile())
		if verr != nil {
			writeErr(w, http.StatusBadRequest, verr.Error())
			return
		}
		if err := s.DB.UpsertLaunchProfile(ctx, profileID, p); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if u, ok := currentUserFrom(r); ok {
			s.audit(ctx, "profile.updated", u.Username, s.clientIP(r), p.Name)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	prev, err := s.DB.GetLaunchProfile(ctx, profileID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	p, err := store.ValidateLaunchProfile(store.MergeLaunchSecrets(req.profile(), prev))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.UpdateLaunchProfile(ctx, profileID, p); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "profile.updated", u.Username, s.clientIP(r), p.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "profileID")
	ctx := r.Context()
	// A built-in is hidden rather than deleted, because there is no row to
	// delete: it is a slice in internal/store. 「自带的几个 agent 没法删啊现在」
	// -- and offering somebody an agent they have not installed is not a
	// catalogue, it is clutter they cannot clear. Reversible, through
	// /restore, which is what makes it safe to be a one-click delete.
	//
	// Any override row goes with it. Otherwise "delete" would leave the edited
	// copy behind and put the original back, which is the opposite of what was
	// asked for.
	if store.IsBuiltinLaunchProfile(profileID) {
		arr, err := s.DB.GetLaunchArrangement(ctx)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if !slices.Contains(arr.Hidden, profileID) {
			arr.Hidden = append(arr.Hidden, profileID)
		}
		if err := s.DB.SetLaunchArrangement(ctx, arr); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		// Best effort: there may be no override row, and not having one is the
		// ordinary case.
		if err := s.DB.DeleteLaunchProfile(ctx, profileID); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			s.writeStoreErr(w, err)
			return
		}
		if u, ok := currentUserFrom(r); ok {
			s.audit(ctx, "profile.deleted", u.Username, s.clientIP(r), profileID)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.DB.DeleteLaunchProfile(ctx, profileID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "profile.deleted", u.Username, s.clientIP(r), profileID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// launchProfileFor resolves the profile a session is being started with.
//
// Returns nil for "no profile", which is what an empty id means and what every
// session created before this feature has. A profile id that names nothing is
// an error rather than a silent nil: creating a session against a gateway that
// has been deleted, and getting one against the default endpoint instead, is
// the kind of quiet substitution that is only noticed by the bill.
func (s *Server) launchProfileFor(ctx context.Context, profileID string) (*store.LaunchProfile, error) {
	if profileID == "" {
		return nil, nil
	}
	p, err := s.DB.GetLaunchProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// restoreProfileFor is the same lookup on the path that rebuilds a session
// after a reboot, where a missing profile must not stop the restore.
//
// A session outlives the profile it was started with, and the row records the
// id rather than a copy of the variables. So this can legitimately find
// nothing, and the choice is between refusing to restore the session at all and
// restoring it without the environment it had. It restores, and says so in the
// log; the session row still carries the id, which is what lets the UI show
// that the profile is gone rather than implying the session still has it.
func (s *Server) restoreProfileFor(ctx context.Context, sessionID, profileID string) *store.LaunchProfile {
	p, err := s.launchProfileFor(ctx, profileID)
	if err == nil {
		return p
	}
	if errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("restoring a session whose launch profile has been deleted; "+
			"it starts without that profile's environment",
			"session", sessionID, "profile", profileID)
	} else {
		s.Log.Warn("restore: read launch profile", "session", sessionID, "err", err)
	}
	return nil
}

// handleReorderProfiles records the order the picker offers them in.
//
// The whole list in one request, ids only, like every other reorder here: a
// per-item index is a sequence of writes that can half-apply, and a half
// applied order is one a person has to fix by dragging again.
//
// Ids that are not in the list are ignored rather than refused. The browser is
// sending back what it was just shown, and something created or hidden in
// another tab in between is a race that must not cost somebody their drag.
func (s *Server) handleReorderProfiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) > store.MaxLaunchProfiles+len(store.BuiltinLaunchProfiles()) {
		writeErr(w, http.StatusRequestEntityTooLarge, "that is more ids than there are profiles")
		return
	}
	ctx := r.Context()
	arr, err := s.DB.GetLaunchArrangement(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	arr.Order = req.IDs
	if err := s.DB.SetLaunchArrangement(ctx, arr); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestoreProfiles puts the built-ins back.
//
// Un-hides every one of them and drops every override row, which is the single
// action that undoes anything this feature can do to the catalogue. It exists
// because delete and edit are both one click: a one-click change with no
// one-click way back is a change people are right to be wary of, and being
// wary of the delete button is how a list stays cluttered.
//
// The owner's own profiles are not touched. Nothing here has any business
// removing those, and the button that did would be the worst button in the
// panel.
func (s *Server) handleRestoreProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	arr, err := s.DB.GetLaunchArrangement(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	arr.Hidden = nil
	if err := s.DB.SetLaunchArrangement(ctx, arr); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, b := range store.BuiltinLaunchProfiles() {
		if err := s.DB.DeleteLaunchProfile(ctx, b.ID); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			s.writeStoreErr(w, err)
			return
		}
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "profile.restored", u.Username, s.clientIP(r), "")
	}
	w.WriteHeader(http.StatusNoContent)
}
