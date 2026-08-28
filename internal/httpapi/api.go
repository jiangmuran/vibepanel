// Package httpapi exposes the REST surface and wires the WebSocket handler.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/selfupdate"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/usage"
	"github.com/jiangmuran/vibepanel/internal/version"
	"github.com/jiangmuran/vibepanel/internal/vnc"
	"github.com/jiangmuran/vibepanel/internal/webui"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// Server holds everything the HTTP layer needs.
type Server struct {
	Cfg      config.Config
	DB       *store.DB
	Tmux     *tmux.Client
	Manager  *session.Manager
	Hub      *ws.Hub
	Detector *session.Detector
	Sampler  *sysmon.Sampler

	// Updater fetches releases. A value rather than a pointer so the zero
	// Server has a working one; see TreeSampler below for the same reasoning.
	Updater selfupdate.Client

	// VNC decides which addresses the panel may open a TCP connection to on a
	// browser's behalf.
	//
	// A value, and the zero value is loopback-only. Same reasoning as
	// TreeSampler, with a sharper edge: a pointer would make "nobody set it"
	// a nil dereference or, worse, a nil check that falls through to no
	// policy at all. The zero value here is the strictest setting, so a Server
	// assembled by hand -- in a test, or by a caller that has not been updated
	// -- fails closed rather than open.
	VNC vnc.Policy

	// VNCEnabled gates whether the VNC routes are registered at all. False is
	// the default, and false means they are absent rather than guarded.
	VNCEnabled bool
	// GitHub queries pull requests, when somebody presses the button that asks
	// it to. A value with a zero Endpoint, which means api.github.com; tests
	// point it at an httptest server, and nothing else sets it.
	//
	// The token is deliberately not on it: it is read from the environment at
	// the moment of the request, so a panel restarted without one stops being
	// able to ask rather than carrying a copy from startup.
	GitHub git.Client

	// Git holds recent reads of the working trees the git tab polls, so that
	// several viewers on one project are one `git status` rather than several.
	// The zero value works and is the one every caller uses; see
	// internal/git/cache.go for why the endpoint may not read the disk direct.
	Git git.Cache

	// fullscreen holds the session ids whose pane has a full-screen program
	// drawing in it, as the poller last saw them.
	//
	// A pointer swap rather than a lock: it is written by one goroutine every
	// two seconds and read by every state build, and the readers only ever want
	// the most recent whole answer.
	fullscreen atomic.Pointer[[]string]
	// TreeSampler holds the previous per-process CPU counters, so it has to be
	// the same one across requests: a fresh sampler per request has nothing to
	// difference against and reports every session at zero forever.
	//
	// A value rather than a pointer, so the zero Server has a working one. Every
	// test that builds a Server by hand would otherwise have to remember this
	// field, and forgetting it is a nil dereference in a handler rather than a
	// compile error.
	TreeSampler sysmon.TreeSampler
	Auth        *Auth
	// Challenges holds in-flight WebAuthn ceremonies. The challenge stays on
	// the server; the browser only carries an opaque id for it.
	Challenges *challengeStore
	Log        *slog.Logger

	// Tokens reads the agents' own transcripts for what they spent.
	//
	// A pointer that must be set explicitly, and nil is a working state: the
	// token-usage endpoints answer 503 and everything else is unaffected. It
	// is deliberately not built on demand from the running user's home
	// directory, because every test that constructs a Server by hand would
	// then start a background walk of whoever's machine the tests are on.
	Tokens *usage.Ingester

	// hookToken authenticates state reports from agent hooks. Cached after the
	// first read so the hot path does not hit the database.
	tokenOnce  sync.Once
	hookToken  string
	tokenError error

	// lastSnapshot is the most recent state payload that was broadcast. The
	// poller compares against it so that a tick where nothing changed sends
	// nothing — otherwise pushing is just polling with extra steps.
	snapMu       sync.Mutex
	lastSnapshot []byte

	// hookInstalled caches whether the reporter is wired into the agent's
	// configuration.
	//
	// The state snapshot needs the answer, and snapshots are built on every
	// broadcast — a session changing state, a note being saved. Asking properly
	// means reading and parsing a file in the user's home directory, so asking
	// every time meant doing that several times a second with a couple of dozen
	// busy sessions. The TTL covers someone editing that file by hand;
	// installing or removing through the panel invalidates it immediately.
	hookMu        sync.Mutex
	hookInstalled bool
	hookCheckedAt time.Time

	// outputSeen debounces last_output_at writes. The pump can call into here
	// hundreds of times a second; the column is read by humans.
	//
	// Nothing ever deletes from it. A session that produced output once leaves
	// its entry for the life of the process, so the map grows with every
	// session ever created rather than with the sessions that exist — around a
	// hundred bytes each, bounded only by how often somebody makes one.
	//
	// Estimated rather than measured, and left on that basis: at a hundred
	// sessions a day it is on the order of three hundred kilobytes a month.
	// The detector has Retain for exactly this, driven by the poller, and
	// wiring the same thing here is a few lines — worth doing next to the next
	// change in this file, where the tests can say whether it is right.
	outMu      sync.Mutex
	outputSeen map[string]time.Time

	// archivedOutput remembers the last_output_at each session had when its
	// scrollback was last captured, so a session that has printed nothing since
	// is not captured and rewritten every archive tick. Pruned in archiveOnce
	// against the sessions that still exist, which is what stops it growing
	// with every session ever created.
	archMu         sync.Mutex
	archivedOutput map[string]int64
	// shareTouch rate-limits the "last seen" write on a share link.
	//
	// A wall display polls the dashboard every couple of seconds and never
	// stops, so stamping every lookup is tens of thousands of writes a day
	// through SQLite's one write lock, for a field the settings page renders as
	// a date. Lazily built so a Server assembled by hand still has one.
	shareTouchOnce sync.Once
	shareTouch     *auth.Cooldown

	// spendSnap is the token-spend rollup a share board draws from, shared by
	// every link and recomputed when it ages out.
	//
	// A wall polls every two seconds forever, and the rollups behind one spend
	// board are five GROUP BYs over a table holding a year of history. Without
	// this they run forty thousand times a day to answer a question whose
	// answer moves when an agent finishes a request. Shared rather than kept
	// per link on purpose: the snapshot holds the panel's real project ids and
	// each link renames them under its own secret afterwards, so a cache on the
	// far side of that would be a cache keyed by a credential.
	// Keyed by the scope's directory, "" for a whole-panel link, so a link
	// scoped to one project never draws from the panel's own rollup.
	spendMu    sync.Mutex
	spendCache map[string]cachedSpend

	// trendRings is the last few minutes of the machine and the token total,
	// per scope, for the widgets that draw a line rather than a number.
	//
	// In memory and never a table: a restart is meant to lose it, because the
	// honest line after a restart is a short one starting now rather than a
	// long one with a hole in it. See internal/httpapi/sharelive.go.
	trendMu    sync.Mutex
	trendRings map[string]*trendRing

	// viewers is who has each share link open right now, counted from the polls
	// they were already making rather than from anything they send.
	//
	// A number, not a column. A wall polls every two seconds forever, and a
	// count in a table would be that many writes for a fact that is true for
	// two seconds and must be false again after a restart.
	viewers shareViewerBook

	// TrimEvery and AuditKeep override the audit trim's schedule and cap. Zero
	// means the constants. Tests set them small; nothing else should. They
	// exist because a periodic job nobody can drive from a test is how this
	// one came to run only at startup for as long as it did.
	TrimEvery time.Duration
	AuditKeep int

	// ArchiveEvery overrides how often scrollback is captured. Zero means
	// archiveInterval. Same reasoning as TrimEvery: a periodic job nobody can
	// drive from a test is one that gets shipped never having run.
	ArchiveEvery time.Duration

	// EventSweepEvery and EventKeepDays override the session-event log's
	// housekeeping. Zero means the constants in events.go. Same reasoning as
	// TrimEvery, with the same warning: tests set them, nothing else should.
	EventSweepEvery time.Duration
	EventKeepDays   int

	// events carries state transitions to the one goroutine that writes them.
	//
	// Bounded and written to with a non-blocking send, which is the whole of
	// why the log cannot stall a state update. Made on first use so a Server
	// built by hand has a working one; a nil channel blocks forever on send,
	// which is the one failure this queue may not have. See events.go.
	eventsOnce    sync.Once
	events        chan store.SessionEvent
	eventsDropped atomic.Int64

	// staleMu guards the record of a panel that has stopped keeping up.
	//
	// The poller writes on every tick — session runtime, derived titles, exit
	// status — so if the database cannot be written or tmux cannot be reached,
	// it is the first thing to notice and it notices within two seconds.
	//
	// Measured with the database's writes capped: the eleventh rename returned
	// `500 store: exec: disk I/O error`, so the person who pressed a button was
	// told. Everyone else saw `/api/health` answering `"ok": true` on a panel
	// that had stopped recording anything at all, and a state snapshot with no
	// hint in it. The terminals kept working, which is the architecture doing
	// its job — and is exactly why nothing else looked wrong.
	staleMu     sync.Mutex
	staleSince  time.Time
	staleLast   time.Time
	staleReason string
	// CertExpiry, if set, reports when the certificate now being served stops
	// being valid.
	//
	// Surfaced on the settings page because the failure it guards is silent by
	// nature: a certificate nobody renewed does not announce itself, it simply
	// stops working one morning. The panel logs a warning as it approaches, but
	// a log line on a machine nobody reads is not where this should first be
	// noticed.
	CertExpiry func() time.Time
}

