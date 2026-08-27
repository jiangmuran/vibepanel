package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// A read-only share link is a second door onto a panel whose first door is one
// password in front of a writable terminal. Everything in this file exists to
// make that door narrow enough to be worth having.
//
// Three properties, and each is enforced by structure rather than by care:
//
//  1. It is a capability. The credential is 32 bytes of crypto/rand in the URL
//     and the database keeps only its SHA-256, exactly as store.APIToken does.
//     Nothing anywhere can read a link back out.
//
//  2. It is read-only because of where it is registered, not because a handler
//     checks a flag. registerShareRoutes mounts one GET below its own
//     middleware; that middleware resolves the token against share_links and
//     nothing else, and share_links is not consulted by currentUser. So a share
//     token presented as a cookie or as a Bearer header is not a credential at
//     all -- it is an unknown string, and every authenticated route answers 401
//     to it. The alternative, a `readOnly` flag on the session that each
//     handler consults, is a hole in whichever handler is written next.
//
//  3. It discloses less than the panel knows, and the redaction is a set of
//     structs in this file rather than an omission at each call site. What is
//     NOT here is the part worth reading: no project path, no cwd, no command
//     line, no tmux name, no real session id, no hostname, no disk path. A
//     project path names a client and a directory layout; a command line
//     carries whatever an agent was invoked with. Neither belongs on a screen
//     behind somebody's desk, and neither has a use on it.
//
// Titles and project names are the one judgement call, so they are a decision
// the person makes per link: store.ShareCounts shows shapes and numbers and no
// text, store.ShareNames adds the names. Counts is the default, because the
// default has to be the one that is safe to point a camera at.

// shareTouchWindow is how often a link's "last seen" may be written.
//
// A wall display polls every couple of seconds, forever. Stamping every lookup
// is around forty thousand writes a day through SQLite's single write lock,
// onto the disk the projects live on, to maintain a field the settings page
// renders as a date. One a minute says the same thing.
const shareTouchWindow = time.Minute

type shareContextKey struct{}

// shareContext is what the middleware hands the handler: the row, and the
// secret the pseudonymous ids are derived from.
type shareContext struct {
	link store.ShareLink
	// secret is the stored hash of the presented token. It never leaves the
	// server and is stable for the life of the link, which is exactly what
	// shareID needs.
	secret []byte
}

func (s *Server) shareCooldowns() *auth.Cooldown {
	s.shareTouchOnce.Do(func() {
		s.shareTouch = auth.NewCooldown(shareTouchWindow)
	})
	return s.shareTouch
}

// registerShareRoutes mounts the entire surface a share token can reach.
//
// One route. Adding a second is the decision, and it should be made against
// the list of what a share token deliberately cannot do: read terminal bytes,
// open the socket, write anything, browse a file, see a note or a todo, or
// learn where on the disk any of it lives.
func (s *Server) registerShareRoutes(r chi.Router) {
	r.Route("/share/{token}", func(r chi.Router) {
		r.Use(s.requireShareToken)
		r.Get("/dashboard", s.handleShareDashboard)
	})
}

