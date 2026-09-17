package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/claudeaccount"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// Claude accounts. docs/design.md, "Claude accounts", has the measurements;
// internal/claudeaccount has the directory.
//
// Under /api/settings because nothing on the main screen reads them: the
// picker shows profiles, and a profile carries its account's id.
func (s *Server) registerClaudeAccountRoutes(r chi.Router) {
	r.Get("/settings/claude-accounts", s.handleListClaudeAccounts)
	r.Post("/settings/claude-accounts", s.handleCreateClaudeAccount)
	r.Patch("/settings/claude-accounts/{accountID}", s.handleRenameClaudeAccount)
	r.Delete("/settings/claude-accounts/{accountID}", s.handleDeleteClaudeAccount)
	// A GET of its own, and not a field on the list, because it runs `claude`
	// once per account: about a second each, which the list must not wait
	// for and nothing polling may trigger.
	r.Get("/settings/claude-accounts/{accountID}/status", s.handleClaudeAccountStatus)
}

// errClaudeAccountGone is a session or profile naming an account that has
// been deleted.
var errClaudeAccountGone = errors.New("the Claude account it uses has been removed")

// claudeMain is ~/.claude for the user the panel runs as, which is the user
// every session runs as.
func (s *Server) claudeMain() (claudeaccount.Main, error) {
	if s.claudeHome != "" {
		return claudeaccount.Main{Home: s.claudeHome}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return claudeaccount.Main{}, fmt.Errorf("no home directory to share Claude Code's configuration from")
	}
	return claudeaccount.Main{Home: home}, nil
}

func (s *Server) claudeRunner() claudeaccount.Runner {
	if s.claudeRun != nil {
		return s.claudeRun
	}
	return claudeaccount.ExecRunner(tmux.LaunchArgv)
}

// claudeAccountEnv prepares an account for a launch and returns the variables
// that start a process under it. Empty id, nothing.
//
// Every path that starts a process goes through here -- creating a session,
// restoring one, restarting one, the chat assistant, and `vibepanel session
// new` through claudeaccount.PrepareByID -- because the directory is repaired
// on the way in (links a release added, a directory ~/.claude did not have
// yet) and a path that skipped it would start the one session that is missing
// its hooks.
func (s *Server) claudeAccountEnv(ctx context.Context, accountID string) ([]string, error) {
	if accountID == "" {
		return nil, nil
	}
	main, err := s.claudeMain()
	if err != nil {
		return nil, err
	}
	env, err := claudeaccount.PrepareByID(ctx, s.DB, s.Cfg.DataDir, main, accountID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errClaudeAccountGone
	}
	return env, err
}

// claudeAccountView is one account as the settings page gets it.
type claudeAccountView struct {
	store.ClaudeAccount
	// Dir is shown so the account can be used from a terminal the panel did
	// not open: `CLAUDE_CONFIG_DIR=<dir> claude`.
	Dir string `json:"dir"`
	// Profiles are the names of the profiles that start with it.
	Profiles []string `json:"profiles"`
	// Running is how many of its sessions have a tmux session right now.
	Running int `json:"running"`
}

func (s *Server) claudeAccountView(ctx context.Context, a store.ClaudeAccount) (claudeAccountView, error) {
	dir, err := claudeaccount.Dir(s.Cfg.DataDir, a.ID)
	if err != nil {
		return claudeAccountView{}, err
	}
	profiles, err := s.DB.ClaudeAccountProfiles(ctx, a.ID)
	if err != nil {
		return claudeAccountView{}, err
	}
	running, err := s.claudeAccountRunning(ctx, a.ID)
	if err != nil {
		return claudeAccountView{}, err
	}
	return claudeAccountView{ClaudeAccount: a, Dir: dir, Profiles: profiles, Running: len(running)}, nil
}

