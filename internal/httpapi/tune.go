package httpapi

import (
	"net/http"

	"github.com/jiangmuran/vibepanel/internal/hooks"
)

// tuneRow is one setting, as the settings page and the first-run tour show it.
//
// Both languages travel with the row rather than being looked up in the
// frontend's dictionary. The descriptions live beside the keys in
// internal/hooks for the reason recorded there -- a description that drifts
// from the key it names is a summary reporting something other than what was
// written -- and duplicating them into i18n.ts would undo exactly that.
type tuneRow struct {
	Key    string `json:"key"`
	What   string `json:"what"`
	WhatZH string `json:"whatZh"`
	Have   any    `json:"have"`
	Want   any    `json:"want"`
	Same   bool   `json:"same"`
}

type tuneStatus struct {
	Path    string    `json:"path"`
	Exists  bool      `json:"exists"`
	Changes int       `json:"changes"`
	Rows    []tuneRow `json:"rows"`
}

func asTuneStatus(st hooks.TuneStatus) tuneStatus {
	out := tuneStatus{Path: st.Path, Exists: st.Exists, Changes: st.Changes, Rows: []tuneRow{}}
	for _, r := range st.Rows {
		out.Rows = append(out.Rows, tuneRow{
			Key: r.Key, What: r.What, WhatZH: r.WhatZH,
			Have: r.Have, Want: r.Want, Same: r.Same,
		})
	}
	return out
}

// handleTuneStatus reports what applying would change, without changing it.
func (s *Server) handleTuneStatus(w http.ResponseWriter, r *http.Request) {
	st, err := hooks.InspectTune()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, asTuneStatus(st))
}

// handleTuneApply writes them.
//
// The response is the comparison as it was *before* the write, which is what
// the page needs to say what happened: a list where every row already agrees,
// rendered after making them agree, tells nobody anything.
func (s *Server) handleTuneApply(w http.ResponseWriter, r *http.Request) {
	before, err := hooks.ApplyTune()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.tuned", u.Username, s.clientIP(r), before.Path)
	}
	writeJSON(w, http.StatusOK, asTuneStatus(before))
}