// Routes builds the router.
//
// Order matters: /api and /ws are registered before the catch-all that serves
// the single-page app, so an unknown API path returns a JSON 404 instead of
// quietly handing the caller an HTML document.
// securityHeaders sets the four that cost nothing and matter for a terminal
// that is deliberately on the public internet.
//
// Referrer-Policy is the one with teeth. The terminal makes every URL an agent
// prints into a clickable link, and clicking one used to send
// `Referer: https://<the panel>/` to whatever host that URL named — an address
// chosen by whatever the agent read, echoed, or was told to print. Measured
// against a listener standing in for "somewhere else on the internet": it
// received `referer: http://127.0.0.1:38475/`, the panel's exact origin. For a
// panel whose exposure story is a non-standard port and a password, handing
// the address to arbitrary third parties on a link click is a real weakening.
//
// Not the opener: the web-links addon opens a blank window, nulls `opener` and
// then navigates, so reverse tabnabbing was already handled. Worth stating
// because it looks like the same bug and is not.
//
// frame-ancestors rather than a full CSP. A real content policy would have to
// account for the inline styles xterm and Tailwind generate, and a CSP that
// breaks the terminal would be turned off within a day. This one directive
// restricts nothing the panel does.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// Strict-Transport-Security is deliberately not here, and this is the
		// note that stops it being added as the obvious fifth.
		//
		// HSTS is scoped to a host and ignores the port it arrived on (RFC 6797
		// §8.1). This panel's whole deployment story is a non-standard port on
		// a machine that runs other things, so a policy sent from :8443 would
		// pin *every* port of that hostname to HTTPS in every visitor's
		// browser — including whatever the operator serves on plain HTTP at
		// :80 — for the whole max-age. Undoing it means serving max-age=0 over
		// HTTPS on that same host, which is a repair job, not a config change.
		//
		// It also buys little here. A downgrade needs something to downgrade
		// *to*, and there is no HTTP listener: with --tls the port speaks TLS
		// only, and with --tls off the header would be sent in the clear and
		// browsers ignore it there.
		//
		// The four above cost nothing, which is the line this set was chosen
		// on. This one does not.
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Routes() http.Handler {
	if s.Challenges == nil {
		s.Challenges = newChallengeStore()
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	// Deliberately NOT middleware.RealIP.
	//
	// It overwrites r.RemoteAddr from X-Forwarded-For or X-Real-IP with no
	// trust model at all, and everything downstream that cares about who is
	// calling — the CIDR allowlist, the login throttle, the audit log — then
	// reads a value the caller chose. Measured before removing it: with
	// --allow-from set to a network this machine is not on, a plain request
	// was refused and the same request with "X-Forwarded-For: <an allowed
	// address>" went through; and twelve wrong passwords with a different
	// header value each time were never throttled, while twelve from one
	// address were throttled after the first.
	//
	// auth.ClientIP does this properly: it believes the header only from a
	// proxy the operator listed in --trusted-proxies. Use that, always.

	r.Route("/api", func(r chi.Router) {
		// Open: the health probe says nothing sensitive, the auth endpoints are
		// how you get in, and hooks carry their own token because they run
		// outside the browser as children of an agent.
		r.Get("/health", s.handleHealth)
		s.registerAuthRoutes(r)
		s.registerPasskeyRoutes(r)
		r.Post("/hook/state", s.handleHookState)

		// A read-only share link carries its own capability in the URL and is
		// resolved against its own table, so it is registered outside the
		// session group rather than inside it with an exemption.
		//
		// That placement is the security boundary, and it is worth being blunt
		// about which direction the danger runs. Everything below RequireAuth
		// is protected by being below it; this is protected by reaching exactly
		// one GET handler and by share_links being a table currentUser does not
		// consult. Move a route from below into here and it loses its
		// authentication; add a second route here and a share token can reach
		// it. There is no flag in between, on purpose.
		s.registerShareRoutes(r)

		// Everything else needs a session. This panel hands out a writable
		// terminal; there is no such thing as a harmless unauthenticated
		// endpoint here.
		r.Group(func(r chi.Router) {
			r.Use(s.RequireAuth)

			r.Get("/state", s.handleState)

			r.Post("/projects", s.handleCreateProject)
			r.Post("/projects/reorder", s.handleReorderProjects)
			r.Patch("/projects/{id}", s.handlePatchProject)
			r.Delete("/projects/{id}", s.handleDeleteProject)

			r.Post("/sessions", s.handleCreateSession)
			r.Patch("/sessions/{id}", s.handlePatchSession)
			r.Delete("/sessions/{id}", s.handleDeleteSession)
			r.Post("/sessions/{id}/restart", s.handleRestartSession)
			// Plural and id-less on purpose. Restoring after a reboot is a
			// batch by nature — the whole machine went down, so the whole list
			// is gone — and a per-id route would have the browser fire two
			// dozen requests that each shell out to tmux.
			r.Post("/sessions/restore", s.handleRestoreSessions)

			s.registerLaunchProfileRoutes(r)
			s.registerPanelRoutes(r)
			// Inside the group, and it has to stay there. This is the one
			// place the panel opens an outbound TCP connection on a browser's
			// say-so; a route that reached it without a session would be a
			// port scanner anybody could point at this machine's networks.
			//
			// And off unless asked for. Not a hidden tab -- the routes do not
			// exist, so a panel nobody turned it on for answers 404 to every
			// one of them. Hiding a control is a decision the frontend makes;
			// this is one the router makes.
			if s.VNCEnabled {
				s.registerVncRoutes(r)
			}
			s.registerGitRoutes(r)
			s.registerUpdateRoutes(r)
			s.registerWebhookRoutes(r)
			s.registerTokenRoutes(r)
			s.registerSettingsRoutes(r)
			// Making and revoking share links is an ordinary settings action
			// and needs the ordinary session. A share token cannot mint another
			// one, which is the property that keeps one leaked link from
			// becoming a supply of them.
			s.registerShareAdminRoutes(r)
		})

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
	})

	// The WebSocket is the terminal itself; it needs the same session as
	// everything else.
	//
	// OriginPatterns is deliberately absent, and that is load-bearing rather
	// than an omission. Left nil, coder/websocket accepts a handshake only when
	// the Origin host matches the Host — so a page on another site cannot open
	// this socket with the browser's cookies attached and be handed a writable
	// terminal. The cookie is SameSite=Strict as well, but the two defend
	// different things and neither makes the other redundant.
	//
	// The obvious way to break it is to add `OriginPatterns: []string{"*"}` to
	// silence a cross-origin complaint while serving the frontend from a dev
	// server on another port. Point the dev server's proxy at the panel
	// instead; a wildcard here hands the terminal to any page the browser
	// happens to be on.
	//
	// Not pinned by a test, which it should be: a handshake carrying a foreign
	// Origin must be refused. Written down here because the protection is a
	// library default reached by writing nothing, and nothing about writing
	// nothing announces itself when the library changes.
	r.With(s.RequireAuth).Handle("/ws", &ws.Handler{
		Manager:  s.Manager,
		Resolve:  resolver{db: s.DB},
		Hub:      s.Hub,
		Log:      s.Log,
		Snapshot: s.snapshot,
		// A socket is authorised once, at the handshake, and then lives for
		// hours. Without this, signing out, a session expiring, an
		// administrator revoking one, or changing the password left the
		// terminals those browsers already had open still streaming — and
		// still accepting keystrokes.
		StillAuthorized: s.stillAuthorized,
	})

	r.Handle("/*", webui.Handler(s.Cfg.StaticDir))
	return r
}

// ─── resolver ─────────────────────────────────────────────────────────────

// resolver lets the ws package look sessions up without importing the store.
type resolver struct{ db *store.DB }

func (r resolver) Resolve(ctx context.Context, sessionID string) (string, int, int, error) {
	s, err := r.db.GetSession(ctx, sessionID)
	if err != nil {
		return "", 0, 0, err
	}
	return s.TmuxName, s.Cols, s.Rows, nil
}

func (r resolver) RecordSize(ctx context.Context, sessionID string, cols, rows int) error {
	return r.db.SetSessionSize(ctx, sessionID, cols, rows)
}

// ─── handlers ─────────────────────────────────────────────────────────────

// snapshot builds the full state payload. Returns nil if it cannot be read,
// in which case nothing is broadcast rather than something wrong.
// RestoreState seeds the detector from the database, once, at startup.
//
// Must run before Reconcile, which re-derives every session from live facts
// and writes the answer back. Without this the answer for anything that is not
// a shell is "working", so a restart turned every session that was waiting for
// a human into one that looked busy — permanently, because nothing was ever
// going to ring a second time.
func (s *Server) RestoreState(ctx context.Context) error {
	if s.Detector == nil {
		return nil
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("restore state: %w", err)
	}
	restored := 0
	for _, row := range rows {
		if row.StateChangedAt == 0 {
			continue
		}
		before := row.State
		s.Detector.Restore(row.ID, row.State, row.StateSource, time.Unix(row.StateChangedAt, 0))
		if before == session.StateWaiting || row.StateSource != session.SourceHeuristic {
			restored++
		}
	}
	if restored > 0 {
		s.Log.Info("restored session states", "sessions", restored)
	}
	return nil
}

// staleGrace is how long the poller must keep failing before the panel says so.
//
// Three ticks. One failed poll is a blip — a tmux command that lost a race with
// a session being deleted — and a banner that appears and disappears is one
// people learn to ignore. Three in a row is a condition.
const staleGrace = 3 * pollInterval

// staleQuiet is how long writes must succeed before the panel stops saying so.
// Long enough that a disk which is full, gets a little room and fills again
// does not flicker.
const staleQuiet = 30 * time.Second

func (s *Server) noteStale(err error) {
	now := time.Now()
	s.staleMu.Lock()
	defer s.staleMu.Unlock()
	if s.staleSince.IsZero() {
		s.staleSince = now
	}
	s.staleLast = now
	s.staleReason = err.Error()
}

// clearStale forgets the failures once they have stopped.
//
// A successful poll is not enough on its own. A database capped at its current
// size still lets the poller rewrite pages it has already allocated while a
// request needing a new one fails — measured, and it is why the first version
// of this signal never fired: the poller kept succeeding and erased the
// evidence on every tick. Failures have to have stopped, not merely paused
// between two of them.
func (s *Server) clearStale() {
	s.staleMu.Lock()
	defer s.staleMu.Unlock()
	if s.staleSince.IsZero() || time.Since(s.staleLast) < staleQuiet {
		return
	}
	s.staleSince, s.staleLast, s.staleReason = time.Time{}, time.Time{}, ""
}

// stale reports why the panel's records are out of date, or "" if they are not.
func (s *Server) stale() string {
	s.staleMu.Lock()
	defer s.staleMu.Unlock()
	if s.staleSince.IsZero() || time.Since(s.staleSince) < staleGrace {
		return ""
	}
	return s.staleReason
}

func (s *Server) snapshot(ctx context.Context) []byte {
	state, err := s.buildState(ctx)
	if err != nil {
		s.Log.Warn("snapshot", "err", err)
		s.noteStale(err)
		return nil
	}
	payload, err := json.Marshal(struct {
		Type string `json:"t"`
		stateResponse
	}{Type: ws.MsgState, stateResponse: state})
	if err != nil {
		s.Log.Warn("snapshot marshal", "err", err)
		return nil
	}
	return payload
}

// notifyState pushes the current state to every viewer, coalesced.
// notifyPanel tells every viewer that a project's note or todo list changed.
//
// Deliberately not part of the state snapshot: notes and todo lists are per
// project and can be long, and pushing them to every viewer on every keystroke
// would make the socket carry a document nobody asked for. This says what
// changed and lets the panel that cares refetch — which is also what makes the
// panels work at all in a second window, where before they simply never
// updated and the second save overwrote the first.
func (s *Server) notifyPanel(projectID, kind string) {
	if s.Hub == nil {
		return
	}
	// "t" is the discriminator every other control message uses; "type" here
	// would be silently ignored by the client's switch.
	payload, err := json.Marshal(map[string]string{
		"t":         ws.MsgPanel,
		"projectId": projectID,
		"kind":      kind,
	})
	if err != nil {
		return
	}
	// BroadcastEvent, not Broadcast: this is not a snapshot. The snapshot slot
	// replaces what is waiting, which is right for something absolute and
	// silently wrong for an event -- and the poller queues a snapshot every two
	// seconds, so a note saved in one browser could go unannounced in another
	// until something unrelated woke it. See queueEvent.
	s.Hub.BroadcastEvent(payload)
}

func (s *Server) notifyState() {
	if s.Hub == nil {
		return
	}
	s.Hub.Notify(func() []byte {
		// A short independent timeout: this runs on the hub's timer, detached
		// from whatever request caused it, and must not hang on a locked
		// database while holding up every other viewer's update.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		payload := s.snapshot(ctx)
		if payload == nil {
			return nil
		}
		s.snapMu.Lock()
		s.lastSnapshot = payload
		s.snapMu.Unlock()
		return payload
	})
}

// HookToken returns the shared secret that authenticates state reports,
// generating it on first use.
func (s *Server) HookToken(ctx context.Context) (string, error) {
	// The memo stays here — every hook report checks the token, so this is on
	// a hot path — but the value itself is the store's, because the admin CLI
	// needs the same one when it creates a session.
	s.tokenOnce.Do(func() {
		s.hookToken, s.tokenError = s.DB.HookToken(ctx)
	})
	return s.hookToken, s.tokenError
}

type hookStateRequest struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
}

