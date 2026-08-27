package httpapi

import (
	"context"
	"errors"
	"net/http"

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
	if store.IsBuiltinLaunchProfile(profileID) {
		// A 400 rather than a silent no-op, and rather than turning the
		// built-in into a row on first edit. Copy-on-write here would mean the
		// catalogue a release ships stops being the catalogue people have, one
		// panel at a time, with nothing on screen saying so.
		writeErr(w, http.StatusBadRequest,
			"a built-in profile cannot be edited; duplicate it and edit the copy")
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
	if store.IsBuiltinLaunchProfile(profileID) {
		writeErr(w, http.StatusBadRequest, "a built-in profile cannot be removed")
		return
	}
	if err := s.DB.DeleteLaunchProfile(r.Context(), profileID); err != nil {
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