// requireShareToken resolves the capability in the URL.
//
// The allowlist is checked first, in the same order and by the same helper
// RequireAuth uses. A share link is reachable without signing in, so leaving
// --allow-from out of this path would make creating one a way to open the
// panel's front door to addresses the operator had excluded.
func (s *Server) requireShareToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ip := s.clientIP(r)

		if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
			s.auditFromOutside(ctx, "blocked", "", ip, "address not in the allowlist")
			writeErr(w, http.StatusForbidden, "not allowed from this address")
			return
		}

		token := chi.URLParam(r, "token")
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "this link is not valid")
			return
		}
		hash := auth.HashToken(token)
		link, err := s.DB.ShareLinkByToken(ctx, hash)
		if errors.Is(err, store.ErrNotFound) {
			// Revoked, expired, or never existed -- one answer for all three,
			// because telling them apart tells whoever is guessing which
			// guesses were close.
			//
			// Audited from outside: this route needs no credential to reach, so
			// an outsider decides how often it happens, and an ungated write
			// here is the same unbounded growth the allowlist rejection had.
			s.auditFromOutside(ctx, "share.rejected", "", ip, "unknown or expired share link")
			writeErr(w, http.StatusUnauthorized, "this link is not valid")
			return
		}
		if err != nil {
			// The same reasoning as RequireAuth's: the link may be perfectly
			// good and the panel simply cannot look it up. A 401 would make
			// the dashboard say "revoked" about a database hiccup, and there is
			// nobody at a wall display to know better.
			s.noteStale(err)
			s.Log.Warn("cannot check a share link", "err", err)
			writeErr(w, http.StatusServiceUnavailable,
				"the panel cannot reach its own database")
			return
		}

		// Best effort, and rate limited. A failed stamp must not fail the
		// request it was stamping.
		if s.shareCooldowns().Allow("share.touch", link.ID, time.Now()) {
			if terr := s.DB.TouchShareLink(ctx, link.ID); terr != nil {
				s.Log.Debug("touch share link", "err", terr)
			}
		}

		next.ServeHTTP(w, r.WithContext(
			context.WithValue(ctx, shareContextKey{}, shareContext{link: link, secret: hash})))
	})
}

func shareFrom(r *http.Request) (shareContext, bool) {
	sc, ok := r.Context().Value(shareContextKey{}).(shareContext)
	return sc, ok
}

