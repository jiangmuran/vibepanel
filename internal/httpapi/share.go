package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/usage"
)

// A read-only share link is a second door onto a panel whose first door is one
// password in front of a writable terminal. Everything in this file exists to
// make that door narrow enough to be worth having.
//
// Four properties, and each is enforced by structure rather than by care:
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
//
// The fourth arrived with boards, and it is the one an edit here is most
// likely to lose:
//
//	4. A board can only ever subtract. The sections a dashboard may carry are
//	   the structs below and nothing else; a board chooses among them and has
//	   no vocabulary for anything that is not one of them. There is no widget
//	   that names a table, a range, a directory or a field -- every option on
//	   one is an enum or a bounded number, checked against store's registry
//	   when it is stored and again when it is read back.
//
//	   So the question "which arrangement of widgets discloses more" has no
//	   answer: the widest board and the narrowest board differ only in how much
//	   of the same fixed set is written. What a link may say is still decided
//	   once, at creation, by `detail`, by somebody signed in -- and store's
//	   UpdateShareLink deliberately cannot change it afterwards.
//
//	   Whether a section is computed at all does follow from whether the board
//	   asked for it, and that is a cost decision rather than a permission one:
//	   the spend rollups are five GROUP BYs over a year of history and a wall
//	   polls every two seconds. It stays a cost decision only while every
//	   section remains a fixed struct in this file. A widget that carried a
//	   *parameter* into a query rather than a choice among precomputed answers
//	   would turn it into a permission one, which is the edit to refuse.

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
//
// It stayed one route through the change that made a board editable from
// somewhere else, and that is worth recording because the obvious design was
// the other one. "Let the person at the screen rearrange it" wants a PATCH
// here, one line, obviously correct in review. What was actually asked for was
// "I should not have to walk to the wall and log in to change it" — which is an
// *owner* editing a board from a laptop, over a route that already exists
// behind a session. The wall picks the change up on its next poll, two seconds
// later, because every poll re-reads the row. Nothing a viewer sends decides
// anything: `v`, `w` and `h` on the query string are recorded in memory for the
// owner's count and are not readable through this token at all.
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

		// Who is looking, counted from the poll rather than from anything the
		// viewer had to be given permission to send. Hashed under the link's
		// own secret so the book holds nothing about who: the same viewer on
		// two links is two unrelated keys, which is the property shareID
		// already exists for.
		raw, vw, vh := shareViewerFrom(r, ip)
		s.viewers.saw(link.ID, shareID(hash, raw), vw, vh, time.Now())

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
	// DoneToday is how many sessions reached "done" since local midnight.
	DoneToday int `json:"doneToday"`
	// LongestWaitAt is when the session that has been waiting longest entered
	// that state, in unix seconds, or 0 when nothing is waiting.
	//
	// Here rather than derived on the client from the row list, because the row
	// list is what a board omits when nothing on it shows rows -- and "how long
	// has the oldest one been waiting" is the number the single-figure boards
	// exist for. A count with no rows behind it still has to answer it.
	LongestWaitAt int64 `json:"longestWaitAt"`
}

// shareSpendTotals is one bucket of token spend.
//
// Tokens, never money. Prices differ per model, per tier and over time, and a
// currency figure derived from a stale table is a confident-looking wrong
// number on a wall -- the same decision the token panel already made.
type shareSpendTotals struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Requests   int64 `json:"requests"`
	// Total is the four token columns added up. Sent rather than summed on the
	// client so that every board showing "what did it cost" is showing the same
	// arithmetic.
	Total int64 `json:"total"`
}

// shareSpendBucket is one labelled column of a chart: a day or a month.
//
// Label is a date, "2026-08-23" or "2026-08". Not a formatted string: the
// browser knows the reader's language and the server does not.
type shareSpendBucket struct {
	Label    string `json:"label"`
	Total    int64  `json:"total"`
	Requests int64  `json:"requests"`
	// The four columns behind Total, so a bar can be stacked.
	//
	// The same tokens the totals above already disclose, cut the same way; no
	// new fact reaches the wire because a board asked for a stacked chart. It
	// is here because a total hides the thing worth seeing: a day that is nine
	// tenths cache reads and a day that is nine tenths output are the same
	// number and different afternoons.
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// shareTrendPoint is one reading of the machine and the running token total.
//
// Restated rather than embedding sysmon.Sample, exactly like shareMachine: a
// field added to the sample must not become a field on a line drawn on a wall.
type shareTrendPoint struct {
	At int64 `json:"at"`
	// CPU is whole-machine percent, or null where /proc could not be read. Null
	// rather than zero, because zero is a real and very different reading.
	CPU    *float64 `json:"cpu"`
	Memory float64  `json:"memory"`
	Load   float64  `json:"load"`
	// Tokens is the running total for the server's local day, within this
	// link's scope. The differences are what a rate is drawn from; the total is
	// what survives a dropped sample, and a difference would not.
	Tokens int64 `json:"tokens"`
}

// shareTrend is the last few minutes, for the widgets that draw a line.
//
// The one section that is not a reading of now. It exists because a wall of
// still numbers cannot be told from a wall that has frozen, and a line that
// moves is the cheapest honest proof the screen is alive.
type shareTrend struct {
	// Every is the sampling interval in seconds, so the client can say what the
	// horizontal axis means without guessing it from the gaps.
	Every int `json:"every"`
	// Points is oldest first, and short: a wall that has just been switched on
	// draws a short line, because the ring is filled by the polls that draw it.
	Points []shareTrendPoint `json:"points"`
}

// shareSpendGroup is one bar of a by-tool or by-project breakdown.
//
// ID for a project is the same pseudonym the session rows carry, so a spend bar
// and a session group with the same id are the same project without either of
// them being the panel's real id. For a tool it is the agent's name, which is
// one of a fixed set this panel can read and names nothing of the user's. Name
// is empty under store.ShareCounts, exactly like every other name here.
type shareSpendGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Total    int64  `json:"total"`
	Requests int64  `json:"requests"`
}

// shareSpend is what the agents recorded spending, redacted.
//
// What is not here is the point, again: no transcript text, no agent session
// ids, no working directories. The token panel discloses all three to a
// signed-in owner and none of them has a use on a wall.
type shareSpend struct {
	// Readable is false until a pass over the transcripts has finished. That
	// is a different fact from "nothing was spent", and a zero rendered for the
	// first is the whole failure this flag exists to prevent.
	Readable  bool  `json:"readable"`
	ScannedAt int64 `json:"scannedAt"`
	// Date is the server's local day. The buckets are local days, so a phone in
	// another timezone must not decide for itself which square is today.
	Date string `json:"date"`
	// HoursToday is how far into the server's local day it is.
	//
	// Here so a rate can be "so far today" rather than a guess. The browser
	// does not know the server's timezone, and one computed there would be the
	// rate of a different day for a reader in another one.
	HoursToday float64 `json:"hoursToday"`
	// WindowDays is the range the grouped figures below cover.
	WindowDays int `json:"windowDays"`

	Today shareSpendTotals `json:"today"`
	// Yesterday and LastMonth are what makes Today and Month mean anything. A
	// total says what; a comparison says whether that is a lot.
	Yesterday shareSpendTotals `json:"yesterday"`
	Month     shareSpendTotals `json:"month"`
	LastMonth shareSpendTotals `json:"lastMonth"`
	Window    shareSpendTotals `json:"window"`
	// AllTime is every token this panel has ever recorded within this scope.
	//
	// Summed from the months, which are already every month there has ever
	// been, so it costs nothing beyond the addition. It is the only figure here
	// that only ever goes up, which is exactly what an odometer needs and what
	// no chart of a window can say.
	AllTime shareSpendTotals `json:"allTime"`

	// The arrays are empty unless a widget on this board asks for them. Empty
	// rather than absent so the client renders one shape either way.
	Days     []shareSpendBucket `json:"days"`
	Months   []shareSpendBucket `json:"months"`
	Heatmap  []shareSpendBucket `json:"heatmap"`
	Tools    []shareSpendGroup  `json:"tools"`
	Models   []shareSpendGroup  `json:"models"`
	Projects []shareSpendGroup  `json:"projects"`
}