// handleHookState accepts a state report from an agent hook.
//
// This is the only precise source of session state: the agent saying what it
// is doing rather than the panel inferring it from bytes. It is optional by
// design — a session whose agent has no hook installed falls back to the
// output heuristic, which is why the hook script can be installed globally
// without affecting anything outside the panel.
func (s *Server) handleHookState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := s.HookToken(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hook token unavailable")
		return
	}
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Constant time: this endpoint is unauthenticated apart from the token, so
	// a timing oracle on it is a timing oracle on the whole thing.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
		// Audited, like the other two paths an unauthenticated caller can
		// reach. The allowlist refusal and a bad setup token both write a row
		// through auditFromOutside; this one wrote nothing at all, so the
		// runbook's diagnosis for "somebody is hammering this panel" -- a
		// GROUP BY over audit_log -- could not see a hook probe.
		//
		// auditFromOutside rather than audit, for the reason its own comment
		// gives: this endpoint is not behind the login throttle, and an
		// ungated write let 400 requests become 400 rows at 237 rows a second.
		// The journal line still goes out every time, which is what fail2ban
		// reads.
		//
		// The value here is noticing, not preventing: the token has full
		// entropy, so nobody is guessing it. What this answers is "has anyone
		// been trying", which was previously unanswerable.
		s.auditFromOutside(r.Context(), "hook.rejected", "", s.clientIP(r), "bad hook token")
		writeErr(w, http.StatusUnauthorized, "bad token")
		return
	}

	var req hookStateRequest
	if !decode(w, r, &req) {
		return
	}
	st := session.State(req.State)
	if !st.Valid() {
		writeErr(w, http.StatusBadRequest, "unknown state "+req.State)
		return
	}
	prev, err := s.DB.GetSession(ctx, req.SessionID)
	if err != nil {
		// An unknown session id is the normal case for an agent started
		// outside the panel that happens to have the hook installed. Say so
		// plainly rather than treating it as an error to be alarmed by.
		s.writeStoreErr(w, err)
		return
	}
	s.Detector.Report(req.SessionID, st, time.Now())
	// The row is kept rather than discarded because the transition has to be
	// recorded from here. A hook is the *accurate* path -- the agent said so
	// itself -- and by the time the poller looks, the detector already agrees
	// with the row, so a poller-only log would miss every hook-driven change
	// and the trends would be drawn from the guesses alone.
	if err := s.setSessionState(ctx, prev, st, session.SourceHook); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