// claudeAccountRunning returns the titles of the sessions started under an
// account whose tmux session still exists, dead pane or not: a dead pane can
// be restarted in place, into an environment that names the directory.
func (s *Server) claudeAccountRunning(ctx context.Context, accountID string) ([]string, error) {
	sessions, err := s.DB.ClaudeAccountSessions(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, sess := range sessions {
		has, err := s.Tmux.Has(ctx, sess.TmuxName)
		if err != nil {
			return nil, err
		}
		if has {
			title := sess.Title
			if title == "" {
				title = sess.Command
			}
			out = append(out, title)
		}
	}
	return out, nil
}

func (s *Server) handleListClaudeAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.DB.ListClaudeAccounts(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := make([]claudeAccountView, 0, len(list))
	for _, a := range list {
		v, err := s.claudeAccountView(ctx, a)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateClaudeAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Isolated bool   `json:"isolated"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	name, err := store.ValidateClaudeAccountName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := s.DB.CountClaudeAccounts(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if n >= store.MaxClaudeAccounts {
		writeErr(w, http.StatusRequestEntityTooLarge, "that is as many Claude accounts as the panel keeps")
		return
	}
	main, err := s.claudeMain()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	accountID := id.New()
	dir, err := claudeaccount.Dir(s.Cfg.DataDir, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The directory before the row, so a row never names a directory that
	// could not be made; and made now rather than at first launch, so the
	// path the page shows can be used from a terminal straight away.
	if _, err := claudeaccount.Prepare(dir, main, req.Isolated); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec, err := s.DB.CreateClaudeAccount(ctx, accountID, store.ClaudeAccount{Name: name, Isolated: req.Isolated})
	if err != nil {
		_ = os.RemoveAll(dir)
		s.writeStoreErr(w, err)
		return
	}
	// The name only. Not the directory, and never the email: the audit log is
	// printed into a journal and read on a settings page.
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "claude_account.created", u.Username, s.clientIP(r), rec.Name)
	}
	v, err := s.claudeAccountView(ctx, rec)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleRenameClaudeAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	name, err := store.ValidateClaudeAccountName(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.RenameClaudeAccount(ctx, accountID, name); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "claude_account.renamed", u.Username, s.clientIP(r), name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteClaudeAccount logs the account out and removes it.
//
// Refused while anything still depends on it, and the refusal names what:
//
//   - a profile, because the picker would go on offering a way to start
//     something that now refuses to start;
//   - a live tmux session, because its process has CLAUDE_CONFIG_DIR set to
//     the directory about to go, and Claude Code recreates a missing config
//     directory on its next write -- a session that quietly carries on, logged
//     out, in a directory the panel no longer knows about.
//
// Sessions whose tmux session is gone do not block it. They can no longer be
// restored, and restoreSession says why rather than bringing them back under
// ~/.claude's login.
func (s *Server) handleDeleteClaudeAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	ctx := r.Context()
	a, err := s.DB.GetClaudeAccount(ctx, accountID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	profiles, err := s.DB.ClaudeAccountProfiles(ctx, accountID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(profiles) > 0 {
		writeErr(w, http.StatusConflict,
			"these launch profiles still use it: "+strings.Join(profiles, ", "))
		return
	}
	running, err := s.claudeAccountRunning(ctx, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(running) > 0 {
		writeErr(w, http.StatusConflict,
			"these sessions are running under it; end them first: "+strings.Join(running, ", "))
		return
	}
	dir, err := claudeaccount.Dir(s.Cfg.DataDir, accountID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	logoutErr, err := claudeaccount.Remove(ctx, s.claudeRunner(), dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.DeleteClaudeAccount(ctx, accountID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "claude_account.deleted", u.Username, s.clientIP(r), a.Name)
	}
	// The directory is gone either way. What a failed logout can leave behind
	// is a macOS Keychain entry, and saying so is the only way anybody finds
	// out it is there.
	resp := struct {
		LogoutError string `json:"logoutError,omitempty"`
	}{}
	if logoutErr != nil {
		s.Log.Warn("claude account removed without logging out", "account", accountID, "err", logoutErr)
		resp.LogoutError = logoutErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// claudeAccountStatus is the status read, plus what Reconcile found.
type claudeAccountStatus struct {
	// Status is nil when Claude Code could not be asked; Error says why.
	Status *claudeaccount.Status `json:"status"`
	Error  string                `json:"error,omitempty"`
	Links  []claudeaccount.Link  `json:"links"`
}

func (s *Server) handleClaudeAccountStatus(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	ctx := r.Context()
	a, err := s.DB.GetClaudeAccount(ctx, accountID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	main, err := s.claudeMain()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dir, err := claudeaccount.Dir(s.Cfg.DataDir, a.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Prepared first: `claude auth status` in a directory missing its links
	// writes .claude.json and friends there itself, and the page should show
	// what a launch would find.
	links, err := claudeaccount.Prepare(dir, main, a.Isolated)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := claudeAccountStatus{Links: links}
	st, err := claudeaccount.ReadStatus(ctx, s.claudeRunner(), dir)
	if err != nil {
		out.Error = err.Error()
	} else {
		out.Status = &st
	}
	writeJSON(w, http.StatusOK, out)
}
