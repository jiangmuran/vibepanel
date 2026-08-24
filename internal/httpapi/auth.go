package httpapi

import (
	"context"
	"crypto/subtle"
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
}

func (s *Server) clientIP(r *http.Request) string {
	if s.Auth == nil {
		return r.RemoteAddr
	}
	return auth.ClientIP(r, s.Auth.TrustedProxies)
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
			s.audit(ctx, "blocked", "", s.clientIP(r), "address not in the allowlist")
			writeErr(w, http.StatusForbidden, "not allowed from this address")
			return
		}

		user, ok := s.currentUser(r)
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

// currentUserFrom reads the account the middleware attached to the request.
func currentUserFrom(r *http.Request) (store.User, bool) {
	u, ok := r.Context().Value(userContextKey).(store.User)
	return u, ok
}

// currentUser resolves the session cookie.
func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	token := auth.TokenFromRequest(r)
	if token == "" {
		return store.User{}, false
	}
	ctx := r.Context()
	hash := auth.HashToken(token)
	sess, err := s.DB.AuthSessionByToken(ctx, hash)
	if err != nil {
		return store.User{}, false
	}
	user, err := s.DB.UserByID(ctx, sess.UserID)
	if err != nil {
		return store.User{}, false
	}
	// Best effort; a failed touch must not fail the request.
	if err := s.DB.TouchAuthSession(ctx, hash); err != nil {
		s.Log.Debug("touch auth session", "err", err)
	}
	return user, true
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
	if u, ok := s.currentUser(r); ok {
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
		s.audit(ctx, "setup.rejected", req.Username, ip, "bad setup token")
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