// shareTodos is how much of each project's checklist is finished.
//
// Counts, and only counts. A todo line says what somebody is about to do about
// a customer, a bug or a deadline; it is closer to a note than to a session
// title, and neither detail mode offers it. What a wall wants from a checklist
// is the fraction, and the fraction is here.
type shareTodos struct {
	Open int `json:"open"`
	Done int `json:"done"`
	// ClosedToday is what came out today, which is the half a board of costs
	// alone leaves off.
	ClosedToday int                 `json:"closedToday"`
	Projects    []shareTodosProject `json:"projects"`
}

type shareTodosProject struct {
	// ID is the same pseudonym the session groups carry, so a checklist and a
	// group of sessions can be lined up without either being a real id.
	ID          string `json:"id"`
	Name        string `json:"name"`
	Open        int    `json:"open"`
	Done        int    `json:"done"`
	ClosedToday int    `json:"closedToday"`
}

// ─── how the day went ─────────────────────────────────────────────────────

// shareFlowTotals is how many transitions of each kind happened in a span.
//
// Out of the session-event log, which is what made a trend on this dashboard
// possible at all: before it the panel stored one `state_changed_at` per
// session and nothing about what came before, so every widget with a time axis
// had a single current number to draw.
//
// Transitions, never a census. A row in that log says a session left one state
// for another; nothing says how many were waiting at 14:00, and reconstructing
// that from the flow needs a starting count and every event since, which the
// first dropped write makes silently wrong. See internal/store/events.go.
type shareFlowTotals struct {
	Started  int `json:"started"`
	Waited   int `json:"waited"`
	Finished int `json:"finished"`
	// WaitSeconds and WaitEnded are the dwell of every wait that *ended* in the
	// span, and how many ended. Two numbers rather than an average, so an empty
	// span is empty rather than a zero-second wait -- and so the client's
	// arithmetic is the same one everywhere.
	WaitSeconds int64 `json:"waitSeconds"`
	WaitEnded   int   `json:"waitEnded"`
}

// shareFlowBucket is one interval of it.
//
// The five figures are written out rather than embedding shareFlowTotals, and
// that is not tidiness: the wire test walks exported fields, an embedded
// unexported type is not one, and the section would have been the one part of
// this response with nothing checking that the server and wire.ts still agree.
type shareFlowBucket struct {
	At          int64 `json:"at"`
	Started     int   `json:"started"`
	Waited      int   `json:"waited"`
	Finished    int   `json:"finished"`
	WaitSeconds int64 `json:"waitSeconds"`
	WaitEnded   int   `json:"waitEnded"`
}

func bucketOfFlow(at int64, t shareFlowTotals) shareFlowBucket {
	return shareFlowBucket{At: at, Started: t.Started, Waited: t.Waited,
		Finished: t.Finished, WaitSeconds: t.WaitSeconds, WaitEnded: t.WaitEnded}
}

// shareFlow is the session-event log, bucketed.
//
// No id of any kind. A bucket is five counts and a timestamp, so this section
// carries nothing that could name a session, a project or a person -- which is
// why it is sent under both detail modes.
type shareFlow struct {
	// Every is the bucket width in seconds, so the client can label the axis
	// without inferring it from the gaps.
	Every int `json:"every"`
	// Since is the start of the first bucket, unix seconds.
	Since int64 `json:"since"`
	// WindowDays is what Window covers. Today is always the server's local day.
	WindowDays int               `json:"windowDays"`
	Today      shareFlowTotals   `json:"today"`
	Window     shareFlowTotals   `json:"window"`
	Buckets    []shareFlowBucket `json:"buckets"`
}

// shareFeedEntry is one thing that happened.
//
// Exactly the fields a session row already carries -- the per-link pseudonym,
// the state, the time -- in the order they happened. That is the whole of the
// disclosure argument: a feed of these adds no new fact to the wire, it
// re-serves facts the dashboard already sends as counts, and it is the cheapest
// honest way to make a television look alive rather than left on.
type shareFeedEntry struct {
	At        int64  `json:"at"`
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId"`
	// Name is the session's title, empty under store.ShareCounts and also empty
	// for a session that has since been deleted -- the log outlives the row on
	// purpose, so a feed entry with no name is an ordinary thing rather than a
	// bug.
	Name       string        `json:"name"`
	From       session.State `json:"from"`
	To         session.State `json:"to"`
	ForSeconds int64         `json:"forSeconds"`
}

type shareFeed struct {
	Entries []shareFeedEntry `json:"entries"`
}

// ─── what was built ───────────────────────────────────────────────────────

// shareRepoTotals is production, counted.
//
// Commits, changed lines and files touched. These replaced "sessions finished
// today" and "todos ticked today" as the headline figures on a wall, and the
// reason is that both of those were self-reported: a todo is ticked because
// somebody remembered to, and a session reaches `done` because an agent's hook
// said so. On a real board both read 0 beside a four-figure request count.
// These are things that exist now and did not this morning.
//
// Added and Removed are two numbers and never a net one. +1200/-800 is a
// different day from +400/-0, and a net figure hides a refactor completely.
type shareRepoTotals struct {
	Commits int `json:"commits"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Files   int `json:"files"`
}

// shareRepoDay is one local day of it. Label is "2026-08-27" on the server's
// clock, the same convention the spend buckets use.
//
// Written out rather than embedding shareRepoTotals, for the reason
// shareFlowBucket gives.
type shareRepoDay struct {
	Label   string `json:"label"`
	Commits int    `json:"commits"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Files   int    `json:"files"`
}

func (d *shareRepoDay) add(t shareRepoTotals) {
	d.Commits += t.Commits
	d.Added += t.Added
	d.Removed += t.Removed
	d.Files += t.Files
}

// shareRepoProject is one project's production, renamed for this link.
type shareRepoProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Repo is false for a project directory that is not a working tree. Sent
	// rather than omitted so the widget can say "not a repository" -- which has
	// to look deliberate rather than broken, because it is.
	Repo   bool            `json:"repo"`
	Today  shareRepoTotals `json:"today"`
	Window shareRepoTotals `json:"window"`
	// Ahead and Behind are against the branch's own upstream; Dirty is how many
	// paths git has something to say about.
	//
	// Counts only. A branch *name* is a feature name and often a ticket or a
	// customer, so it is refused here at both detail settings even though the
	// git tab shows it to a signed-in owner -- the same distinction that keeps
	// a commit count on the wire and a commit subject off it.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	Dirty  int `json:"dirty"`
}

// shareRepoPRs is a repository's pull requests, as counts.
//
// Restated rather than embedding git.PRSummary, exactly like shareMachine: a
// field added to that struct must not become a field on a wall. What is
// deliberately absent is every part of a pull request that is text -- title,
// number, author, branch, URL. A count says how much is in flight; a title says
// what somebody is building for whom.
type shareRepoPRs struct {
	// Readable is false until the first fetch has finished, or when there is no
	// token in the panel's environment. Different from "no pull requests are
	// open", and a zero drawn for the first is the failure this flag exists to
	// prevent.
	Readable bool `json:"readable"`
	// AgeSeconds is how old the answer is. On screen, because there is no
	// poller behind it: it is refreshed in the background at most once every
	// few minutes, and without a age a list looks live.
	AgeSeconds       int64 `json:"ageSeconds"`
	Open             int   `json:"open"`
	Draft            int   `json:"draft"`
	Green            int   `json:"green"`
	Red              int   `json:"red"`
	Pending          int   `json:"pending"`
	Approved         int   `json:"approved"`
	ChangesRequested int   `json:"changesRequested"`
	MergedToday      int   `json:"mergedToday"`
	// MergedPartial says the merge count is a floor: every one of the most
	// recent merges this build asks about was today, so there may be more.
	MergedPartial bool `json:"mergedPartial"`
}