// shareID renames a row for one link.
//
// The real session and project ids are what the authenticated API addresses
// rows by, and they end up in the environment of every process a session
// spawns. There is no reason a dashboard needs them: it needs something stable
// so React can key a list and so a row does not jump between polls. An HMAC
// under the link's own stored hash is stable for the life of the link,
// different for every other link, and says nothing about the panel.
func shareID(secret []byte, raw string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// shareMachine is the machine reading, with the path removed.
//
// A copy of sysmon.Sample's numbers rather than the Sample itself, because
// Sample carries DiskPath -- which is the panel's data directory, and so names
// a user account and a filesystem layout. Restating the fields is how the
// decision about each one becomes reviewable; embedding the sample would mean
// the next field added to it is disclosed by default.
type shareMachine struct {
	CPUReadable bool     `json:"cpuReadable"`
	CPUPercent  *float64 `json:"cpuPercent"`
	Cores       int      `json:"cores"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	MemTotal     uint64 `json:"memTotal"`
	MemAvailable uint64 `json:"memAvailable"`
	SwapTotal    uint64 `json:"swapTotal"`
	SwapFree     uint64 `json:"swapFree"`

	DiskTotal uint64 `json:"diskTotal"`
	DiskFree  uint64 `json:"diskFree"`

	Uptime int64 `json:"uptime"`
}

// shareMachineFrom copies the numbers across, one at a time.
//
// A function rather than a literal inside the response, so that the copying is
// the only thing it does and the whole of what a share link learns about the
// machine sits in one place. `Cores` falls back to the runtime's
// answer because a sampler that could not read /proc still knows how many
// processors this process was given, and a dashboard reading "0 cores" is
// worse than one reading the right number with an empty CPU bar beside it.
func shareMachineFrom(sample sysmon.Sample) shareMachine {
	out := shareMachine{
		CPUReadable: sample.CPUReadable, CPUPercent: sample.CPUPercent, Cores: sample.Cores,
		Load1: sample.Load1, Load5: sample.Load5, Load15: sample.Load15,
		MemTotal: sample.MemTotal, MemAvailable: sample.MemAvailable,
		SwapTotal: sample.SwapTotal, SwapFree: sample.SwapFree,
		DiskTotal: sample.DiskTotal, DiskFree: sample.DiskFree,
		Uptime: sample.Uptime,
	}
	if out.Cores == 0 {
		out.Cores = runtime.NumCPU()
	}
	return out
}

// shareSession is one row on the dashboard.
type shareSession struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	// Name is empty under store.ShareCounts. Empty rather than absent so the
	// client renders one shape either way instead of branching on undefined.
	Name  string        `json:"name"`
	State session.State `json:"state"`
	// Kind is "agent", "shell" or "other" -- a three-valued summary of the
	// pane's foreground process, never the command line itself. It is what
	// makes a wall display readable ("four agents, two shells") without
	// disclosing arguments an agent was invoked with.
	Kind string `json:"kind"`
	// StateChangedAt is what turns a triangle into "waiting 14 minutes", which
	// is the number this whole dashboard exists to put on a wall.
	StateChangedAt int64 `json:"stateChangedAt"`

	Exited     bool `json:"exited"`
	ExitStatus int  `json:"exitStatus"`

	// Measured says a usage reading was found for this row. A session whose
	// pane has gone is absent from /api/usage rather than zero, because zero is
	// a real reading, and that distinction has to survive the trip here.
	Measured   bool    `json:"measured"`
	CPUPercent float64 `json:"cpuPercent"`
	RSS        uint64  `json:"rss"`
	Procs      int     `json:"procs"`
}

// shareProject groups the rows. Under store.ShareCounts it has no name, only a
// pseudonymous id and its tallies, and the dashboard numbers the groups.
type shareProject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Waiting int    `json:"waiting"`
	Working int    `json:"working"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
}

type shareCounts struct {
	Projects int `json:"projects"`
	Sessions int `json:"sessions"`
	Waiting  int `json:"waiting"`
	Working  int `json:"working"`
	Done     int `json:"done"`
	Exited   int `json:"exited"`
	Crashed  int `json:"crashed"`
}

// shareDashboard is the whole response, and the whole of what a share link
// discloses. Read it as the list.
type shareDashboard struct {
	// At is when the server took this reading, in unix seconds. The dashboard
	// counts up from it, which is what stops a frozen page from looking like a
	// quiet system.
	At int64 `json:"at"`
	// Name is what the link was called when it was made -- typed by the owner,
	// so it is theirs to put on a wall, and it is the only free text here that
	// did not come from a machine.
	Name string `json:"name"`
	// Detail echoes which mode this link is in, so the page can say so rather
	// than leaving a reader to wonder why nothing has a name.
	Detail string `json:"detail"`
	// ExpiresAt is unix seconds, 0 when the link does not expire. Sent so the
	// page can warn before it goes dark rather than after.
	ExpiresAt int64 `json:"expiresAt"`

	// UsageReadable is false where there is no /proc to walk, which is a
	// different thing from every session being idle.
	UsageReadable bool `json:"usageReadable"`
	// Stale is true when the panel has stopped keeping its records up to date.
	// The string itself is not sent: it is an error message about this
	// machine's storage and a wall display can do nothing with it.
	Stale bool `json:"stale"`

	Machine  shareMachine   `json:"machine"`
	Counts   shareCounts    `json:"counts"`
	Projects []shareProject `json:"projects"`
	Sessions []shareSession `json:"sessions"`
}

// handleShareDashboard is everything a share token can ask for.
//
// It reads the same two sources the panel's own monitor does -- the session
// rows and the process-tree sampler -- and returns a redaction of them. It
// writes nothing, and the only state it touches is the sampler's previous
// counters, which is what makes a CPU percentage a percentage.
func (s *Server) handleShareDashboard(w http.ResponseWriter, r *http.Request) {
	sc, ok := shareFrom(r)
	if !ok {
		// Unreachable through the router: the middleware either sets this or
		// answers. Refusing rather than carrying on is the point -- a handler
		// that renders a dashboard when it cannot say which link asked has
		// lost the only thing that limits what it may say.
		writeErr(w, http.StatusUnauthorized, "this link is not valid")
		return
	}
	ctx := r.Context()

	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}

	named := store.ShareDetail(sc.link.Detail) == store.ShareNames
	sample := s.Sampler.Sample()

	out := shareDashboard{
		At: time.Now().Unix(), Name: sc.link.Name, Detail: sc.link.Detail,
		ExpiresAt: sc.link.ExpiresAt, UsageReadable: sysmon.ProcReadable(),
		Stale: s.stale() != "", Machine: shareMachineFrom(sample),
		Projects: []shareProject{}, Sessions: []shareSession{},
	}

	usage := s.shareUsage(ctx, sessions, out.UsageReadable)

	// Bottom terminals are left out, and that is a content decision rather than
	// a privacy one: they are scratch shells opened under a session, so a wall
	// showing them reports two rows for one task and counts a shell sitting at
	// a prompt as something that is "done". What they cost is still on screen,
	// in the machine meters above.
	byProject := map[string]*shareProject{}
	order := []string{}
	for _, row := range sessions {
		if row.ParentID != nil {
			continue
		}
		pid := shareID(sc.secret, row.ProjectID)
		grp, seen := byProject[pid]
		if !seen {
			grp = &shareProject{ID: pid}
			byProject[pid] = grp
			order = append(order, pid)
		}
		grp.Total++
		switch row.State {
		case session.StateWaiting:
			grp.Waiting++
			out.Counts.Waiting++
		case session.StateWorking:
			grp.Working++
			out.Counts.Working++
		default:
			grp.Done++
			out.Counts.Done++
		}
		if row.Exited {
			out.Counts.Exited++
			if row.ExitStatus != 0 && row.ExitStatus != store.ExitStatusVanished {
				out.Counts.Crashed++
			}
		}

		item := shareSession{
			ID:             shareID(sc.secret, row.ID),
			ProjectID:      pid,
			State:          row.State,
			Kind:           shareKind(row.Command),
			StateChangedAt: row.StateChangedAt,
			Exited:         row.Exited,
			ExitStatus:     row.ExitStatus,
		}
		if named {
			item.Name = row.Title
		}
		if u, have := usage[row.ID]; have {
			item.Measured = true
			item.CPUPercent = u.CPUPercent
			item.RSS = u.RSS
			item.Procs = u.Procs
		}
		out.Sessions = append(out.Sessions, item)
		out.Counts.Sessions++
	}

	// Project names, only for the groups that have a session in them. A
	// project with nothing running is not a row on a wall, and listing it would
	// disclose that it exists for no benefit.
	if named {
		for _, p := range projects {
			if grp, have := byProject[shareID(sc.secret, p.ID)]; have {
				grp.Name = p.Name
			}
		}
	}
	for _, pid := range order {
		out.Projects = append(out.Projects, *byProject[pid])
	}
	out.Counts.Projects = len(out.Projects)

	// No caching, anywhere between here and the screen. This is a live
	// reading, and a dashboard served from a proxy's cache is the exact failure
	// the connection indicator exists to make visible -- except that it would
	// look live, because the numbers would arrive.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// shareUsage samples per-session cost, tolerating a tmux that cannot answer.
//
// Split out so the handler reads as the redaction it is. Failure is empty
// rather than fatal, the same way /api/usage treats it: the meters go blank and
// everything else on screen is still true.
func (s *Server) shareUsage(ctx context.Context, sessions []store.Session, readable bool) map[string]sysmon.Usage {
	if !readable {
		return nil
	}
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return nil
	}
	pidOf := make(map[string]int, len(infos))
	for _, i := range infos {
		pidOf[i.Name] = i.PID
	}
	panes := make(map[string]int, len(sessions))
	for _, sess := range sessions {
		if pid, ok := pidOf[sess.TmuxName]; ok && pid > 0 {
			panes[sess.ID] = pid
		}
	}
	return s.TreeSampler.Sample(panes)
}

