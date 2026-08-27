package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/git"
)

// The git tab, in two halves that are deliberately not the same endpoint.
//
// GET /git reads the disk. No network, no credential, no configuration, and it
// is the half that always works -- which is the point: branch, uncommitted
// state, ahead/behind and the recent log are the four questions somebody
// watching agents edit a repository actually asks, and every one of them is
// answerable from the working tree.
//
// POST /git/github is the network, and it is a POST for that reason rather than
// because it changes anything. A GET is a thing a browser re-issues on its own
// -- a reload, a back button, a link prefetch, a preconnect -- and "the panel
// never makes an outbound request nobody asked for" cannot survive an endpoint
// with that property. One press, one request.

func (s *Server) registerGitRoutes(r chi.Router) {
	r.Get("/projects/{id}/git", s.handleGit)
	r.Post("/projects/{id}/git/github", s.handleGitHub)
}

// gitSession is one session sitting somewhere other than where the project is.
//
// Only the sessions whose directory is on a *different* commit than the project
// root are listed, and that filter is the whole value of the section. Six
// agents in one directory are six identical rows saying nothing; six agents in
// six worktrees are the thing you opened this tab to see, and there is no other
// screen in the panel that would tell you.
type gitSession struct {
	SessionID  string `json:"sessionId"`
	Branch     string `json:"branch"`
	Detached   bool   `json:"detached"`
	Head       string `json:"head"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	Staged     int    `json:"staged"`
	Unstaged   int    `json:"unstaged"`
	Untracked  int    `json:"untracked"`
	Conflicted int    `json:"conflicted"`
}

// gitResponse is everything the local half knows.
type gitResponse struct {
	Status  git.Status   `json:"status"`
	Commits []git.Commit `json:"commits"`
	// Remote is nil where there is no origin, which is the ordinary state of a
	// repository nobody has pushed yet.
	Remote *git.Remote `json:"remote"`
	// GitHub says the remote is one the network half can query at all. Separate
	// from TokenSet so the panel can tell "this is not a GitHub repository"
	// from "you have not given the panel a token", which are different
	// sentences with different actions.
	GitHub   bool `json:"github"`
	TokenSet bool `json:"tokenSet"`

	Sessions []gitSession `json:"sessions"`
	// SessionsTruncated says the list stopped at the cap rather than at the
	// end. A list that silently stops is the defect this panel refuses
	// everywhere else.
	SessionsTruncated bool `json:"sessionsTruncated"`
}

// maxSessionDirs bounds how many working trees one request will read.
//
// Each one is a process. A dozen is more parallel agents than anybody has on
// one project, and the bound is what stops a page load from forking fifty
// times because somebody scripted session creation.
const maxSessionDirs = 12

// recentCommits is how much log the tab carries.
//
// Fifteen, because the question is "what happened while I was not looking" and
// that is a screenful. More is a git client, which this is not.
const recentCommits = 15

func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.GetProject(ctx, chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}

	// Every list starts as an empty slice rather than nil. A nil slice marshals
	// to `null`, the frontend's next move is `.length`, and the case that gets
	// there first is the empty one -- a directory that is not a repository, or
	// a clean tree. See browse.List, where exactly this took the whole console
	// down on an empty project.
	out := gitResponse{
		Status:   git.Status{Changes: []git.Change{}},
		Commits:  []git.Commit{},
		Sessions: []gitSession{},
		TokenSet: git.Token() != "",
	}
	st, err := git.ReadStatus(ctx, p.Path)
	if err != nil {
		if errors.Is(err, git.ErrNotARepo) {
			// Not a repository is an answer, not a failure. Plenty of project
			// directories are not repositories and the tab says so in a line.
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.Status = st

	if commits, lerr := git.ReadLog(ctx, p.Path, recentCommits); lerr == nil {
		out.Commits = commits
	}
	if remote, ok, rerr := git.ReadRemote(ctx, p.Path); rerr == nil && ok {
		out.Remote = &remote
		out.GitHub = remote.GitHub()
	}

	out.Sessions, out.SessionsTruncated = s.sessionBranches(r, p.ID, p.Path, out.Status)
	writeJSON(w, http.StatusOK, out)
}

// sessionBranches reads the working tree each session is sitting in.
//
// A session's cwd comes from tmux and is an absolute path the session can
// change at any time with `cd`, so it is put back through browse.Resolve
// against the project root before anything runs in it. That is not paranoia
// about tmux: it is the same containment as every other file route, and it
// means a session that has wandered to /etc simply has no row here rather than
// having git run somewhere nobody chose. There is one path check in this
// codebase and this uses it.
func (s *Server) sessionBranches(r *http.Request, projectID, root string, head git.Status) ([]gitSession, bool) {
	ctx := r.Context()
	all, err := s.DB.ListSessions(ctx)
	if err != nil {
		return []gitSession{}, false
	}

	// One read per distinct directory, however many sessions share it.
	seen := map[string]git.Status{}
	out := []gitSession{}
	truncated := false
	for _, sess := range all {
		if sess.ProjectID != projectID || sess.CWD == "" {
			continue
		}
		rel, rerr := filepath.Rel(root, sess.CWD)
		if rerr != nil {
			continue
		}
		// Rejected here rather than left to Resolve, and the difference is not
		// a security one -- Resolve cleans "../../etc" against "/" and lands
		// back inside the project either way, so nothing escapes. What it stops
		// is worse than an error: a session sitting in ~/other/thing has a
		// relative path of "../other/thing", which cleans to "other/thing" and
		// reads a *different directory inside this project*. The row would name
		// one session and describe another one's tree, and nothing would say so.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		abs, aerr := browse.Resolve(root, rel)
		if aerr != nil {
			continue
		}
		st, ok := seen[abs]
		if !ok {
			if len(seen) >= maxSessionDirs {
				truncated = true
				continue
			}
			st, err = git.ReadStatus(ctx, abs)
			if err != nil {
				// Remembered as the zero Status so a directory that is not a
				// repository is not re-read once per session in it.
				st = git.Status{}
			}
			seen[abs] = st
		}
		if !st.Repo {
			continue
		}
		// The filter that makes this section worth having: a session on the
		// same commit as the project root has nothing to add to what the header
		// already says.
		if st.Branch == head.Branch && st.Head == head.Head {
			continue
		}
		out = append(out, gitSession{
			SessionID:  sess.ID,
			Branch:     st.Branch,
			Detached:   st.Detached,
			Head:       st.Head,
			Ahead:      st.Ahead,
			Behind:     st.Behind,
			Staged:     st.Staged,
			Unstaged:   st.Unstaged,
			Untracked:  st.Untracked,
			Conflicted: st.Conflicted,
		})
	}
	return out, truncated
}

// githubResponse is one press of the button.
type githubResponse struct {
	Total int      `json:"total"`
	PRs   []git.PR `json:"prs"`
	// CheckedAt is when this answer was fetched. On screen next to the list,
	// because there is no poller behind it: without a timestamp a list of pull
	// requests looks live, and it is as old as the last press.
	CheckedAt int64 `json:"checkedAt"`
}

// handleGitHub asks GitHub about the project's repository.
//
// The only outbound request in the panel that is not an update check, and like
// that one it happens when somebody presses a button and at no other time.
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.GetProject(ctx, chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	token := git.Token()
	if token == "" {
		writeErr(w, http.StatusBadRequest, "no GitHub token in the panel's environment")
		return
	}
	remote, ok, rerr := git.ReadRemote(ctx, p.Path)
	if rerr != nil || !ok || !remote.GitHub() {
		writeErr(w, http.StatusBadRequest, "this project has no github.com remote")
		return
	}
	client := s.GitHub
	client.Token = token
	res, err := client.OpenPRs(ctx, remote.Owner, remote.Name)
	if err != nil {
		// 502 rather than 500: the panel worked, the other end did not, and
		// telling those apart is the difference between "check your token" and
		// "file a bug".
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, githubResponse{
		Total:     res.Total,
		PRs:       res.PRs,
		CheckedAt: time.Now().Unix(),
	})
}
