package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

type contextKey string

const userContextKey contextKey = "vibepanel.user"

// Auth is the server's authentication state.
type Auth struct {
	Throttle *auth.Throttle

	// SetupToken is printed at startup while no account exists, and is the only
	// thing that authorises creating the first one. Without it, anyone who
	// reached the panel before its owner did would own it.
	SetupToken string

	TrustedProxies []*net.IPNet
	Allow          []*net.IPNet

	// BlockedAudit gates the database write for allowlist rejections, which an
	// outsider can trigger as fast as it can make requests. Nil lets every one
	// through, which is what the tests that do not care about it get.
	BlockedAudit *auth.Cooldown
}

func (s *Server) clientIP(r *http.Request) string {
	if s.Auth == nil {
		return r.RemoteAddr
	}
	return auth.ClientIP(r, s.Auth.TrustedProxies)
}

// auditFromOutside records an event that an unauthenticated caller can produce
// at will.
//
// The journal line goes out every time — fail2ban reads it, and banning an
// address needs to see the individual requests. The database write is gated to
// one per source per minute, because it was not gated at all: 400 requests from
// an address the allowlist rejects wrote 400 rows, at 237 rows/sec, and nothing
// on that path is behind authentication or the login throttle.
func (s *Server) auditFromOutside(ctx context.Context, event, username, ip, detail string) {
	if s.Auth != nil && !s.Auth.BlockedAudit.Allow(event, ip, time.Now()) {
		s.Log.Info("audit", "event", event, "user", username, "ip", ip, "detail", detail)
		return
	}
	s.audit(ctx, event, username, ip, detail)
}

// audit records an event to the database and to the log.
//
// Both, deliberately: the database backs the settings page, and the log line
// is what a fail2ban rule or a human tailing journalctl can act on.
func (s *Server) audit(ctx context.Context, event, username, ip, detail string) {
	if err := s.DB.Audit(ctx, store.AuditEntry{
		Event: event, Username: username, IP: ip, Detail: detail,
	}); err != nil {
		s.Log.Warn("audit write", "err", err)
	}
	s.Log.Info("audit", "event", event, "user", username, "ip", ip, "detail", detail)
}

// cookieSecure reports whether the session cookie may carry the Secure flag.
//
// Setting it unconditionally would break a plain-HTTP deployment entirely: the
// browser would refuse to send the cookie back and every request would look
// unauthenticated, with nothing on screen to explain why.
func (s *Server) cookieSecure() bool { return s.Cfg.TLSMode != "off" && s.Cfg.TLSMode != "" }

// ─── middleware ───────────────────────────────────────────────────────────

// RequireAuth rejects requests without a valid session.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if s.Auth != nil && !auth.Allowed(s.clientIP(r), s.Auth.Allow) {
			s.auditFromOutside(ctx, "blocked", "", s.clientIP(r), "address not in the allowlist")
			writeErr(w, http.StatusForbidden, "not allowed from this address")
			return
		}

		user, ok, uerr := s.currentUser(r)
		if uerr != nil {
			// Not 401: the session may be perfectly good and the panel simply
			// cannot look it up. Saying "sign in required" sends somebody to a
			// login form that goes to the same database.
			s.noteStale(uerr)
			s.Log.Warn("cannot check the session", "err", uerr)
			writeErr(w, http.StatusServiceUnavailable,
				"the panel cannot reach its own database; your sessions are unaffected")
			return
		}
		if !ok {
			// A distinct code for "never set up" so the browser can show the
			// setup screen rather than a login form nobody can satisfy.
			n, err := s.DB.CountUsers(ctx)
			if err == nil && n == 0 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "not configured", "setupRequired": true,
				})
				return
			}
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, userContextKey, user)))
	})
}

// stillAuthorized re-runs the checks RequireAuth made, for a request whose
// connection is still open.
//
// The same two questions in the same order, so that a connection cannot
// outlive a rule an ordinary request would now fail: is this address still
// allowed, and does this session still exist. Both can change while a
// WebSocket is open, and a WebSocket is open for hours.
func (s *Server) stillAuthorized(r *http.Request) bool {
	if s.Auth != nil && !auth.Allowed(s.clientIP(r), s.Auth.Allow) {
		return false
	}
	_, ok, _ := s.currentUser(r)
	return ok
}

// currentUserFrom reads the account the middleware attached to the request.
func currentUserFrom(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userContextKey).(store.User)
	return u, ok
}