// HandleSignals records what the PTY pump observed. Wired to
// session.Manager.OnSignals; runs on the pump goroutine, so it must be quick.
func (s *Server) HandleSignals(sig session.Signals) {
	now := time.Now()
	if s.Detector != nil {
		s.Detector.Observe(sig.SessionID, sig, now)
	}
	if !sig.Visible {
		return
	}

	s.outMu.Lock()
	if s.outputSeen == nil {
		s.outputSeen = map[string]time.Time{}
	}
	last := s.outputSeen[sig.SessionID]
	if now.Sub(last) < time.Second {
		s.outMu.Unlock()
		return
	}
	s.outputSeen[sig.SessionID] = now
	s.outMu.Unlock()

	// Off the pump goroutine: a database write must never sit between the PTY
	// and the viewers watching it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.DB.TouchSessionOutput(ctx, sig.SessionID, now.Unix()); err != nil {
			s.Log.Debug("touch output", "session", sig.SessionID, "err", err)
		}
	}()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	tv, _ := s.Tmux.Version(r.Context())
	// ok is a claim, and it was an unconditional one. A panel whose database
	// cannot be written answers every request, serves every terminal, and has
	// stopped recording anything — which is precisely the state a health check
	// exists to find.
	stale := s.stale()
	body := map[string]any{
		"ok":          stale == "",
		"version":     version.Version,
		"commit":      version.Commit,
		"tmuxVersion": tv,
		"live":        len(s.Manager.LiveIDs()),
		"passkeys":    s.Cfg.PasskeysUsable(),
	}
	if stale != "" {
		body["stale"] = stale
	}
	writeJSON(w, http.StatusOK, body)
}

// stateResponse is the whole picture in one request.
//
// The UI needs projects and sessions together on every load, and two requests
// would let it render a session list against a project list from a different
// instant.
type stateResponse struct {
	Projects []store.Project `json:"projects"`
	Sessions []store.Session `json:"sessions"`
	Live     []string        `json:"live"`

	// Fullscreen is the sessions with a full-screen program drawing in them.
	//
	// The browser cannot tell: tmux emulates the alternate screen per pane and
	// composes the result, and this panel deliberately keeps tmux's own client
	// out of the alternate screen so that scrollback exists at all. So the byte
	// stream a viewer sees is indistinguishable from ordinary output, and a
	// terminal that offers to scroll back through it lands the reader in
	// whatever was on screen before the agent started -- which is what was
	// reported: "滚动条一滑就滑到在执行 claude 之前的记录了".
	Fullscreen []string `json:"fullscreen"`

	// ProjectOrder is "auto" (most recently active first) or "manual". The UI
	// offers "sort by activity" only when that would change something, rather
	// than showing a control that does nothing.
	ProjectOrder string `json:"projectOrder"`

	// HooksInstalled says which way out to offer when the state is guessed:
	// install the reporter, or reload the sessions that started before it.
	HooksInstalled bool `json:"hooksInstalled"`

	// Stale is why the panel has stopped keeping its records up to date, and
	// empty when it has not. A full disk is the case this was written for: the
	// terminals keep working, so nothing else on screen looks wrong.
	Stale string `json:"stale"`

	// HasProjectOrder is true when an arrangement is stored, whichever
	// ordering is in use. Without it the sidebar has no way to offer the way
	// back: switching to automatic used to erase the arrangement, so there was
	// never anything to return to and the control that did it removed itself.
	HasProjectOrder bool `json:"hasProjectOrder"`

	// StateGuessed is true when an agent is running and nothing is reporting
	// its state, so "waiting for you" is being inferred rather than known.
	//
	// It exists because the inference does not work for the agent most people
	// run here: Claude Code does not ring the terminal bell when it stops for
	// a decision, and the bell is the only signal the heuristic has. Saying so
	// is better than quietly under-reporting the one state the panel is for.
	StateGuessed bool `json:"stateGuessed"`
}

// buildState assembles what every viewer is told about the panel.
//
// One builder, because there were two: this and the WebSocket snapshot listed
// the same fields separately, and adding one to a viewer meant remembering to
// add it to the other. A field carried over the socket and missing from the
// REST answer — or the reverse — is not something anything would have caught.
// It happened while this comment was being written: `stale` reached the
// snapshot and not this handler, and the check that found it was a probe
// looking at the wrong one.
func (s *Server) buildState(ctx context.Context) (stateResponse, error) {
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return stateResponse{}, err
	}
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		return stateResponse{}, err
	}
	manual, err := s.DB.ProjectOrderIsManual(ctx)
	if err != nil {
		return stateResponse{}, err
	}
	hasOrder, err := s.DB.HasProjectOrder(ctx)
	if err != nil {
		return stateResponse{}, err
	}
	return stateResponse{
		Projects:        emptyIfNil(projects),
		Sessions:        emptyIfNil(sessions),
		Live:            emptyIfNil(s.Manager.LiveIDs()),
		Fullscreen:      emptyIfNil(fullscreenNow(&s.fullscreen)),
		ProjectOrder:    orderMode(manual),
		HasProjectOrder: hasOrder,
		StateGuessed:    s.stateIsGuessed(sessions),
		HooksInstalled:  s.hooksAreInstalled(),
		Stale:           s.stale(),
	}, nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	state, err := s.buildState(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// stateIsGuessed reports whether an agent is running with nothing reporting
// its state.
func (s *Server) stateIsGuessed(sessions []store.Session) bool {
	var agent bool
	for _, sess := range sessions {
		if session.IsAgentCommand(sess.Command) {
			agent = true
			break
		}
	}
	if !agent {
		return false
	}
	for _, sess := range sessions {
		// One hook report anywhere is enough: the script is installed globally
		// or not at all.
		if sess.StateSource == session.SourceHook {
			return false
		}
	}
	// Installed is enough to stop asking, and the previous rule -- keep the
	// notice up until a report actually arrives -- was right about the facts
	// and wrong about who they were for.
	//
	// Measured on the owner's own panel, which is where the complaint came
	// from ("现在不知道为啥一直提示着什么状态靠猜"): the reporter is installed,
	// `notify` is on line 8 of ~/.codex/config.toml above the first table, and
	// running the script by hand moved the session to waiting/hook in one
	// second. Nothing is broken. Codex's `notify` fires on turn completion and
	// nothing else, so three sessions that are mid-turn have simply never had
	// anything to report -- and the panel was telling their owner, on every
	// screen, to go and do the thing he had already done.
	//
	// So the banner asks only when there is something to press: an agent is
	// running and no agent's hooks are installed at all. "This particular
	// session has never reported" is a fact about one row and belongs on that
	// row, not across the top of the panel.
	if s.hooksAreInstalled() {
		return false
	}
	return true
}

// hookCheckTTL bounds how stale the cached answer may be. Short enough that
// editing the agent's configuration by hand is noticed while you are still
// looking at the panel, long enough that a burst of state changes costs one
// read rather than one per change.
const hookCheckTTL = 10 * time.Second

// hooksAreInstalled reports whether the reporter is wired into the agent's
// configuration, from cache when it can.
//
// Errors count as "not installed", which is the same answer the uncached
// version gave: unreadable configuration and absent configuration are
// indistinguishable from here, and both mean the panel is guessing.
func (s *Server) hooksAreInstalled() bool {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	if !s.hookCheckedAt.IsZero() && time.Since(s.hookCheckedAt) < hookCheckTTL {
		return s.hookInstalled
	}
	installed := false
	if script, err := s.scriptPath(); err == nil {
		if st, ierr := hooks.Inspect(script); ierr == nil {
			// Either agent counts. This read only Claude's, so a machine where
			// Codex was the one wired up was told to install hooks it already
			// had -- and the notice offering the remedy is the panel's own
			// admission that it is guessing, which it was not.
			installed = st.Installed || st.CodexInstalled
		}
	}
	s.hookInstalled, s.hookCheckedAt = installed, time.Now()
	return installed
}

// forgetHookStatus drops the cached answer, so a change made through the panel
// shows up in the next snapshot rather than up to a TTL later.
func (s *Server) forgetHookStatus() {
	s.hookMu.Lock()
	s.hookCheckedAt = time.Time{}
	s.hookMu.Unlock()
}

// isDirectory reports whether a path is a directory that exists right now.
//
// "Right now" is the point: a project's directory is checked when the project
// is created and can be gone by the time a session is started in it.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func orderMode(manual bool) string {
	if manual {
		return "manual"
	}
	return "auto"
}

type reorderProjectsRequest struct {
	// Ids is the desired order, top first.
	Ids []string `json:"ids"`
	// Auto discards manual positions and returns to activity ordering.
	Auto bool `json:"auto"`
}