// shareKind summarises what is running in a pane without quoting it.
//
// Three values, from the two predicates internal/session already has. The
// command itself is never sent: it is `#{pane_current_command}` for a shell,
// and for anything else it is whatever the user typed, arguments included.
func shareKind(command string) string {
	switch {
	case session.IsAgentCommand(command):
		return "agent"
	case session.IsShellCommand(command):
		return "shell"
	default:
		return "other"
	}
}

// ─── management, behind the ordinary session ──────────────────────────────

// maxShareSeconds bounds how far ahead an expiry may be set.
//
// Not a security control -- a link with no expiry is offered and is the common
// case for a monitor on a desk. It is a guard against a typo in milliseconds
// producing a date in the year 50000, which renders as nonsense on the row
// meant to tell you when to stop worrying about the link.
//
// Counted in seconds rather than as a time.Duration, deliberately. The obvious
// spelling is `time.Duration(req.ExpiresIn) * time.Second > 365*24*time.Hour`,
// and that multiplication is int64 nanoseconds: a caller sending 10^18 wraps it
// negative, sails past the check, and lands in time.Add with a value that wraps
// again. Comparing the seconds the caller actually sent cannot overflow.
const maxShareSeconds = 365 * 24 * 60 * 60

func (s *Server) registerShareAdminRoutes(r chi.Router) {
	r.Get("/settings/shares", s.handleListShares)
	r.Post("/settings/shares", s.handleCreateShare)
	r.Delete("/settings/shares/{shareID}", s.handleDeleteShare)
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	links, err := s.DB.ListShareLinks(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(links))
}

