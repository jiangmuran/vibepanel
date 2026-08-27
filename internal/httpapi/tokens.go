package httpapi

import (
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/usage"
)

// registerTokenRoutes mounts the token-usage endpoints.
//
// Named token-usage rather than usage or tokens because both were taken and
// both would have been read as the other thing: /api/usage is per-session CPU
// and memory right now, and /api/settings/tokens is API credentials. Three
// unrelated meanings of "usage" and "token" in one API is how somebody calls
// the wrong one.
func (s *Server) registerTokenRoutes(r chi.Router) {
	r.Get("/token-usage", s.handleTokenUsage)
	r.Post("/token-usage/refresh", s.handleTokenUsageRefresh)
}

// tokens returns the ingester, or nil when the panel was built without one.
func (s *Server) tokens() *usage.Ingester { return s.Tokens }

// heatmapDays is how far back the year grid reaches: 53 weeks.
//
// A GitHub-style grid is whole weeks, and 52 of them leave the current week
// half-drawn at one end. 371 days is 53 whole weeks, which is what makes the
// first column complete and the leftmost month label true.
const heatmapDays = 371

// defaultRange is the range control's starting position.
const defaultRange = 30

// tokenUsageSource is one agent's transcript directory as the last pass found
// it.
//
// Found is the field that decides whether a number below may be rendered at
// all. False means this agent contributed nothing *because nothing could be
// read*, and the panel has to say that instead of a zero.
type tokenUsageSource struct {
	Tool    string `json:"tool"`
	Root    string `json:"root"`
	Found   bool   `json:"found"`
	Problem string `json:"problem"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Skipped int    `json:"skipped"`
}

// tokenUsageSession is one agent session, with the panel project it can be
// matched to if there is one.
type tokenUsageSession struct {
	store.UsageAgentSession
	// ProjectID is empty when the directory this ran in belongs to no project
	// the panel knows about. That is a normal state, not an error: the panel
	// does not have to have heard of a directory for an agent to have worked
	// in it.
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
}

// tokenUsageProject is one project's spend, plus the catch-all for work done
// outside every project.
type tokenUsageProject struct {
	// ID is empty for the catch-all row.
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	store.UsageTotals
}

type tokenUsageResponse struct {
	// ScannedAt is zero until a pass has finished. The browser must render
	// "still reading" rather than a total while it is, because a zero here
	// means "nothing has been counted yet" and looks exactly like "nothing was
	// spent".
	ScannedAt int64 `json:"scannedAt"`
	Scanning  bool  `json:"scanning"`
	PassMs    int64 `json:"passMs"`
	// PassError is whatever went wrong during the last pass. Non-empty means
	// the totals below are incomplete.
	PassError string             `json:"passError"`
	Sources   []tokenUsageSource `json:"sources"`

	// Today is the server's local date. The buckets are local days, so the
	// browser must not decide for itself what today is -- a phone in another
	// timezone would highlight the wrong square.
	Today string `json:"today"`
	From  string `json:"from"`
	To    string `json:"to"`
	Days  int    `json:"days"`

	Total    store.UsageTotals       `json:"total"`
	ByDay    []store.UsageDay        `json:"byDay"`
	Heatmap  []store.UsageDay        `json:"heatmap"`
	ByMonth  []store.UsageDay        `json:"byMonth"`
	ByTool   []store.UsageToolTotals `json:"byTool"`
	Projects []tokenUsageProject     `json:"projects"`

	Sessions     []tokenUsageSession `json:"sessions"`
	SessionCount int                 `json:"sessionCount"`
	SessionLimit int                 `json:"sessionLimit"`
}

// handleTokenUsage reports what the agents recorded spending.
//
// Read from the database, never from the transcripts: a request that walked
// 2 GB of JSONL would take seconds and the browser polls this. A pass is
// kicked off in the background when the answer is stale, and the response says
// so, so the first load of a machine with a year of history renders "reading
// transcripts" and then fills in — rather than rendering a confident zero.
func (s *Server) handleTokenUsage(w http.ResponseWriter, r *http.Request) {
	in := s.tokens()
	if in == nil {
		writeErr(w, http.StatusServiceUnavailable, "token usage is not configured")
		return
	}
	in.Ensure(false)

	q := r.URL.Query()

	// days: clamped rather than rejected. It decides the size of one GROUP BY
	// over a table this panel owns, so a silly value is a silly chart and not
	// a denial of service -- but an unbounded one would still be a query with
	// no ceiling, and the clamp is where that stops.
	days := defaultRange
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "days must be a number")
			return
		}
		days = min(max(n, 1), 3660)
	}

	// tool: validated against the list rather than passed through. It reaches
	// a SQL parameter, so it is not an injection risk; it is a correctness one.
	// An unknown value silently matches nothing, and a filter that returns an
	// empty chart without saying why is indistinguishable from a quiet week.
	tool := q.Get("tool")
	if tool != "" {
		known := false
		for _, t := range usage.Tools {
			if string(t) == tool {
				known = true
			}
		}
		if !known {
			writeErr(w, http.StatusBadRequest, "unknown tool")
			return
		}
	}

	ctx := r.Context()
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}

	// project: an id, resolved here to the path the rows are keyed by. The
	// browser never sends a path -- handing it one and taking it back would
	// let a caller filter on any directory on the machine and learn from the
	// answer whether an agent had ever run there.
	var cwdPrefix string
	if id := q.Get("project"); id != "" {
		found := false
		for _, p := range projects {
			if p.ID == id {
				cwdPrefix = p.Path
				found = true
			}
		}
		if !found {
			writeErr(w, http.StatusBadRequest, "no such project")
			return
		}
	}

	now := time.Now()
	today := now.Format("2006-01-02")
	from := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	ranged := store.UsageFilter{From: from, To: today, Tool: tool, CWDPrefix: cwdPrefix}
	// The heatmap is deliberately not narrowed by the range control. It is the
	// "how has this year gone" view, and a 53-week grid holding seven days of
	// data is not a smaller version of that -- it is a broken one.
	yearly := store.UsageFilter{
		From:      now.AddDate(0, 0, -(heatmapDays - 1)).Format("2006-01-02"),
		To:        today,
		Tool:      tool,
		CWDPrefix: cwdPrefix,
	}
	allTime := store.UsageFilter{Tool: tool, CWDPrefix: cwdPrefix}

	out := tokenUsageResponse{
		Today: today, From: from, To: today, Days: days,
		SessionLimit: usage.SessionLimit(),
	}

	if out.ByDay, err = s.DB.UsageByDay(ctx, ranged); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if out.Heatmap, err = s.DB.UsageByDay(ctx, yearly); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Months are every month there has ever been, not just the ones inside the
	// range: "每个月" is the question, and answering it with the last thirty
	// days of months is answering a different one.
	if out.ByMonth, err = s.DB.UsageByMonth(ctx, allTime); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, d := range out.ByDay {
		out.Total.Input += d.Input
		out.Total.Output += d.Output
		out.Total.CacheRead += d.CacheRead
		out.Total.CacheWrite += d.CacheWrite
		out.Total.Requests += d.Requests
	}

	byTool, err := s.DB.UsageByTool(ctx, ranged)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Every known tool appears, spend or none, so that an agent contributing
	// nothing is visible next to its reason rather than absent.
	for _, t := range usage.Tools {
		row := byTool[string(t)]
		row.Tool = string(t)
		out.ByTool = append(out.ByTool, row)
	}

	dirs, err := s.DB.UsageByDirectory(ctx, ranged)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out.Projects = groupByProject(dirs, projects)

	sessions, count, err := s.DB.UsageBySession(ctx, ranged, usage.SessionLimit())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out.SessionCount = count
	out.Sessions = make([]tokenUsageSession, 0, len(sessions))
	for _, sess := range sessions {
		row := tokenUsageSession{UsageAgentSession: sess}
		if p, ok := projectFor(sess.CWD, projects); ok {
			row.ProjectID, row.ProjectName = p.ID, p.Name
		}
		out.Sessions = append(out.Sessions, row)
	}

	pass, scanning := in.Status()
	out.Scanning = scanning
	if !pass.At.IsZero() {
		out.ScannedAt = pass.At.Unix()
		out.PassMs = pass.Duration.Milliseconds()
		out.PassError = pass.Err
	}
	for _, src := range pass.Sources {
		out.Sources = append(out.Sources, tokenUsageSource{
			Tool: string(src.Tool), Root: src.Root, Found: src.Found,
			Problem: src.Problem, Files: src.Files, Bytes: src.Bytes, Skipped: src.Skipped,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

// handleTokenUsageRefresh forces a pass now.
//
// 202 rather than 200, and it does not wait. A first pass over a year of
// history is seconds of disk; a request that blocked on it would be
// indistinguishable from a hung panel, and a browser that retried would ask
// for a second one. The button says "check again" and the numbers arrive on
// the next poll.
func (s *Server) handleTokenUsageRefresh(w http.ResponseWriter, r *http.Request) {
	in := s.tokens()
	if in == nil {
		writeErr(w, http.StatusServiceUnavailable, "token usage is not configured")
		return
	}
	started := in.Ensure(true)
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": started})
}

// projectFor finds the project a working directory belongs to.
//
// Longest path first, so a project nested inside another wins -- which is the
// arrangement somebody makes on purpose, and picking the outer one would put
// all of the inner project's spend on its parent.
//
// Compared segment by segment rather than as a string prefix: /home/me/api and
// /home/me/api-v2 share nine characters, and a prefix match folds the second
// project's spend into the first.
func projectFor(cwd string, projects []store.Project) (store.Project, bool) {
	best := -1
	var found store.Project
	for _, p := range projects {
		if p.Path == "" || !under(cwd, p.Path) {
			continue
		}
		if len(p.Path) > best {
			best, found = len(p.Path), p
		}
	}
	return found, best >= 0
}

// under reports whether path is dir or inside it.
func under(path, dir string) bool {
	dir = strings.TrimSuffix(filepath.Clean(dir), string(filepath.Separator))
	path = filepath.Clean(path)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// groupByProject folds per-directory totals into per-project ones, keeping
// everything that matched no project in a row of its own.
//
// The catch-all row is the point. Agents get run in directories the panel has
// never been told about, and dropping those would make the project table
// disagree with the total above it by an amount nothing explains.
func groupByProject(dirs []store.UsageDirectory, projects []store.Project) []tokenUsageProject {
	byID := map[string]*tokenUsageProject{}
	var outside tokenUsageProject

	for _, d := range dirs {
		p, ok := projectFor(d.CWD, projects)
		if !ok {
			outside.Input += d.Input
			outside.Output += d.Output
			outside.CacheRead += d.CacheRead
			outside.CacheWrite += d.CacheWrite
			outside.Requests += d.Requests
			continue
		}
		row := byID[p.ID]
		if row == nil {
			row = &tokenUsageProject{ID: p.ID, Name: p.Name, Path: p.Path}
			byID[p.ID] = row
		}
		row.Input += d.Input
		row.Output += d.Output
		row.CacheRead += d.CacheRead
		row.CacheWrite += d.CacheWrite
		row.Requests += d.Requests
	}

	out := make([]tokenUsageProject, 0, len(byID)+1)
	for _, row := range byID {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total() > out[j].Total() })
	if outside.Total() > 0 {
		// Always last: it is a residue, not a project, and sorting it into the
		// middle of the list would read as one.
		out = append(out, outside)
	}
	return out
}