func (s *Server) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	var req reorderProjectsRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Auto {
		// Switch the ordering, keep the arrangement. `auto` used to erase every
		// position, which made a clock icon with no confirmation the most
		// destructive control in the sidebar — and it removed itself on the way
		// out, since it only renders in manual mode.
		if err := s.DB.SetProjectOrderManual(ctx, false); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		// No ids: go back to the arrangement already stored. That is the way
		// out of automatic ordering, and before the positions survived the
		// switch there was no such thing to ask for.
		if len(req.Ids) == 0 {
			if err := s.DB.SetProjectOrderManual(ctx, true); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.notifyState()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.DB.ReorderProjects(ctx, req.Ids); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

type createProjectRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := filepath.Abs(expandHome(req.Path))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad path: "+err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot open directory: "+err.Error())
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, abs+" is not a directory")
		return
	}
	if req.Name == "" {
		req.Name = filepath.Base(abs)
	}
	// KNOWN GAP, minor and possibly deliberate: nothing stops the same
	// directory being added twice. `projects.path` has no unique constraint,
	// this does not look, and the dialog does not either. Both rows then take
	// their name from the same basename, so the sidebar shows two entries that
	// are the same pixels with separate notes and todos behind them.
	//
	// The panel has machinery for exactly this problem — disambiguatedLabels —
	// and it works on sessions, not projects.
	//
	// Left alone rather than guarded, because two projects on one directory is
	// arguable rather than obviously wrong; somebody may want the same tree
	// grouped two ways. If it is to be refused, the useful answer names the
	// project already there rather than a UNIQUE constraint failure.
	p, err := s.DB.CreateProject(r.Context(), id.New(), req.Name, abs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusCreated, p)
}