// shareRepo is what the working trees say, redacted.
//
// The first section on this surface that is read from a disk rather than from
// the database, and the list of what it does *not* carry is the part worth
// reading: no path, no filename, no branch name, no commit subject, no sha, no
// author. `git log --shortstat` is what produces it, and that is a disclosure
// decision rather than a parsing convenience -- `--numstat` would carry every
// changed filename in the repository through this process on its way here, and
// `%s` would carry the messages. Neither is asked for, so neither is read.
//
// Sent under both detail modes. A commit count names nobody; the project names
// beside it follow `named`, exactly like every other group on this dashboard.
type shareRepo struct {
	// Readable is false until the first background read has finished. Zero and
	// "not counted yet" are different facts about a repository and the second
	// one is said out loud.
	Readable bool `json:"readable"`
	// AgeSeconds is how old the reading is, or -1 when there is none. A wall
	// polls every two seconds; this is refreshed every ninety, so the figure is
	// shown with its age rather than presented as this second's.
	AgeSeconds int64 `json:"ageSeconds"`
	// Repos is how many of the projects in scope are working trees, and
	// Projects is how many were looked at. A panel whose projects are not
	// checkouts shows zeroes, and these two are what let the widget say why.
	Repos      int `json:"repos"`
	Projects   int `json:"projects"`
	WindowDays int `json:"windowDays"`

	Today  shareRepoTotals `json:"today"`
	Window shareRepoTotals `json:"window"`
	// Days is empty unless a widget on this board draws a series.
	Days []shareRepoDay `json:"days"`
	// ByProject is empty unless a widget on this board ranks them.
	ByProject []shareRepoProject `json:"byProject"`
	// PRs is null unless a widget on this board shows pull requests.
	PRs *shareRepoPRs `json:"prs"`
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
	// Remark is the owner's own label for this screen: the room it is in, the
	// audience it is for. Written by somebody signed in, read by whoever is
	// standing in front of it.
	//
	// Sent under both detail modes, and that is a decision rather than an
	// omission. `detail` governs whether the *panel's* words -- session titles
	// and project names, read out of its own database -- may leave the machine.
	// A remark is not the panel's: it is a sentence the owner wrote to the
	// viewer, about the screen, with the effect in front of them. `name` has
	// always been sent in both modes for the same reason, and a remark
	// suppressed under `counts` would be a label the owner cannot see on the
	// wall they labelled -- which they would then put in `name`, which is
	// disclosed anyway. The settings page says so where it is typed.
	Remark string `json:"remark"`
	// Locked says the owner has fixed this board.
	//
	// Sent so the screen can say it -- the lock exists so that a wall a
	// customer is looking at is not the one you rearrange by accident, and it
	// is enforced where the edit happens, in handleUpdateShare. Nothing here
	// depends on the viewer honouring it, because there is nothing for a viewer
	// to honour: a board is not editable from this side at all.
	Locked bool `json:"locked"`
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

	// Board is the arrangement this link opens, as it was stored and after the
	// read path has dropped anything it does not recognise. Sent back rather
	// than kept on the server so the page draws what the link says and nothing
	// else -- there is no second copy of the layout in the frontend to drift
	// from this one.
	Board store.Board `json:"board"`

	Machine  shareMachine   `json:"machine"`
	Counts   shareCounts    `json:"counts"`
	Projects []shareProject `json:"projects"`
	// Sessions is empty unless a widget on this board shows rows. A board that
	// is one number does not carry a list of every session to draw it.
	Sessions []shareSession `json:"sessions"`
	// Spend is null unless a widget on this board shows token spend.
	//
	// Null rather than a zeroed object: "this board does not show spend" and
	// "this panel spent nothing" are different facts, and the second one has a
	// Readable flag of its own to tell it from "nothing has been counted yet".
	Spend *shareSpend `json:"spend"`
	// Todos is null unless a widget on this board shows checklist progress.
	Todos *shareTodos `json:"todos"`
	// Trend is null unless a widget on this board draws a moving line.
	Trend *shareTrend `json:"trend"`
	// Flow is null unless a widget on this board draws how the day went, and
	// Feed unless one lists what just happened. Both come out of the
	// session-event log.
	Flow *shareFlow `json:"flow"`
	Feed *shareFeed `json:"feed"`
	// Repo is null unless a widget on this board shows what was built. It is
	// the only section read from a disk rather than from the database, and it
	// is computed from a background-refreshed cache -- a wall polling every two
	// seconds must never be the thing that runs `git log`.
	Repo *shareRepo `json:"repo"`

	// Scope is "", "project" or "session": what this link is about.
	//
	// Echoed so the page can say it is one project's board rather than the
	// panel's, which is the difference between "nothing is running" and
	// "nothing is running in the thing you were sent".
	Scope string `json:"scope"`
	// ScopeName is the scoped project's or session's name under `names`, and
	// empty under `counts` like every other name here. Also empty when the
	// scoped row no longer exists, which is the same thing the empty board
	// below is already saying.
	ScopeName string `json:"scopeName"`

	// ScopeRepoOwner and ScopeRepoName are the scoped project's repository, as
	// two parsed halves and nothing else.
	//
	// Three narrowings, and each is the disclosure decision rather than styling.
	//
	// **Only under `names`.** A repository is a name, and a public resolvable one
	// that also names the organisation. Under `counts` this dashboard sends no
	// names at all, and github.com/acme-holdings/payroll identifies the customer
	// at least as precisely as the project path that mode exists to withhold. So
	// it is gated by the same `named` that gates ScopeName, and the redaction
	// test asserts both halves are empty and "github.com" appears nowhere in a
	// counts-mode body.
	//
	// **Only for a project-scoped link.** A session-scoped link's ScopeName is a
	// session title; hanging a repository off it would disclose the project a
	// session belongs to on a link narrowed to one session. A whole-panel link
	// has no single repository to name.
	//
	// **Never the remote URL and never the path.** Two halves, so the viewer's
	// browser can build https://github.com/{owner}/{name} and nothing else.
	// Sending the raw remote would hand a viewer an ssh URL with a hostname in
	// it, which is the class of thing this struct exists to restate rather than
	// forward.
	//
	// This is the first thing on the share surface that reads a working tree.
	// docs/api.md said the surface never does; it now says when it does and why.
	// One `git remote get-url` behind the cache the repository tab already uses,
	// and none at all in counts mode.
	ScopeRepoOwner string `json:"scopeRepoOwner"`
	ScopeRepoName  string `json:"scopeRepoName"`
}

// handleShareDashboard is everything a share token can ask for.
//
// It reads the same two sources the panel's own monitor does -- the session
// rows and the process-tree sampler -- and returns a redaction of them. It
// writes nothing to the database, and the only state it touches is the
// sampler's previous counters, which is what makes a CPU percentage a
// percentage, and the in-memory viewer book and trend ring next door.
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
	out, err := s.buildShareDashboard(r.Context(), sc.link, sc.secret)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// No caching, anywhere between here and the screen. This is a live
	// reading, and a dashboard served from a proxy's cache is the exact failure
	// the connection indicator exists to make visible -- except that it would
	// look live, because the numbers would arrive.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// buildShareDashboard is the redaction itself, with no HTTP in it.