// currentUser resolves the session cookie.
// currentUser answers who is making this request.
//
// Three outcomes, not two. "No session" and "the database cannot say" were the
// same answer, so a panel whose disk had filled told every viewer to sign in —
// and the sign-in went to the same broken database, so they would try again,
// and again, until the login throttle locked them out of a panel that was only
// ever short of space. Measured: with the database closed, every authenticated
// request answered 401 "sign in required".
//
// Refusing either way is right and stays. What changes is that the panel says
// which of the two it is.
func (s *Server) currentUser(r *http.Request) (store.User, bool, error) {
	token := auth.TokenFromRequest(r)
	if token == "" {
		return store.User{}, false, nil
	}
	ctx := r.Context()
	hash := auth.HashToken(token)
	sess, err := s.DB.AuthSessionByToken(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	user, err := s.DB.UserByID(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, false, nil
	}
	if err != nil {
		return store.User{}, false, err
	}
	// Best effort; a failed touch must not fail the request.
	if err := s.DB.TouchAuthSession(ctx, hash); err != nil {
		s.Log.Debug("touch auth session", "err", err)
	}
	return user, true, nil
}

// ─── handlers ─────────────────────────────────────────────────────────────

func (s *Server) registerAuthRoutes(r interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	r.Get("/auth/state", s.handleAuthState)
	r.Post("/auth/setup", s.handleSetup)
	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/logout", s.handleLogout)
	// Behind RequireAuth by its own check rather than by the router, because
	// the auth routes are registered outside the authenticated group.
	r.Post("/auth/password", s.handleChangePassword)
}

type authState struct {
	Configured     bool   `json:"configured"`
	Authenticated  bool   `json:"authenticated"`
	Username       string `json:"username,omitempty"`
	PasskeysUsable bool   `json:"passkeysUsable"`
	// PasskeyReason explains a disabled passkey button instead of leaving the
	// user to guess why the browser refused.
	PasskeyReason string `json:"passkeyReason,omitempty"`
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	n, err := s.DB.CountUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	state := authState{
		Configured:     n > 0,
		PasskeysUsable: s.Cfg.PasskeysUsable(),
	}
	if !state.PasskeysUsable {
		switch {
		case s.Cfg.Domain == "":
			state.PasskeyReason = "no --domain is set; WebAuthn needs a hostname"
		case net.ParseIP(s.Cfg.Domain) != nil:
			state.PasskeyReason = "an IP address cannot be a WebAuthn Relying Party ID"
		default:
			state.PasskeyReason = "passkeys need HTTPS, or localhost"
		}
	}
	if u, ok, _ := s.currentUser(r); ok {
		state.Authenticated = true
		state.Username = u.Username
	}
	writeJSON(w, http.StatusOK, state)
}

type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	n, err := s.DB.CountUsers(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n > 0 {
		// Once an account exists this endpoint is closed forever. Leaving it
		// open would be a second way in that nobody is watching.
		writeErr(w, http.StatusConflict, "already configured")
		return
	}

	var req setupRequest
	if !decode(w, r, &req) {
		return
	}
	if s.Auth == nil || s.Auth.SetupToken == "" {
		writeErr(w, http.StatusInternalServerError, "setup is not available")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Auth.SetupToken)) != 1 {
		// Reachable only while no account exists, which is exactly when nobody
		// is watching the panel yet. Unauthenticated and unthrottled, like the
		// allowlist refusal, so it is recorded the same way.
		s.auditFromOutside(ctx, "setup.rejected", req.Username, ip, "bad setup token")
		writeErr(w, http.StatusUnauthorized, "bad setup token")
		return
	}
	if err := validateCredentials(req.Username, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := s.DB.CreateUser(ctx, id.New(), req.Username, hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(ctx, "setup.completed", user.Username, ip, "")
	s.issueSession(w, r, user)
	writeJSON(w, http.StatusCreated, authState{
		Configured: true, Authenticated: true, Username: user.Username,
		PasskeysUsable: s.Cfg.PasskeysUsable(),
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	if s.Auth != nil && !auth.Allowed(s.clientIP(r), s.Auth.Allow) {
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	if s.Auth != nil {
		if wait, blocked := s.Auth.Throttle.Delay(ip, time.Now()); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests,
				"too many attempts; try again in "+wait.Round(time.Second).String())
			return
		}
	}

	var req loginRequest
	if !decode(w, r, &req) {
		return
	}

	user, err := s.DB.UserByName(ctx, req.Username)
	if err != nil {
		// The same response and the same work either way: a fast "no such
		// user" and a slow "wrong password" tells an attacker which usernames
		// exist.
		auth.DummyVerify(req.Password)
		s.failLogin(ctx, req.Username, ip, "unknown user")
		writeErr(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	ok, verr := auth.VerifyPassword(req.Password, user.PasswordHash)
	if verr != nil {
		s.Log.Warn("password verify", "err", verr)
	}
	if !ok {
		s.failLogin(ctx, req.Username, ip, "wrong password")
		writeErr(w, http.StatusUnauthorized, "wrong username or password")
		return
	}

	if s.Auth != nil {
		s.Auth.Throttle.Succeed(ip)
	}
	s.audit(ctx, "login", user.Username, ip, "password")
	s.issueSession(w, r, user)
	writeJSON(w, http.StatusOK, authState{
		Configured: true, Authenticated: true, Username: user.Username,
		PasskeysUsable: s.Cfg.PasskeysUsable(),
	})
}

func (s *Server) failLogin(ctx context.Context, username, ip, detail string) {
	if s.Auth != nil {
		s.Auth.Throttle.Fail(ip, time.Now())
	}
	s.audit(ctx, "login.failed", username, ip, detail)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := auth.TokenFromRequest(r); token != "" {
		if err := s.DB.DeleteAuthSession(r.Context(), auth.HashToken(token)); err != nil {
			s.Log.Warn("delete auth session", "err", err)
		}
	}
	auth.ClearCookie(w, s.cookieSecure())
	w.WriteHeader(http.StatusNoContent)
}

// issueSession creates a session and sets the cookie.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user store.User) {
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create a session")
		return
	}
	err = s.DB.CreateAuthSession(r.Context(), auth.HashToken(token), user.ID,
		auth.SessionTTL, r.UserAgent(), s.clientIP(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create a session")
		return
	}
	auth.SetCookie(w, token, s.cookieSecure())
}

func validateCredentials(username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is required")
	}
	if len(username) > 64 {
		return errors.New("username is too long")
	}
	if len(password) < auth.MinPasswordLength {
		return errors.New("password must be at least " +
			strconv.Itoa(auth.MinPasswordLength) + " characters")
	}
	if len(password) > 1024 {
		return errors.New("password is too long")
	}
	return nil
}

type changePasswordRequest struct {
	Current string `json:"current"`
	Next    string `json:"next"`
}

// handleChangePassword replaces the account password.
//
// There was no way to change it, from anywhere. The setup wizard sets one once
// and nothing could ever replace it, so the answer to "this leaked" or "I typed
// it into the wrong window" was to stop the panel and edit SQLite by hand.
//
// Three things it does that a naive version would not:
//
//  1. Requires the current password. A stolen session cookie is then not enough
//     to lock the owner out of their own panel — which is the difference
//     between an intruder who can read your terminals and one who owns them.
//  2. Throttles on failure, through the same limiter as sign-in. Otherwise this
//     endpoint is an unthrottled oracle for guessing the password that the
//     login page refuses to be.
//  3. Signs every other browser out. The reason to change a password is that
//     somebody else might have the old one; leaving their session alive makes
//     the change decorative. This browser keeps its session, because being
//     logged out of the page you just used to change your password reads as
//     the change having failed.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	user, ok, _ := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if s.Auth != nil {
		if wait, blocked := s.Auth.Throttle.Delay(ip, time.Now()); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeErr(w, http.StatusTooManyRequests,
				"too many attempts; try again in "+wait.Round(time.Second).String())
			return
		}
	}

	var req changePasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	okPass, err := auth.VerifyPassword(req.Current, user.PasswordHash)
	if err != nil || !okPass {
		if s.Auth != nil {
			s.Auth.Throttle.Fail(ip, time.Now())
		}
		s.audit(ctx, "password_change_refused", user.Username, ip, "current password did not match")
		writeErr(w, http.StatusUnauthorized, "the current password is wrong")
		return
	}
	if err := validateCredentials(user.Username, req.Next); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Next == req.Current {
		writeErr(w, http.StatusBadRequest, "the new password is the same as the old one")
		return
	}

	hash, err := auth.HashPassword(req.Next)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash the password")
		return
	}
	if err := s.DB.SetPasswordHash(ctx, user.ID, hash); err != nil {
		s.writeStoreErr(w, err)
		return
	}

	// Everywhere else, then this browser back in. Order matters: dropping all
	// of them and re-issuing is simpler to reason about than trying to spare
	// one row, and it means a failure half way through leaves nobody signed in
	// rather than everybody.
	if err := s.DB.DeleteUserAuthSessions(ctx, user.ID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.issueSession(w, r, user)
	if s.Auth != nil {
		s.Auth.Throttle.Succeed(ip)
	}
	s.audit(ctx, "password_changed", user.Username, ip, "other sessions signed out")
	w.WriteHeader(http.StatusNoContent)
}