type patchProjectRequest struct {
	Name      *string `json:"name"`
	Pinned    *bool   `json:"pinned"`
	SortIndex *int    `json:"sortIndex"`
	// ClearSortIndex returns the project to automatic activity ordering.
	// A separate flag because a null SortIndex is indistinguishable from an
	// absent one after JSON decoding.
	ClearSortIndex bool `json:"clearSortIndex"`
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	var req patchProjectRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Name != nil {
		if err := s.DB.RenameProject(ctx, pid, *req.Name); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := s.DB.SetProjectPinned(ctx, pid, *req.Pinned); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.ClearSortIndex {
		if err := s.DB.SetProjectSortIndex(ctx, pid, nil); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	} else if req.SortIndex != nil {
		if err := s.DB.SetProjectSortIndex(ctx, pid, req.SortIndex); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	p, err := s.DB.GetProject(ctx, pid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, p)
}

// tearDownSession ends one session: its tmux session first, then the panel's
// attachment to it.
//
// That order is the whole point. A tmux client does not exit when its PTY is
// closed, so detaching first leaves close() waiting on the two-second timer
// that kills the client — measured at 2015ms to delete one session, and 10029ms
// for a project with five, because the loops that call this are serial.
//
// Killing the tmux session first takes that wait away rather than hiding it:
// the client's session is gone, so the client exits on its own and the pump
// sees EOF immediately.
//
// Kill failing before anything else has happened is also the better half of
// the bargain. Detaching first and then failing to kill left the panel with no
// attachment to a tmux session that was still running.
//
// The ctx that reaches here is deliberately not the request's.
//
// Go cancels a request context the moment the client disconnects, and both
// callers loop over sessions killing them one at a time. A tab closed just
// after the click cancelled the context somewhere in that loop, so some tmux
// sessions were dead with their rows intact -- a batch of GONE produced by
// doing nothing wrong. Both callers detach before they start destroying
// anything, the way notifyState's writers already did.
func (s *Server) tearDownSession(ctx context.Context, id, tmuxName string) error {
	if err := s.Tmux.Kill(ctx, tmuxName); err != nil {
		return err
	}
	s.Manager.Detach(id)
	// Without this the detector keeps a tracker per session for the life of the
	// process — small, but it is the kind of asymmetry between two paths that
	// doing the same thing eventually turns into a real bug.
	if s.Detector != nil {
		s.Detector.Forget(id)
	}
	// The same thing, one field over, and it was missed when the line above was
	// written: outputSeen debounces the last_output_at write per session and
	// nothing ever removed an entry. An id that will never appear again is a
	// timestamp kept until the process exits.
	s.outMu.Lock()
	delete(s.outputSeen, id)
	s.outMu.Unlock()
	return nil
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := chi.URLParam(r, "id")

	// Detached, with a bound. Everything below this line destroys something,
	// and half of it is worse than none of it: a killed tmux session whose row
	// survived is a row the panel shows and can never reach again. The request
	// context is cancelled by the client closing its tab, which is not a reason
	// to stop halfway through a delete it already asked for.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	// Kill tmux first: the rows cascade away on delete, and a row that vanishes
	// while its tmux session lives on leaves a process nothing can reach.
	sessions, err := s.DB.ListProjectSessions(opCtx, pid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, sess := range sessions {
		if err := s.tearDownSession(opCtx, sess.ID, sess.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, "kill "+sess.TmuxName+": "+err.Error())
			return
		}
	}
	if err := s.DB.DeleteProject(opCtx, pid); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

type createSessionRequest struct {
	ProjectID string   `json:"projectId"`
	Title     string   `json:"title"`
	Command   []string `json:"command"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`

	// ParentSessionID makes this a scratch terminal under a main session,
	// starting in whatever directory that session is currently in.
	ParentSessionID string `json:"parentSessionId"`

	// LaunchProfileID names the profile to start with: its argv, and its
	// environment.
	//
	// Command above still wins when it is not empty, so a caller that knows
	// exactly what it wants is not made to invent a profile for it. The picker
	// sends only this, and the server resolves it, so the same session can be
	// created with curl and a profile name -- which is the property that keeps
	// the CLI's `session new --profile` from having to reimplement any of it.
	LaunchProfileID string `json:"launchProfileId"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := s.DB.GetProject(ctx, req.ProjectID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if req.Cols <= 0 {
		req.Cols = 120
	}
	if req.Rows <= 0 {
		req.Rows = 32
	}

	// A scratch terminal starts where its parent currently is, not where the
	// project root is. Opening a shell next to an agent that has cd'd into a
	// worktree, and landing somewhere else, is the kind of small wrongness
	// that makes a panel feel untrustworthy.
	dir := p.Path
	var parent *string
	if req.ParentSessionID != "" {
		parentRec, perr := s.DB.GetSession(ctx, req.ParentSessionID)
		if perr != nil {
			s.writeStoreErr(w, perr)
			return
		}
		if parentRec.ParentID != nil {
			writeErr(w, http.StatusBadRequest, "a scratch terminal cannot have scratch terminals of its own")
			return
		}
		if parentRec.ProjectID != p.ID {
			writeErr(w, http.StatusBadRequest, "parent session belongs to a different project")
			return
		}
		if parentRec.CWD != "" {
			dir = parentRec.CWD
		}
		parent = &req.ParentSessionID
	}

	// tmux falls back to $HOME when -c names a directory that is not there, and
	// says nothing about it. So a project whose directory has been removed — a
	// git worktree pruned, a mount gone, a rename — silently produced an agent
	// running in the user's home directory, filed in the sidebar under the
	// project it was not in.
	//
	// Measured: directory deleted, POST /api/sessions returns 201, and
	// pane_current_path is /home/jmr. For a panel whose job is running coding
	// agents, "refactor this" starting in somebody's home directory is the
	// wrong kind of surprise.
	//
	// A scratch terminal inherits its parent's working directory, which can
	// have gone while the project root is still there; the root is a useful
	// place to be and not a lie about where you are. Only when there is
	// nowhere left to stand does this refuse.
	if !isDirectory(dir) {
		if dir != p.Path && isDirectory(p.Path) {
			dir = p.Path
		} else {
			writeErr(w, http.StatusBadRequest,
				"the project directory is not there any more: "+p.Path)
			return
		}
	}

	profile, err := s.launchProfileFor(ctx, req.LaunchProfileID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}

	argv := req.Command
	if len(argv) == 0 && profile != nil {
		argv = profile.Command
	}

	sid := id.New()
	tmuxName := id.TmuxName(sid)

	err = s.Tmux.Create(ctx, tmux.CreateOptions{
		Name:    tmuxName,
		Dir:     dir,
		Command: argv,
		// Hooks identify their session and reach the panel through these. A
		// session created without them simply falls back to the output
		// heuristic, which is why the hook script is safe to install globally:
		// outside the panel the variables are absent and it does nothing.
		//
		// The panel's own go last. tmux takes the last -e when two name the
		// same variable, so ordering is what stops a profile pointing a
		// session's state reports at somebody else's address with the panel's
		// hook token attached. store.LaunchEnv is the one place that knows it.
		Env:    store.LaunchEnv(profile, s.hookEnv(ctx, sid, p.ID)),
		Width:  req.Cols,
		Height: req.Rows,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rec, err := s.DB.CreateSession(ctx, store.Session{
		ID: sid, ProjectID: p.ID, TmuxName: tmuxName,
		Title: req.Title, CWD: dir, Cols: req.Cols, Rows: req.Rows,
		State: session.StateWorking, ParentID: parent,
		// The argv as asked for, which is the only version of it that can be
		// run again. Everything the poller writes into `command` after this is
		// a name for what happens to be in the pane right now.
		//
		// Resolved, not the request's: a profile's argv is what actually ran,
		// and a restore that went back to the profile for it would run whatever
		// the profile says today rather than what this session was started as.
		// The environment is the opposite case and is looked up again on
		// restore -- see LaunchProfileID.
		LaunchCommand:   argv,
		LaunchProfileID: req.LaunchProfileID,
	})
	if err != nil {
		// Untracked tmux session: remove it rather than orphan a process the
		// panel can never show again.
		_ = s.Tmux.Kill(context.WithoutCancel(ctx), tmuxName)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Title != "" {
		if err := s.DB.SetSessionTitle(ctx, sid, req.Title, store.TitleManual); err != nil {
			s.Log.Warn("set initial title", "session", sid, "err", err)
		}
		rec.TitleSource = store.TitleManual
	}
	if err := s.DB.TouchProject(ctx, p.ID); err != nil {
		s.Log.Warn("touch project", "project", p.ID, "err", err)
	}

	// Attach now, not at the next poll. A session can ring the bell within a
	// second of starting, and anything that happens before the pump is running
	// is simply not seen.
	if _, aerr := s.Manager.Attach(ctx, sid, tmuxName, req.Cols, req.Rows); aerr != nil {
		s.Log.Warn("attach new session", "session", sid, "err", aerr)
	}
	s.notifyState()
	writeJSON(w, http.StatusCreated, rec)
}

type patchSessionRequest struct {
	Title          *string `json:"title"`
	Pinned         *bool   `json:"pinned"`
	State          *string `json:"state"`
	SortIndex      *int    `json:"sortIndex"`
	ClearSortIndex bool    `json:"clearSortIndex"`
	// RestoreOnBoot asks for this session to be rebuilt without confirmation
	// the next time the panel starts and finds its tmux session gone.
	RestoreOnBoot *bool `json:"restoreOnBoot"`
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "id")
	var req patchSessionRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Title != nil {
		// A title arriving over the API is a person renaming a tab, so it is
		// recorded as manual and stops automatic updates overwriting it.
		if err := s.DB.SetSessionTitle(ctx, sid, *req.Title, store.TitleManual); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := s.DB.SetSessionPinned(ctx, sid, *req.Pinned); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.State != nil {
		st := session.State(*req.State)
		if !st.Valid() {
			writeErr(w, http.StatusBadRequest, "unknown state "+*req.State)
			return
		}
		// The detector has to know as well: it is what the poller consults, so
		// an override recorded only in the database is undone two seconds later.
		if s.Detector != nil {
			s.Detector.SetManual(sid, st, time.Now())
		}
		if err := s.setSessionStateByID(ctx, sid, st, session.SourceManual); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.RestoreOnBoot != nil {
		if err := s.DB.SetSessionRestoreOnBoot(ctx, sid, *req.RestoreOnBoot); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.ClearSortIndex {
		if err := s.DB.SetSessionSortIndex(ctx, sid, nil); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	} else if req.SortIndex != nil {
		if err := s.DB.SetSessionSortIndex(ctx, sid, req.SortIndex); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, rec)
}

// handleRestartSession brings a dead session's process back.
//
// In place, reusing the pane and the session id, so the sidebar entry, its
// name, its project and its scrollback all stay put. Deleting the session and
// making a new one would lose every one of those.
//
// What respawn-pane does destroy, measured rather than assumed: the pane's
// visible screen, which is where the crash message and the tail of any stack
// trace are. Everything that had already scrolled into history survives. A
// viewer watching at the time keeps the lost screen in the browser's own
// scrollback; a viewer who reloads afterwards does not, because replay is
// rebuilt from tmux's history.
// hookEnv builds the variables a hook inside a session needs to report back.
//
// Shared by creating a session and by restarting one whose tmux session has
// disappeared, because the second builds a session too and a copy of this that
// drifted would produce sessions that silently cannot report their state.
func (s *Server) hookEnv(ctx context.Context, sessionID, projectID string) []string {
	token, terr := s.HookToken(ctx)
	if terr != nil {
		s.Log.Warn("hook token unavailable; sessions will fall back to the heuristic", "err", terr)
		token = ""
	}
	return hooks.SessionEnv(sessionID, projectID, s.Cfg.LoopbackURL(), token)
}

func (s *Server) handleRestartSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Respawn needs a session to respawn into. If the tmux session is gone —
	// killed from a shell, lost with the server, gone in a reboot — there is
	// nothing to restart and this used to answer 500, so the button offered on
	// exactly those rows was the one button that could not work.
	//
	// It then built a bare tmux session under the same name, and ran a login
	// shell in it. The reasoning was correct at the time and is written out in
	// store.Session.LaunchCommand: `command` holds #{pane_current_command},
	// which is a label rather than an argv, and running it would start
	// something nobody asked for.
	//
	// The panel records the real argv now, so that fallback is no longer the
	// best available answer — it was a shell wearing the name of an agent. This
	// branch is restoreSession, which runs what the session was created with
	// and puts the archived scrollback back above it under a banner saying the
	// process below is new. Restarting a *dead pane* is unchanged: respawn in
	// place, which is what the other branch does.
	exists, cerr := s.Tmux.Has(ctx, rec.TmuxName)
	if cerr != nil {
		writeErr(w, http.StatusInternalServerError, cerr.Error())
		return
	}
	if exists {
		// Refuse a session whose process is still running.
		//
		// Respawn is `respawn-pane -k`, which kills whatever is there. Nothing
		// here asked whether there was anything to kill -- the comment above
		// reasons carefully about the tmux session being *gone* and not about
		// it still working. The only guard was the frontend, which renders the
		// button only when the session has exited.
		//
		// The reachable case is the one this panel is built for. Two viewers: A
		// restarts a dead session, B's tab still holds the snapshot from before
		// and still offers the button, and B's click kills the agent A just
		// started. The window is one round trip wide, because notifyState
		// follows the restart -- and "one round trip" is a description of a
		// race, not of a safe interval.
		//
		// 409 rather than 400: nothing about the request is malformed, the
		// state it assumed has changed underneath it, which is exactly what
		// Conflict means. The client already returns to the sign-in screen on
		// 401 and shows the message otherwise, so this arrives as text.
		//
		// If restarting a *live* session is ever wanted -- an agent wedged
		// rather than exited is a real need -- it is a different affordance
		// with a confirm on it, not this one silently doing more than its name.
		if info, ierr := s.Tmux.Get(ctx, rec.TmuxName); ierr == nil && !info.Dead {
			writeErr(w, http.StatusConflict,
				"that session is running again; reload before restarting it")
			return
		}
		if err := s.Tmux.Respawn(ctx, rec.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := s.restoreSession(ctx, rec); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// restoreSession already cleared the exit flag, forgot the old
		// evidence, set the state and attached. Falling through would redo all
		// of it, and would overwrite restored_at's meaning with nothing.
		s.notifyState()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Clear the flag here rather than waiting for the poller: the button the
	// user just pressed has to visibly do something, and a tick of latency
	// reads as "nothing happened" and gets pressed again.
	if err := s.DB.SetSessionExit(ctx, sid, false, 0); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// A respawned pane is a new process behind the same session, so the old
	// evidence describes somebody else. Forgetting is what stops a bell rung
	// by the dead process from being attributed to the new one.
	if s.Detector != nil {
		s.Detector.Forget(sid)
	}
	if err := s.setSessionStateByID(ctx, sid, session.StateWorking, session.SourceHeuristic); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	// Detached, with a bound: see handleDeleteProject. A tab closed just after
	// the click must not leave half a session tree killed.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(opCtx, sid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Children cascade away in the database, but their tmux sessions do not.
	// Deleting the row first would leave processes nothing in the UI can reach.
	children, err := s.DB.ListChildSessions(opCtx, sid)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, child := range append(children, rec) {
		if err := s.tearDownSession(opCtx, child.ID, child.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, "kill "+child.TmuxName+": "+err.Error())
			return
		}
	}
	if err := s.DB.DeleteSession(opCtx, sid); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

// ─── helpers ──────────────────────────────────────────────────────────────

// maxBodyBytes caps request bodies. None of these endpoints takes anything
// large, and an unbounded decode is a free denial of service.
const maxBodyBytes = 1 << 20

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeStoreErr answers a failed database call, and remembers that it failed.
//
// A method rather than a function so the failure is recorded somewhere the
// panel can act on. Measured with the database's writes capped: the request
// that failed got a 500 with the real error, and nothing else changed at all —
// /api/health still said ok, the snapshot said nothing, and the terminals kept
// working because they belong to tmux. The only person who found out was the
// one who happened to press a button.
func (s *Server) writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.noteStale(err)
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// emptyIfNil makes JSON arrays render as [] rather than null, so the frontend
// never has to guard a map over a missing list.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// expandHome resolves a leading ~ so the "new project" dialog accepts the paths
// people actually type.
func expandHome(p string) string {
	if p != "~" && !filepath.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Reconcile makes the database agree with tmux at startup.
//
// The two can disagree in both directions: a session the panel recorded may
// have been killed while it was down, and the panel may have been killed
// between creating a tmux session and writing its row.
func (s *Server) Reconcile(ctx context.Context) error {
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list tmux: %w", err)
	}
	byName := make(map[string]tmux.Info, len(infos))
	for _, i := range infos {
		byName[i.Name] = i
	}

	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list sessions: %w", err)
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		seen[row.TmuxName] = true
		info, alive := byName[row.TmuxName]
		if !alive {
			if err := s.markVanished(ctx, row); err != nil {
				s.Log.Warn("reconcile vanished", "session", row.ID, "err", err)
			}
			continue
		}
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command); err != nil {
			s.Log.Warn("reconcile runtime", "session", row.ID, "err", err)
		}
		// A bell that rang while the panel was down is still latched, because
		// tmux only clears the flag when a client views the window and there
		// was no client. Read it before attaching, which clears it: otherwise
		// restarting the panel loses every "this needs you" raised while it
		// was gone — exactly when the user was not watching.
		if info.Bell && s.Detector != nil {
			s.Detector.Observe(row.ID, session.Signals{Bell: true}, time.Now())
		}
		// Attach at startup too, so state is being watched from the moment the
		// panel is up rather than from the first poll.
		if _, aerr := s.Manager.Attach(ctx, row.ID, row.TmuxName, row.Cols, row.Rows); aerr != nil {
			s.Log.Debug("attach at startup", "session", row.ID, "err", aerr)
		}
	}

	orphans := 0
	for _, i := range infos {
		if !seen[i.Name] {
			orphans++
		}
	}
	if orphans > 0 {
		// Adopting them would guess at which project they belong to. Reporting
		// is honest; `tmux -L <socket> kill-session` is one command away.
		s.Log.Warn("tmux sessions on our socket with no database row",
			"count", orphans, "socket", s.Cfg.TmuxSocket)
	}

	// Last, after every row has been compared with tmux and the missing ones
	// marked. Restoring reads that mark, so doing it inside the loop above
	// would race the loop's own writes.
	s.RestoreFlagged(ctx)
	return nil
}

// pollInterval is how often tmux is asked what changed.
//
// Two seconds is a compromise: each tick shells out to tmux once, and the
// values it refreshes (title, cwd, current command) are things a human reads
// rather than reacts to. Live output does not come through here — it arrives on
// the PTY the instant it is produced.
const pollInterval = 2 * time.Second

// trimInterval is how often the audit log is cut back to its cap while the
// panel is running.
//
// It was trimmed at startup and nowhere else, and the panel is built to run for
// months — `loginctl enable-linger` is in the install instructions because a
// panel that dies when you log out only appears to work. So the bound existed
// exactly when nobody needed it.
//
// Cooldown's overflow case names this trim as what limits the damage when a
// flood is too widely distributed to gate: "the trim on the table is what
// bounds the damage from here". That was written believing this ran. Under a
// sustained flood, "restart the panel" is not a bound.
//
// Five minutes rather than an hour: at the rate measured for the unthrottled
// path — 237 rows a second from one client — an hour of overshoot is most of a
// million rows, and five minutes is a few megabytes.
//
// The tick costs nothing worth counting. Measured on a table sitting exactly at
// the cap, which is the worst case for the no-op — the subquery still walks
// 50,000 index entries to discover there is no boundary row: 1.03ms, against
// 6.6ms for a trim that actually removed 10,000 rows.
const trimInterval = 5 * time.Minute

// Poll keeps the database in step with tmux until the context ends.
//
// The audit trim rides along here rather than in its own goroutine: it is
// database housekeeping on a timer, which is what this loop already is, and a
// second lifecycle to start, stop and get wrong buys nothing.
func (s *Server) Poll(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	every := s.TrimEvery
	if every <= 0 {
		every = trimInterval
	}
	keep := s.AuditKeep
	if keep <= 0 {
		keep = store.AuditKeep
	}
	trim := time.NewTicker(every)
	defer trim.Stop()
	arch := s.ArchiveEvery
	if arch <= 0 {
		arch = archiveInterval
	}
	archive := time.NewTicker(arch)
	defer archive.Stop()
	// The one thing here that is deliberately *not* in this loop. Everything
	// else is periodic work; this is the drain for the session-event log, and
	// putting it in the select would put a database write for a chart on the
	// goroutine that keeps the panel's idea of every session current. See
	// events.go: the producer side is a non-blocking send precisely so that
	// nothing on this goroutine ever waits for that write.
	go s.drainEvents(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-archive.C:
			// Here rather than in a goroutine of its own, for the same reason
			// the audit trim is: this loop already exists to do periodic work
			// against tmux and the database, and a second lifecycle to start,
			// stop and get wrong buys nothing.
			//
			// Not routed through noteStale either. A capture that fails is a
			// session whose scrollback will be a little older than it could
			// have been, not the panel losing track of what the sessions are
			// doing, and the banner says the second thing.
			s.archiveOnce(ctx)
		case <-trim.C:
			// Not routed through noteStale: a failure here is housekeeping
			// falling behind, not the panel losing track of the sessions, and
			// saying "the panel has stopped recording what the sessions are
			// doing" would be false.
			if n, err := s.DB.TrimAuditLog(ctx, keep); err != nil {
				if ctx.Err() == nil {
					s.Log.Warn("trim audit log", "err", err)
				}
			} else if n > 0 {
				s.Log.Info("trimmed audit log", "rows", n)
			}
		case <-t.C:
			if err := s.pollOnce(ctx); err != nil && ctx.Err() == nil {
				s.Log.Debug("poll", "err", err)
				s.noteStale(err)
				continue
			}
			s.clearStale()
			// Push only when the picture actually changed. A tick that
			// broadcasts regardless is polling again, just with the cost moved
			// onto every connected viewer.
			payload := s.snapshot(ctx)
			if payload == nil {
				continue
			}
			s.snapMu.Lock()
			changed := !bytes.Equal(payload, s.lastSnapshot)
			if changed {
				s.lastSnapshot = payload
			}
			s.snapMu.Unlock()
			if changed && s.Hub != nil {
				s.Hub.Broadcast(payload)
			}
		}
	}
}

// markVanished records that a session's tmux session is no longer there.
//
// The row is kept rather than deleted: the user should see what happened and
// dismiss it themselves, because losing a session silently is worse than
// showing a dead one. What is not kept is the state, which used to be frozen
// at whatever it last was — so a session that had been waiting for a human
// went on showing an orange triangle for an agent that no longer exists. The
// panel's one job is answering "which of these needs me", and a permanent
// wrong answer is worse than no answer.
//
// Exited with an unknown status, because that is the truth: nothing observed
// how it ended. Done as well, or it keeps sorting above everything running.
//
// Both the startup reconcile and the poller need this. The poller alone would
// do it within a second or two, but the second or two is exactly the moment
// after a reboot when most sessions are gone and the first thing on screen
// would be a list of triangles asking for attention that nothing needs.
func (s *Server) markVanished(ctx context.Context, row store.Session) error {
	if row.Exited && row.ExitStatus == store.ExitStatusVanished {
		return nil
	}
	if err := s.DB.SetSessionExit(ctx, row.ID, true, store.ExitStatusVanished); err != nil {
		return err
	}
	if row.State == session.StateDone {
		return nil
	}
	return s.setSessionState(ctx, row, session.StateDone, session.SourceHeuristic)
}

func (s *Server) pollOnce(ctx context.Context) error {
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return err
	}
	// No early return when tmux reports nothing.
	//
	// It was here to skip building an empty map, and it skipped the whole
	// reconciliation pass instead — in precisely the state where reconciliation
	// is the only thing that can help: every session gone. Nothing then pruned
	// the detector, so a panel that had just lost its tmux server kept the
	// history of sessions that no longer existed. The loop below does nothing
	// on an empty list anyway; the work above it is what has to run.
	byName := make(map[string]tmux.Info, len(infos))
	for _, i := range infos {
		byName[i.Name] = i
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return err
	}
	if s.Detector != nil {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		s.Detector.Retain(ids)
	}
	// Which panes have a full-screen program drawing in them.
	//
	// Not persisted and not part of the session row: it changes when an agent
	// opens or closes its TUI and has no meaning once the pane is gone. It is
	// collected here because this is the only place that holds tmux's answer,
	// and it is broadcast because the browser cannot work it out. tmux's
	// alternate-screen emulation is per pane, and the panel deliberately keeps
	// tmux's *client* out of the alternate screen -- see the smcup@/rmcup@ line
	// in vibepanel.conf -- so nothing in the byte stream says a TUI is running.
	fullscreen := make([]string, 0, len(rows))
	now := time.Now()
	for _, row := range rows {
		info, alive := byName[row.TmuxName]
		if alive && info.AlternateOn {
			fullscreen = append(fullscreen, row.ID)
		}
		if !alive {
			if err := s.markVanished(ctx, row); err != nil {
				return err
			}
			continue
		}

		// Attach every live session, not only the one somebody is watching.
		//
		// State is inferred from the PTY byte stream, and the panel exists to
		// tell you about the sessions you are *not* looking at. Attaching only
		// on subscribe meant a session could ring the bell, sit there wanting
		// a human, and show as "done" until you happened to click it — the one
		// failure that makes the whole feature pointless.
		//
		// The cost is one tmux client and one replay buffer per session. For
		// the couple of dozen sessions this is built for, that is a few tens of
		// megabytes and a handful of small processes.
		live, attached := s.Manager.Get(row.ID)
		if !attached {
			if l, aerr := s.Manager.Attach(ctx, row.ID, row.TmuxName, row.Cols, row.Rows); aerr != nil {
				s.Log.Debug("attach for monitoring", "session", row.ID, "err", aerr)
			} else {
				live = l
			}
		}
		// This writes on every tick whether anything changed or not, and so does
		// the title update below. It was written down as a finding -- `UPDATE
		// sessions SET cwd = ?, command = ?` has no value comparison, so at the
		// two dozen sessions this is built for and a two-second tick, twenty-four
		// writes a second at complete idle, forever, to the disk the projects live
		// on -- and the premise was wrong. SQLite elides an update that changes
		// nothing.
		//
		// Measured with two instruments that agree, in
		// TestAnUpdateThatChangesNothingDoesNotWrite: a thousand identical calls
		// move `PRAGMA data_version` by zero and grow the WAL by zero bytes, while
		// a thousand that change the row grow it by 4,120,032 -- one page each.
		// The remaining cost is the statement itself, 10µs, which at twenty-four a
		// second is a quarter of a millisecond per second.
		//
		// So the comparison SetSessionExit and SetSessionState make is not a
		// convention these two lines break; it is those two needing it for their
		// own reasons. The guard is left off deliberately. If SQLite ever stops
		// eliding these the test above fails, and then it is worth adding.
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command); err != nil {
			return err
		}
		// The title the PTY saw, which is where a tmux-aware program's OSC
		// arrives. See Live.title: it is not in #{pane_title} and never will
		// be, because passthrough is defined as tmux not looking.
		var ptyTitle string
		if live != nil {
			ptyTitle = live.Title()
		}
		if title := deriveTitle(info, ptyTitle, row.ParentID != nil); title != "" {
			// SetSessionTitle with TitleAuto is a no-op once the user has
			// renamed the tab, so this cannot stomp a manual name.
			if err := s.DB.SetSessionTitle(ctx, row.ID, title, store.TitleAuto); err != nil {
				return err
			}
		}
		// ExitStatus rather than DeadStatus: a killed process leaves the latter
		// empty, so an agent the OOM killer took read as "exited with 0".
		if info.Dead != row.Exited || (info.Dead && info.ExitStatus() != row.ExitStatus) {
			if err := s.DB.SetSessionExit(ctx, row.ID, info.Dead, info.ExitStatus()); err != nil {
				return err
			}
		}
		if s.Detector != nil {
			st, src := s.Detector.Evaluate(row.ID, session.Observation{
				Dead:      info.Dead,
				ShellOnly: session.IsShellCommand(info.Command),
			}, now)
			if st != row.State || src != row.StateSource {
				// setSessionState rather than the store's: it is what records
				// the transition, and doing that here would be a second copy of
				// the comparison below. Neither the write nor the recording can
				// take longer than the write already did -- the log is a
				// non-blocking send onto a bounded queue drained elsewhere,
				// because this loop is what keeps the panel's idea of every
				// session current and nothing hung off it may hold it up.
				if err := s.setSessionState(ctx, row, st, src); err != nil {
					return err
				}
				// On the transition, not on the state. This runs every two
				// seconds against every session; firing on "is waiting" rather
				// than "has just started waiting" would send one notification
				// per tick for as long as somebody is away from their desk,
				// which is the whole time the feature exists to cover.
				if st != row.State {
					s.fireWebhooks(ctx, row, string(st))
				}
			}
		}
	}
	s.fullscreen.Store(&fullscreen)
	return nil
}

// deriveTitle picks the best automatic name for a session.
//
// Four fallbacks, in descending order of how much they tell the user:
//
//  1. The title the application set over OSC 0/2, as tmux recorded it in
//     #{pane_title}. Agents set useful ones. Skipped when it is just the
//     hostname, which is what a plain shell leaves it as and is identical for
//     every session on the box.
//  2. The same thing sent the other way: an OSC wrapped in tmux's passthrough
//     DCS, which reaches the panel's own PTY and never reaches pane_title.
//     That is the form a program that has noticed $TMUX uses, because the title
//     is meant for the terminal a human is looking at rather than for tmux —
//     codex already sends its OSC 9 and OSC 52 exactly that way. The panel
//     parsed those titles, bounded them, broadcast them to the browser, and
//     stored none of them.
//  3. The running command — "claude", "codex" is exactly what is being looked
//     for.
//  4. For a shell, the directory it sits in. Every shell is called "bash", so
//     the command tells you nothing; where it is at least distinguishes the one
//     in a worktree from the one at the repo root.
//
// 1 before 2 because pane_title is live and the PTY title is the last one ever
// seen: a program that stops setting titles should stop naming the session. In
// practice they do not both hold — a program picks one route, and the route it
// picks is the one tmux is not watching.
//
// The directory fallback is skipped for scratch terminals. They live in a tab
// strip that already belongs to one session in one directory, so naming them
// all after it produces a row of identical tabs. The UI numbers those instead.
func deriveTitle(info tmux.Info, ptyTitle string, isScratch bool) string {
	if info.Title != "" && info.Title != hostname() && info.Title != info.Command {
		return info.Title
	}
	if ptyTitle != "" && ptyTitle != hostname() {
		return ptyTitle
	}
	if info.Command != "" && !isShell(info.Command) {
		return info.Command
	}
	if !isScratch && info.Path != "" {
		if base := filepath.Base(info.Path); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	if isShell(info.Command) {
		return ""
	}
	return info.Command
}

// isShell defers to the session package rather than keeping a second list.
//
// There were two, and they had already diverged: this one was missing csh,
// tcsh and the empty string. A session running csh was therefore a shell to
// the state machine — quiet means done rather than working — and not a shell
// to the namer, so it was labelled "csh" instead of falling back to the
// directory it sits in. Two lists that have to agree, in two packages, with
// nothing checking.
//
// session owns the judgement: its version says so in as many words, and the
// state machine is where being a shell has consequences. Naming just wants the
// same answer.
func isShell(cmd string) bool { return session.IsShellCommand(cmd) }

var cachedHostname = func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}()

func hostname() string { return cachedHostname }

// fullscreenNow reads the poller's last answer, or nothing before it has run.
func fullscreenNow(p *atomic.Pointer[[]string]) []string {
	if v := p.Load(); v != nil {
		return *v
	}
	return nil
}