type createShareRequest struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// ExpiresIn is seconds from now, or 0 for a link that does not expire. A
	// duration rather than an instant because the client has no reason to be
	// trusted about what time it is, and "two weeks" is what a person means.
	ExpiresIn int64 `json:"expiresIn"`
}

// handleCreateShare mints a link, and is the only time its token is readable.
//
// The same shape as an API token and for the same reason: the database keeps a
// SHA-256, so this response is the only copy. The settings page says so before
// the button rather than after.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req createShareRequest
	if !decode(w, r, &req) {
		return
	}
	// TruncateTitle, not name[:64]: this string is a heading on the dashboard,
	// and a byte slice through a multi-byte character renders the last one as
	// U+FFFD. The token endpoint next door still cuts by bytes; its names are
	// only ever shown in a settings row, which is not an excuse so much as the
	// reason nobody has noticed.
	name := session.TruncateTitle(strings.TrimSpace(req.Name))
	if name == "" {
		name = "dashboard"
	}

	// Default to the quieter mode. A default that discloses names would be one
	// nobody chose, on a screen chosen because other people can see it.
	detail := store.ShareDetail(strings.TrimSpace(req.Detail))
	if detail == "" {
		detail = store.ShareCounts
	}
	if !store.ValidShareDetail(detail) {
		writeErr(w, http.StatusBadRequest, "detail must be counts or names")
		return
	}
	if req.ExpiresIn < 0 || req.ExpiresIn > maxShareSeconds {
		writeErr(w, http.StatusBadRequest, "expiresIn must be between 0 and a year, in seconds")
		return
	}
	var expiresAt int64
	if req.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(req.ExpiresIn) * time.Second).Unix()
	}

	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	rec, err := s.DB.CreateShareLink(r.Context(), id.New(), auth.HashToken(token), prefix, name,
		detail, u.ID, expiresAt)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "share.created", u.Username, s.clientIP(r), name+" ("+string(detail)+")")
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"id":        rec.ID,
		"name":      rec.Name,
		"prefix":    rec.Prefix,
		"detail":    rec.Detail,
		"expiresAt": rec.ExpiresAt,
		"createdAt": rec.CreatedAt,
	})
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	// linkID, not shareID: shareID is the function above that renames a row for
	// a link, and shadowing it here would compile and mean something else.
	linkID := chi.URLParam(r, "shareID")
	if err := s.DB.DeleteShareLink(r.Context(), linkID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "share.revoked", u.Username, s.clientIP(r), linkID)
	}
	w.WriteHeader(http.StatusNoContent)
}