//
// Split out from the handler so that the editor's preview draws from *this*
// function rather than from a second one. An owner composing a wall from a
// laptop has to be shown what that wall shows, and the way to get that wrong is
// to write a second reduction of the panel's state for the preview: it would
// diverge on the first field added to either, and the direction it diverges in
// is "the preview shows something the real screen does not".
//
// secret is what the pseudonymous ids are derived from. For a real poll it is
// the presented token's stored hash; for the preview it is the link's id, so a
// preview's ids are stable, different from the live link's, and join to
// nothing. Nothing downstream cares which it was, which is the point.
func (s *Server) buildShareDashboard(ctx context.Context, link store.ShareLink,
	secret []byte) (shareDashboard, error) {
	sc := shareContext{link: link, secret: secret}

	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return shareDashboard{}, err
	}
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		return shareDashboard{}, err
	}

	named := store.ShareDetail(sc.link.Detail) == store.ShareNames
	sample := s.Sampler.Sample()

	// What the board asks for, as a set of section names. It can only ever
	// subtract: every section is a fixed struct in this file, and a widget
	// chooses among them rather than describing one. See property 4 at the top.
	board := sc.link.Board
	needs := board.Needs()

	// What this link is about, resolved from its own row. Never from the
	// request: a scope a caller could name is a scope a caller could change.
	scope := resolveScope(sc.link, projects, sessions)

	out := shareDashboard{
		At: time.Now().Unix(), Name: sc.link.Name, Detail: sc.link.Detail,
		Remark: sc.link.Remark, Locked: sc.link.Locked,
		ExpiresAt: sc.link.ExpiresAt, UsageReadable: sysmon.ProcReadable(),
		Stale: s.stale() != "", Machine: shareMachineFrom(sample), Board: board,
		Scope: string(scope.kind), Projects: []shareProject{}, Sessions: []shareSession{},
	}
	if named {
		out.ScopeName = scope.name
		out.ScopeRepoOwner, out.ScopeRepoName = s.shareRepoFor(ctx, scope)
	}
	// Today, on the server's clock. The browser's would put "closed today" and
	// "finished today" on a different day for a reader in another timezone.
	dayStart := startOfLocalDay(time.Now())

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
		if !scope.covers(row) {
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
			// The oldest wait, kept whether or not the rows themselves are
			// sent. A board that is one number still has to be able to say how
			// long that number has been true.
			if !row.Exited && row.StateChangedAt > 0 &&
				(out.Counts.LongestWaitAt == 0 || row.StateChangedAt < out.Counts.LongestWaitAt) {
				out.Counts.LongestWaitAt = row.StateChangedAt
			}
		case session.StateWorking:
			grp.Working++
			out.Counts.Working++
		default:
			grp.Done++
			out.Counts.Done++
			// What finished today, as opposed to what is finished. A board of
			// costs alone reads as an expense report; this is the other half,
			// and it is the closest thing the panel honestly has to output.
			// It counts sessions that *reached* done today -- a session that
			// finished last week and has sat there since is not today's work.
			if row.StateChangedAt >= dayStart {
				out.Counts.DoneToday++
			}
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
		if needs[store.NeedSessions] {
			out.Sessions = append(out.Sessions, item)
		}
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

	if needs[store.NeedSpend] {
		spend := s.shareSpendFor(ctx, projects, sc.secret, named, needs, board, scope)
		out.Spend = &spend
	}
	if needs[store.NeedTodos] {
		todos := s.shareTodosFor(ctx, projects, sc.secret, named, scope, dayStart)
		out.Todos = &todos
	}
	if needs[store.NeedFlow] {
		flow := s.shareFlowFor(ctx, scope, board, time.Now(), dayStart)
		out.Flow = &flow
	}
	if needs[store.NeedFeed] {
		feed := s.shareFeedFor(ctx, sessions, sc.secret, named, scope, dayStart)
		out.Feed = &feed
	}
	if needs[store.NeedRepo] || needs[store.NeedRepoDays] || needs[store.NeedRepoPRs] {
		// Nothing here runs a process. Every figure comes from the warm cache
		// in internal/git, which refreshes behind the request; a wall polling
		// every two seconds must never be the thing that runs `git log`, and
		// the version of this that called ReadActivity directly would be one
		// fork per project per poll, forever.
		repo := s.shareRepoWork(ctx, projects, sc.secret, named, needs, board, scope, dayStart)
		out.Repo = &repo
	}
	if needs[store.NeedTrend] {
		// Sampled here rather than by the poller, and only when a widget on
		// this board draws it. A panel nobody is watching does no work for a
		// graph nobody is looking at -- the same rule the token ingester
		// already follows.
		//
		// The token half comes from the spend section when the board carries
		// one and is zero otherwise, which is honest: a board with a machine
		// line and no spend widget has no token figures to make a rate from,
		// and inventing one by querying anyway would be five GROUP BYs a poll
		// for a line nothing draws.
		var today int64
		if out.Spend != nil {
			today = out.Spend.Today.Total
		}
		trend := shareTrend{Every: int(trendSampleEvery / time.Second),
			Points: []shareTrendPoint{}}
		for _, pt := range s.observeTrend(scope.cwd, time.Now(),
			trendFrom(time.Now(), sample, today)) {
			p := shareTrendPoint{At: pt.at, Memory: pt.memory, Load: pt.load, Tokens: pt.tokens}
			if pt.cpu >= 0 {
				cpu := pt.cpu
				p.CPU = &cpu
			}
			trend.Points = append(trend.Points, p)
		}
		out.Trend = &trend
	}
	return out, nil
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

// ─── scope: which rows this link is about ─────────────────────────────────

// scopeOf is a link's scope, resolved against the rows that exist right now.
//
// Resolved on every request rather than at creation, because the project or
// session it names can be deleted, and what happens then is the whole reason
// this is a type rather than two strings passed around. A scope that resolves
// to nothing must show nothing. The failure it exists to prevent has one shape
// everywhere: an empty id, an empty path, an empty filter -- and an empty
// filter means "everything". A link sent to one collaborator about one project
// would quietly become a view of every project on the machine on the day
// somebody deleted that project.
type scopeOf struct {
	kind store.ShareScope
	// projectID and sessionID are the panel's real ids, used only to compare
	// against rows here. Neither is ever written to the response.
	projectID string
	sessionID string
	// sessionProjectID is the project a session-scoped link's session sits in,
	// which is what its checklist and its spend are attributed to.
	sessionProjectID string
	// cwd is the scoped project's directory, which is what narrows the spend
	// rollups. Empty for a whole-panel link -- and also empty for a scoped link
	// whose target is gone, which is why every caller has to check `kind`
	// before treating an empty cwd as "no filter".
	cwd string
	// name is the scoped row's own name, disclosed only under `names`.
	name string
	// missing says the scope names a row that is not there any more.
	missing bool
}

// covers reports whether one session row is inside this scope.
func (s scopeOf) covers(row store.Session) bool {
	switch s.kind {
	case store.ShareProject:
		return s.projectID != "" && row.ProjectID == s.projectID
	case store.ShareSession:
		return s.sessionID != "" && row.ID == s.sessionID
	default:
		return true
	}
}

// coversProject reports whether one project is inside this scope.
//
// A session-scoped link covers the project that session is in, because the
// checklist of the project somebody is collaborating on is the context for the
// one piece of work they were sent. It does not cover any other project.
func (s scopeOf) coversProject(id string) bool {
	switch s.kind {
	case store.ShareProject:
		return s.projectID != "" && id == s.projectID
	case store.ShareSession:
		return s.sessionProjectID != "" && id == s.sessionProjectID
	default:
		return true
	}
}

// resolveScope turns a link's stored scope into something the handler can use.
func resolveScope(link store.ShareLink, projects []store.Project,
	sessions []store.Session) scopeOf {
	out := scopeOf{kind: store.ShareScope(link.Scope)}
	switch out.kind {
	case store.ShareProject:
		out.projectID = link.ScopeID
		for _, p := range projects {
			if p.ID == link.ScopeID {
				out.cwd, out.name = p.Path, p.Name
				return out
			}
		}
		out.missing = true
	case store.ShareSession:
		out.sessionID = link.ScopeID
		for _, row := range sessions {
			if row.ID != link.ScopeID {
				continue
			}
			out.name, out.sessionProjectID = row.Title, row.ProjectID
			for _, p := range projects {
				if p.ID == row.ProjectID {
					out.cwd = p.Path
				}
			}
			return out
		}
		out.missing = true
	}
	return out
}

// shareRepoFor is the scoped project's repository, as owner and name.
//
// Empty for everything that is not a project-scoped link, for a directory that
// is not a checkout, for a repository nobody has pushed, and for any remote
// internal/git will not vouch for as github.com. Four ways to get nothing, and
// the page renders the project's name alone for all of them -- which is what a
// project without a repository should look like.
//
// The parse is not repeated here. Remote.GitHub() is the one place in this
// product allowed to decide what a remote string means, and a second answer to
// that question on the disclosure path is how the two drift.
//
// Called only under `named`, which is what keeps a counts-mode board from
// reading a working tree at all.
func (s *Server) shareRepoFor(ctx context.Context, scope scopeOf) (string, string) {
	if scope.kind != store.ShareProject || scope.missing || scope.cwd == "" {
		return "", ""
	}
	snap, err := s.Git.Read(ctx, scope.cwd, 0)
	if err != nil || !snap.HasRemote || !snap.Remote.GitHub() {
		return "", ""
	}
	return snap.Remote.Owner, snap.Remote.Name
}

// startOfLocalDay is midnight this morning, on the server's clock, in unix
// seconds.
func startOfLocalDay(now time.Time) int64 {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

// ─── checklists, counted ──────────────────────────────────────────────────

// shareTodosFor counts every scoped project's checklist.
//
// Never the items themselves. A todo line is prose about work -- a customer, a
// bug, a date -- and it is the one piece of user text that neither detail mode
// offers, because a wall wants the fraction and the fraction gives nothing
// away.
func (s *Server) shareTodosFor(ctx context.Context, projects []store.Project, secret []byte,
	named bool, scope scopeOf, dayStart int64) shareTodos {
	out := shareTodos{Projects: []shareTodosProject{}}
	if scope.kind != store.ShareWhole && scope.projectID == "" && scope.sessionID == "" {
		return out
	}
	rows, err := s.DB.TodoProgressByProject(ctx, dayStart)
	if err != nil {
		// Empty rather than fatal: a checklist that cannot be counted leaves a
		// gap, and the rest of the screen is still true.
		s.Log.Debug("share todo progress", "err", err)
		return out
	}
	for _, p := range projects {
		if !scope.coversProject(p.ID) {
			continue
		}
		row, have := rows[p.ID]
		if !have {
			// A project with no checklist at all is left out rather than shown
			// as 0/0, which renders as a finished list.
			continue
		}
		item := shareTodosProject{
			ID: shareID(secret, p.ID), Open: row.Open, Done: row.Done,
			ClosedToday: row.ClosedSince,
		}
		if named {
			item.Name = p.Name
		}
		out.Open += item.Open
		out.Done += item.Done
		out.ClosedToday += item.ClosedToday
		out.Projects = append(out.Projects, item)
	}
	return out
}

// ─── token spend, redacted ────────────────────────────────────────────────

// shareSpendCacheFor is how long one reading of the spend rollups is reused.
//
// A wall polls every two seconds and never stops. The rollups behind a spend
// board are six GROUP BYs over a table that holds a year of history, and
// running them forty thousand times a day to answer a question whose answer
// changes when an agent finishes a request is work for nothing. Fifteen seconds
// is invisible on a chart of days and is the difference between six queries a
// poll and six queries a quarter of a minute.
//
// Cached before the per-link renaming, never after: the snapshot holds the
// panel's own figures with real project ids still on them, and each link
// renames them under its own secret afterwards. A cache on the far side of that
// would be a cache keyed by a credential.
const shareSpendCacheFor = 15 * time.Second

// shareSpendWindowDays is the range the grouped figures cover.
//
// One window for every breakdown and for the "window" total, rather than a
// range control per widget. A dashboard has nobody standing at it to adjust
// anything, and three widgets side by side each covering a different span, with
// no controls to explain why, is a screen that adds up to nothing.
const shareSpendWindowDays = 30

// shareSpendHistoryDays is how far back the day series reaches: 53 whole weeks,
// so the year grid's first column is complete and its leftmost month label is
// true. The same 371 the token panel uses, for the same reason.
const shareSpendHistoryDays = 371

// shareSpendCacheMax bounds the number of scopes held at once.
//
// One entry per scope a link is pointed at, and scopes are created by an
// authenticated owner rather than by whoever holds a link -- so this is not a
// bound against an attacker, it is a bound against a panel with two hundred
// projects quietly keeping two hundred rollups alive. Over the cap the map is
// dropped whole rather than evicted cleverly: the cost of being wrong is one
// recomputation, and an LRU here would be more machinery than the thing it
// manages.
const shareSpendCacheMax = 32

// spendProjectRow is one project's spend before it has been renamed for a link.
//
// The real id, kept only inside the cache and never marshalled. Nothing in this
// struct has a json tag, which is the mechanical half of that promise.
type spendProjectRow struct {
	id, name        string
	total, requests int64
}

// spendSnapshot is what the panel knows about spend within one scope, computed
// once and shared by every link looking at that scope.
type spendSnapshot struct {
	readable  bool
	scannedAt int64
	date      string
	// hoursToday is how far into the local day the server is, so a rate can be
	// "so far today" rather than a guess. Sent rather than derived in the
	// browser, which does not know the server's timezone and would compute the
	// rate of a different day for a reader in another one.
	hoursToday float64
	// days keeps the store's own rows rather than the wire's buckets, because
	// the totals derived from them -- today, yesterday, this month, the window
	// -- want the input/output/cache split that a bucket has thrown away.
	days     []store.UsageDay
	months   []store.UsageDay
	tools    []shareSpendGroup
	models   []shareSpendGroup
	projects []spendProjectRow
}

func emptySpend() spendSnapshot {
	return spendSnapshot{
		days: []store.UsageDay{}, months: []store.UsageDay{},
		tools: []shareSpendGroup{}, models: []shareSpendGroup{},
	}
}

type cachedSpend struct {
	at   time.Time
	snap spendSnapshot
}

// spendNow returns the shared snapshot for one scope, recomputing it when it
// has aged out.
//
// cwdPrefix is empty for a whole-panel link and is the scoped project's own
// directory otherwise. It arrives from the link's row and never from a request:
// a caller that could name a directory here could ask whether an agent had ever
// run in one, and learn the answer from whether the numbers moved.
//
// Failure is an empty snapshot rather than an error, the same way the CPU
// sampler's is: a chart that cannot be drawn leaves a gap, and everything else
// on the screen is still true.
func (s *Server) spendNow(ctx context.Context, projects []store.Project,
	cwdPrefix string) spendSnapshot {
	s.spendMu.Lock()
	defer s.spendMu.Unlock()
	if hit, ok := s.spendCache[cwdPrefix]; ok && time.Since(hit.at) < shareSpendCacheFor {
		return hit.snap
	}

	snap := emptySpend()
	in := s.tokens()
	if in == nil {
		// No ingester: nothing has ever been counted and nothing ever will be.
		// Reported as "not readable" rather than as zero, which is the
		// distinction the whole flag exists for.
		s.putSpend(cwdPrefix, snap)
		return snap
	}

	// A wall display is a watcher, and the ingester's whole design is that an
	// unwatched panel walks nothing. Without this a spend board shows whatever
	// the transcripts said when the panel last started, forever, while saying
	// it is live -- the failure the connection indicator exists to make
	// impossible, arriving through the numbers instead.
	//
	// Bounded by the ingester's own MinInterval and single-flight, so a link
	// polled every two seconds costs one pass every thirty seconds: the same as
	// a panel tab left open, which is what this is.
	in.Ensure(false)

	pass, _ := in.Status()
	if pass.At.IsZero() {
		// A pass has been asked for and none has finished. Everything below
		// would be a confident zero.
		s.putSpend(cwdPrefix, snap)
		return snap
	}
	snap.readable, snap.scannedAt = true, pass.At.Unix()

	now := time.Now()
	snap.date = now.Format("2006-01-02")
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	snap.hoursToday = now.Sub(midnight).Hours()
	from := now.AddDate(0, 0, -(shareSpendHistoryDays - 1)).Format("2006-01-02")

	history := store.UsageFilter{From: from, To: snap.date, CWDPrefix: cwdPrefix}
	days, err := s.DB.UsageByDay(ctx, history)
	if err != nil {
		s.Log.Debug("share spend by day", "err", err)
		empty := emptySpend()
		s.putSpend(cwdPrefix, empty)
		return empty
	}
	snap.days = days

	// Months are every month there has ever been, not the months the day series
	// happens to reach: "what has each month cost" is the question, and
	// answering it with the last fifty-three weeks of months answers a
	// different one.
	if months, merr := s.DB.UsageByMonth(ctx,
		store.UsageFilter{CWDPrefix: cwdPrefix}); merr == nil {
		snap.months = months
	} else {
		s.Log.Debug("share spend by month", "err", merr)
	}

	window := store.UsageFilter{
		From:      now.AddDate(0, 0, -(shareSpendWindowDays - 1)).Format("2006-01-02"),
		To:        snap.date,
		CWDPrefix: cwdPrefix,
	}
	if byTool, terr := s.DB.UsageByTool(ctx, window); terr == nil {
		// Every agent this panel can read appears, spend or none, so that one
		// contributing nothing is a bar at zero rather than a row that is
		// simply not there.
		for _, tool := range usage.Tools {
			row := byTool[string(tool)]
			snap.tools = append(snap.tools, shareSpendGroup{
				ID: string(tool), Name: string(tool),
				Total: row.Total(), Requests: row.Requests})
		}
	} else {
		s.Log.Debug("share spend by tool", "err", terr)
	}

	if models, merr := s.DB.UsageByModel(ctx, window); merr == nil {
		for _, m := range models {
			// A model name is the vendor's -- claude-opus-4, gpt-5-codex -- so
			// unlike a working directory it names nothing of the user's. It is
			// carried as both id and name because there is nothing to redact:
			// under `counts` the client shows the id, and it is the same word.
			snap.models = append(snap.models, shareSpendGroup{
				ID: m.Model, Name: m.Model, Total: m.Total(), Requests: m.Requests})
		}
	} else {
		s.Log.Debug("share spend by model", "err", merr)
	}

	if dirs, derr := s.DB.UsageByDirectory(ctx, window); derr == nil {
		// groupByProject, and then the fields are restated one at a time. Its
		// own row type carries the project's path, which is the first thing on
		// the list of what a share link never sends.
		for _, p := range groupByProject(dirs, projects) {
			snap.projects = append(snap.projects, spendProjectRow{
				id: p.ID, name: p.Name, total: p.Total(), requests: p.Requests})
		}
	} else {
		s.Log.Debug("share spend by directory", "err", derr)
	}

	s.putSpend(cwdPrefix, snap)
	return snap
}

// putSpend records a snapshot. Called with spendMu held.
func (s *Server) putSpend(key string, snap spendSnapshot) {
	if s.spendCache == nil || len(s.spendCache) >= shareSpendCacheMax {
		s.spendCache = map[string]cachedSpend{}
	}
	s.spendCache[key] = cachedSpend{at: time.Now(), snap: snap}
}

// shareSpendFor projects the shared snapshot onto one link and one board.
//
// Two things happen here and nowhere else: the real project ids become this
// link's pseudonyms, and the arrays the board did not ask for are left empty.
func (s *Server) shareSpendFor(ctx context.Context, projects []store.Project, secret []byte,
	named bool, needs map[string]bool, board store.Board, scope scopeOf) shareSpend {
	out := shareSpend{
		WindowDays: shareSpendWindowDays,
		Days:       []shareSpendBucket{}, Months: []shareSpendBucket{},
		Heatmap: []shareSpendBucket{}, Tools: []shareSpendGroup{},
		Models: []shareSpendGroup{}, Projects: []shareSpendGroup{},
	}
	if scope.kind != store.ShareWhole && scope.cwd == "" {
		// A scoped link whose project no longer exists, or was never resolvable.
		// Falling through to the unscoped rollups here is the whole failure this
		// branch exists to stop: an empty prefix means "no filter", so a link
		// scoped to a deleted project would start reporting the spend of every
		// project on the machine. Nothing, and it says so as "not counted".
		return out
	}

	snap := s.spendNow(ctx, projects, scope.cwd)
	out.Readable, out.ScannedAt, out.Date = snap.readable, snap.scannedAt, snap.date
	out.HoursToday = snap.hoursToday

	month, lastMonth := "", ""
	yesterday := ""
	if d, err := time.Parse("2006-01-02", snap.date); err == nil {
		month = snap.date[:7]
		lastMonth = d.AddDate(0, -1, 0).Format("2006-01")
		yesterday = d.AddDate(0, 0, -1).Format("2006-01-02")
	}
	windowFrom := spendDaysFrom(snap.date, shareSpendWindowDays)

	daysCut := ""
	if needs[store.NeedSpendDays] {
		// Cut to the widest range any bar widget on this board asked for. A
		// board showing a fortnight does not carry a year of days to draw it.
		daysCut = spendDaysFrom(snap.date, boardSpendDays(board))
	}
	for _, d := range snap.days {
		switch {
		case d.Day == snap.date:
			addDay(&out.Today, d)
		case yesterday != "" && d.Day == yesterday:
			addDay(&out.Yesterday, d)
		}
		if month != "" && strings.HasPrefix(d.Day, month) {
			addDay(&out.Month, d)
		}
		if windowFrom != "" && d.Day >= windowFrom {
			addDay(&out.Window, d)
		}
		bucket := bucketOf(d.Day, d)
		if daysCut != "" && d.Day >= daysCut {
			out.Days = append(out.Days, bucket)
		}
		if needs[store.NeedSpendHeatmap] {
			out.Heatmap = append(out.Heatmap, bucket)
		}
	}
	for _, m := range snap.months {
		if m.Day == lastMonth {
			addDay(&out.LastMonth, m)
		}
		// Every month there has ever been is already in hand, so the running
		// total of everything is an addition rather than a query. The odometer
		// is the only figure on a board that only goes up, and it would be a
		// sixth GROUP BY if it were asked for separately.
		addDay(&out.AllTime, m)
		if needs[store.NeedSpendMonths] {
			out.Months = append(out.Months, bucketOf(m.Day, m))
		}
	}

	if needs[store.NeedSpendTools] {
		out.Tools = snap.tools
	}
	if needs[store.NeedSpendModels] {
		out.Models = snap.models
	}
	if needs[store.NeedSpendProjects] {
		for _, p := range snap.projects {
			row := shareSpendGroup{Total: p.total, Requests: p.requests}
			// The catch-all row for work done outside every project keeps an
			// empty id: it is a residue rather than a project, and giving it a
			// pseudonym would make it look like one the session groups had
			// simply not mentioned.
			if p.id != "" {
				row.ID = shareID(secret, p.id)
				if named {
					row.Name = p.name
				}
			}
			out.Projects = append(out.Projects, row)
		}
	}
	return out
}

// bucketOf is one labelled column, restated field by field.
//
// A function rather than a literal at each of the three call sites, so that
// what a bucket discloses is decided once. Restated rather than embedding
// store.UsageDay, for the reason everything in this file is restated: that row
// carries the day's own columns and a column added to it must not become a
// column on a chart on a wall without somebody writing the line.
func bucketOf(label string, d store.UsageDay) shareSpendBucket {
	return shareSpendBucket{
		Label: label, Total: d.Total(), Requests: d.Requests,
		Input: d.Input, Output: d.Output, CacheRead: d.CacheRead, CacheWrite: d.CacheWrite,
	}
}

// addDay folds one day's row into a running total, column by column.
//
// Restated rather than embedding store.UsageTotals, for the reason the rest of
// this file restates everything: a column added to the store's rollup is not
// disclosed by a share link until somebody writes the line that discloses it.
func addDay(into *shareSpendTotals, d store.UsageDay) {
	into.Input += d.Input
	into.Output += d.Output
	into.CacheRead += d.CacheRead
	into.CacheWrite += d.CacheWrite
	into.Requests += d.Requests
	into.Total += d.Total()
}

// daySeriesKinds are the widget kinds that draw the day series.
//
// A list rather than a single name, because "which widgets draw days" and
// "which widgets need the days section" are the same question asked in two
// places -- board.Needs answers the second from the registry, and this answers
// the first. A kind missing from here still gets its section; it just gets it
// cut to the default window, which is a chart that quietly shows a fortnight
// when its author asked for a year.
var daySeriesKinds = map[string]bool{"spendbars": true, "sparkline": true, "spendstack": true}

// boardSpendDays is the widest day range any series widget on a board asked for.
func boardSpendDays(board store.Board) int {
	widest := 0
	for _, w := range board.Widgets {
		if !daySeriesKinds[w.Kind] || (w.By != "" && w.By != "day") {
			continue
		}
		days := w.Days
		if days <= 0 {
			days = shareSpendWindowDays
		}
		if days > widest {
			widest = days
		}
	}
	return widest
}

// spendDaysFrom is the oldest day a range of n days reaches back to.
func spendDaysFrom(today string, n int) string {
	if today == "" || n <= 0 {
		return ""
	}
	d, err := time.Parse("2006-01-02", today)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, -(n - 1)).Format("2006-01-02")
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

// registerShareAdminRoutes is where a link is made, edited and revoked.
//
// Behind the ordinary session, and that is the whole answer to "I should not
// have to walk to the wall and log in to change it". The board a television is
// showing is edited from here, by somebody signed in on a laptop, and the
// television picks it up on its next poll. Nothing was added under the share
// token to make that work.
func (s *Server) registerShareAdminRoutes(r chi.Router) {
	r.Get("/settings/shares", s.handleListShares)
	r.Post("/settings/shares", s.handleCreateShare)
	r.Get("/settings/shares/catalogue", s.handleShareCatalogue)
	r.Get("/settings/shares/{shareID}/preview", s.handleSharePreview)
	r.Patch("/settings/shares/{shareID}", s.handleUpdateShare)
	r.Delete("/settings/shares/{shareID}", s.handleDeleteShare)
}

// handleSharePreview answers with what that screen is showing right now.
//
// The editor's whole problem is that the owner is composing for a screen they
// cannot see, and the two ways to solve it badly are both worse than this one:
// invented sample data, which composes a layout against numbers that will not
// be the real ones; or a second reduction of the panel's state written in the
// frontend, which diverges from the real redaction on the first field either
// side gains.
//
// So it is the same builder the dashboard uses, called with the link's own row.
// It is a settings route: it needs the ordinary session, a share token answers
// 401 to it like everything else, and it discloses strictly less than
// /api/state -- which the caller already has -- so there is nothing here a
// signed-in owner could not already read.
func (s *Server) handleSharePreview(w http.ResponseWriter, r *http.Request) {
	link, err := s.DB.ShareLinkByID(r.Context(), chi.URLParam(r, "shareID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// The link's id as the secret, not its token hash. The hash is a live
	// credential's fingerprint and this response is not the place to derive
	// anything from it; the preview's pseudonyms only have to be stable within
	// the preview, and joining them to the real screen's is not something
	// anybody wants to be able to do.
	out, err := s.buildShareDashboard(r.Context(), link, []byte(link.ID))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// shareCatalogue is the vocabulary a board is built from.
//
// Served rather than mirrored in the frontend, so that every option the editor
// offers is an option the validator accepts. Two copies of this table is how a
// settings page comes to offer a widget the server refuses, and the person who
// finds out is the one who pressed the button.
type shareCatalogue struct {
	Presets []store.Preset     `json:"presets"`
	Widgets []store.WidgetSpec `json:"widgets"`
	// Screens are the sizes a preset can be composed for, in the order the
	// editor offers them. Served rather than mirrored, like everything else
	// here: a screen the picker offers and the catalogue does not name is a
	// group with nothing in it.
	Screens []string `json:"screens"`
	// Steps are the widths the editor offers, in twelfths. The validator takes
	// any span from 1 to MaxSpan; these are the ones worth putting in a select.
	Steps []int `json:"steps"`
	// The bounds, so the editor can stop somebody rather than watch the server
	// stop them.
	MaxWidgets int `json:"maxWidgets"`
	MaxSpan    int `json:"maxSpan"`
	MaxRows    int `json:"maxRows"`
	MaxCaption int `json:"maxCaption"`
	MaxRemark  int `json:"maxRemark"`
	MaxDays    int `json:"maxDays"`
	// MaxDensity is how many density steps there are, so the editor's control
	// is built from the server's answer rather than from a number in a file
	// beside it.
	MaxDensity int `json:"maxDensity"`
}

func (s *Server) handleShareCatalogue(w http.ResponseWriter, r *http.Request) {
	out := shareCatalogue{
		Presets: store.Presets(), Screens: store.Screens(), Steps: store.GridSteps(),
		MaxWidgets: store.MaxWidgets, MaxSpan: store.MaxSpan, MaxRows: store.MaxRows,
		MaxCaption: store.MaxCaption, MaxRemark: store.MaxRemark, MaxDays: store.MaxSpendDays,
		MaxDensity: store.MaxDensity,
	}
	for _, kind := range store.KnownWidgetKinds() {
		if spec, ok := store.WidgetOptions(kind); ok {
			out.Widgets = append(out.Widgets, spec)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListShares lists the links, with each scope resolved to a name.
//
// Resolved here rather than joined in SQL, and resolved every time rather than
// stored: the project a link is scoped to can be renamed or deleted, and a name
// copied into share_links at creation would go on naming a project that no
// longer exists. A scope whose row is gone comes back with an empty name, which
// is what the settings page renders as "the thing this was about is gone".
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	links, err := s.DB.ListShareLinks(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	projects, perr := s.DB.ListProjects(ctx)
	if perr != nil {
		s.writeStoreErr(w, perr)
		return
	}
	sessions, serr := s.DB.ListSessions(ctx)
	if serr != nil {
		s.writeStoreErr(w, serr)
		return
	}
	now := time.Now()
	for i := range links {
		links[i].ScopeName = resolveScope(links[i], projects, sessions).name
		// How many screens have this open, and the largest of them. Both are
		// counted from the polls those screens were already making; see
		// internal/httpapi/sharelive.go for why neither is a column.
		//
		// This is the signal that tells an owner about to rearrange a wall
		// whether anything is actually showing it -- and, when something is,
		// what shape of screen they are composing for.
		links[i].Viewers, links[i].ViewportWidth, links[i].ViewportHeight =
			s.viewers.count(links[i].ID, now)
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
	// Preset names a starting arrangement; Board is an explicit one and wins.
	//
	// Both are offered because both are how this gets used: the settings page
	// sends a board it has just let somebody edit, and a person wiring up a
	// television with `curl` sends "preset": "attention" and is finished.
	// Neither is required, and a request with neither gets the default board.
	Preset string       `json:"preset"`
	Board  *store.Board `json:"board"`
	// Scope is "", "project" or "session"; ScopeID is the panel's own id of the
	// one it is about.
	//
	// A real id, and the only real id this API takes for a share link. It is
	// checked here against the rows that exist: a scope naming a project the
	// caller invented would otherwise be stored, resolve to nothing on every
	// poll, and look exactly like a project that had been deleted.
	Scope   string `json:"scope"`
	ScopeID string `json:"scopeId"`
	// Remark is the owner's label for the screen. Free text, cut to
	// store.MaxRemark runes rather than refused: it is a sentence somebody
	// typed, and a 400 on the eighty-first character is a form that argues.
	Remark string `json:"remark"`
	// Locked fixes the board against later edits until it is unlocked.
	Locked bool `json:"locked"`
}

// scopeFor validates a requested scope against the rows that exist.
//
// Refused rather than stored optimistically. A scope naming a project nobody
// has heard of resolves to nothing on every poll from then on, which renders as
// an empty dashboard -- indistinguishable from a project that was deleted, and
// impossible to tell from a typo without reading the database.
func (s *Server) scopeFor(ctx context.Context, kind, id string) (store.ShareScope, string, error) {
	scope := store.ShareScope(strings.TrimSpace(kind))
	id = strings.TrimSpace(id)
	if !store.ValidShareScope(scope) {
		return "", "", fmt.Errorf("scope must be project, session, or left out")
	}
	if scope == store.ShareWhole {
		if id != "" {
			return "", "", fmt.Errorf("a scopeId needs a scope")
		}
		return scope, "", nil
	}
	if id == "" {
		return "", "", fmt.Errorf("a %s scope needs a scopeId", scope)
	}
	switch scope {
	case store.ShareProject:
		if _, err := s.DB.GetProject(ctx, id); err != nil {
			return "", "", fmt.Errorf("no such project")
		}
	case store.ShareSession:
		if _, err := s.DB.GetSession(ctx, id); err != nil {
			return "", "", fmt.Errorf("no such session")
		}
	}
	return scope, id, nil
}

// boardFrom resolves the two ways a request can name a board.
//
// A pointer for Board so that "no board field" and "an empty board" are
// different requests: the first means "use the preset, or the default", and
// the second is a mistake worth an error rather than a silent substitution.
func boardFrom(preset string, board *store.Board) (store.Board, error) {
	// Checked here rather than left to ValidateBoard, which only ever sees the
	// preset written inside the board. An unknown name arriving in the
	// top-level field would otherwise be copied onto a valid board below and
	// stored, which is the one string on a board nothing else validates.
	if preset != "" && !store.KnownPreset(preset) {
		return store.Board{}, fmt.Errorf("unknown preset %q", preset)
	}
	if board != nil {
		out, err := store.ValidateBoard(*board)
		if err != nil {
			return store.Board{}, err
		}
		// A board the editor built from a preset carries the provenance in the
		// board itself; one sent by hand can name it alongside instead.
		if out.Preset == "" {
			out.Preset = preset
		}
		return out, nil
	}
	if preset == "" {
		return store.DefaultBoard(), nil
	}
	expanded, _ := store.PresetBoard(preset)
	return expanded, nil
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
	board, err := boardFrom(strings.TrimSpace(req.Preset), req.Board)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	scope, scopeID, err := s.scopeFor(r.Context(), req.Scope, req.ScopeID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
		detail, board, scope, scopeID, u.ID, req.Remark, req.Locked, expiresAt)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// The board's shape goes in the audit line, not its contents. "which
	// widgets" is a layout decision; "counts or names" is the disclosure
	// decision, and that is the one an operator reading this row is looking for.
	s.audit(r.Context(), "share.created", u.Username, s.clientIP(r),
		name+" ("+string(detail)+", "+boardLabel(board)+scopeLabel(scope, scopeID)+")")
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"id":        rec.ID,
		"name":      rec.Name,
		"prefix":    rec.Prefix,
		"detail":    rec.Detail,
		"board":     rec.Board,
		"scope":     rec.Scope,
		"remark":    rec.Remark,
		"locked":    rec.Locked,
		"expiresAt": rec.ExpiresAt,
		"createdAt": rec.CreatedAt,
	})
}

// scopeLabel names a link's scope for the audit trail.
//
// The real id goes in, deliberately. This row is read by the panel's owner
// looking at their own audit trail, so "which project did I open up" is the
// question, and a pseudonym would make it unanswerable to the one person
// entitled to the answer.
func scopeLabel(scope store.ShareScope, id string) string {
	if scope == store.ShareWhole {
		return ""
	}
	return ", " + string(scope) + " " + id
}

// boardLabel names a board for the audit trail: its preset, or its size.
func boardLabel(b store.Board) string {
	if b.Preset != "" {
		return b.Preset
	}
	return strconv.Itoa(len(b.Widgets)) + " widgets"
}

type updateShareRequest struct {
	Name   string       `json:"name"`
	Remark string       `json:"remark"`
	Board  *store.Board `json:"board"`
	// Locked is a pointer so that "not mentioned" and "set to false" are
	// different requests. They have to be: on a locked link the second is the
	// only edit that is allowed, and the first is the one that gets refused.
	Locked *bool `json:"locked"`
}

// handleUpdateShare renames a link and rearranges its board.
//
// Not its detail mode, and not its expiry. By the time anybody edits a link its
// URL is already in an email or typed into a television, so widening what that
// address discloses is a change the people holding it would never see. A board
// can only rearrange what the mode already allows; the mode itself means a new
// link, which is a thing somebody has to hand out on purpose.
func (s *Server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	linkID := chi.URLParam(r, "shareID")
	var req updateShareRequest
	if !decode(w, r, &req) {
		return
	}

	// What the row says now, before anything is written. The lock is decided
	// against the stored value and never against one in the request: a client
	// that believed a link was unlocked is exactly the client the lock exists
	// to stop.
	current, err := s.DB.ShareLinkByID(r.Context(), linkID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if current.Locked {
		// A locked board accepts one change and it is unlocking. Not "unlock
		// and also apply this board": the failure being prevented is a screen
		// somebody is sitting in front of being rearranged by an editor left
		// open on the wrong row, and a single request that could do both would
		// leave the lock as a message rather than a guard.
		//
		// So the rest of the request is dropped, deliberately, rather than
		// merged. The editor unlocks, refetches, and edits -- two acts, which
		// is the whole of what a lock is.
		if req.Locked == nil || *req.Locked {
			writeErr(w, http.StatusConflict, "this board is locked")
			return
		}
		if uerr := s.DB.UpdateShareLink(r.Context(), linkID, current.Name, current.Remark,
			current.Board, false); uerr != nil {
			s.writeStoreErr(w, uerr)
			return
		}
		if u, ok := currentUserFrom(r); ok {
			s.audit(r.Context(), "share.unlocked", u.Username, s.clientIP(r), current.Name)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Board == nil {
		writeErr(w, http.StatusBadRequest, "a board is required")
		return
	}
	board, err := store.ValidateBoard(*req.Board)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := session.TruncateTitle(strings.TrimSpace(req.Name))
	if name == "" {
		name = "dashboard"
	}
	locked := req.Locked != nil && *req.Locked
	if err := s.DB.UpdateShareLink(r.Context(), linkID, name, req.Remark, board,
		locked); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		// A lock is its own line rather than a word inside share.updated. An
		// operator reading the trail for "who fixed the screen the customer is
		// looking at" is asking a different question from "who moved a widget",
		// and one event answering both is one somebody has to grep inside.
		if locked {
			s.audit(r.Context(), "share.locked", u.Username, s.clientIP(r), name)
		}
		s.audit(r.Context(), "share.updated", u.Username, s.clientIP(r),
			name+" ("+boardLabel(board)+")")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	// linkID, not shareID: shareID is the function above that renames a row for
	// a link, and shadowing it here would compile and mean something else.
	linkID := chi.URLParam(r, "shareID")
	if err := s.DB.DeleteShareLink(r.Context(), linkID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// The viewers of a link that no longer exists. They will stop polling
	// within a couple of seconds anyway and their entries would age out, but a
	// revoked link reading "3 screens" for the fifteen seconds after it was
	// revoked is the one moment somebody is looking at that number hardest.
	s.viewers.forget(linkID)
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "share.revoked", u.Username, s.clientIP(r), linkID)
	}
	w.WriteHeader(http.StatusNoContent)
}
