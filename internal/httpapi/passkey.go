package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// challengeCookie carries the id of an in-flight WebAuthn ceremony.
//
// The challenge itself stays on the server. Handing it to the browser and
// taking it back would mean trusting the client with the one value the whole
// exchange is built on.
const challengeCookie = "vibepanel_webauthn"

// challengeTTL bounds a ceremony. A person taps their key within seconds; a
// challenge that lived for minutes would be a replay window for no benefit.
const challengeTTL = 3 * time.Minute

type challengeStore struct {
	mu    sync.Mutex
	items map[string]*challengeEntry
}

type challengeEntry struct {
	session webauthn.SessionData
	expires time.Time
	// userID is set for registration, where the ceremony belongs to an already
	// signed-in account.
	userID string
}

func newChallengeStore() *challengeStore {
	return &challengeStore{items: map[string]*challengeEntry{}}
}

func (c *challengeStore) put(session webauthn.SessionData, userID string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(b)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	c.items[key] = &challengeEntry{
		session: session,
		expires: time.Now().Add(challengeTTL),
		userID:  userID,
	}
	return key, nil
}

// take returns a challenge and removes it. Single use: a challenge that can be
// presented twice is a replay.
func (c *challengeStore) take(key string) (*challengeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	delete(c.items, key)
	return e, true
}

func (c *challengeStore) sweep() {
	now := time.Now()
	for k, e := range c.items {
		if now.After(e.expires) {
			delete(c.items, k)
		}
	}
}

// webAuthnUser adapts an account to the library's interface.
type webAuthnUser struct {
	user  store.User
	creds []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte { return []byte(u.user.ID) }

func (u *webAuthnUser) WebAuthnName() string { return u.user.Username }

func (u *webAuthnUser) WebAuthnDisplayName() string { return u.user.Username }

func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webAuthn builds the relying party from the current configuration.
//
// Built per request rather than cached because the domain is what defines the
// Relying Party ID, and getting a stale one wrong means every registered
// passkey silently stops working.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	if !s.Cfg.PasskeysUsable() {
		return nil, errors.New("passkeys are not available with this configuration")
	}
	origin := s.Cfg.PublicURL()
	return webauthn.New(&webauthn.Config{
		RPID:          s.Cfg.Domain,
		RPDisplayName: "vibepanel",
		RPOrigins:     []string{origin},
	})
}

func (s *Server) loadWebAuthnUser(r *http.Request, user store.User) (*webAuthnUser, error) {
	rows, err := s.DB.ListCredentials(r.Context(), user.ID)
	if err != nil {
		return nil, err
	}
	out := &webAuthnUser{user: user}
	for _, row := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(row.Data, &c); err != nil {
			// One unreadable credential must not stop the others working; the
			// user can delete it from the settings list.
			s.Log.Warn("unreadable credential", "id", row.ID, "err", err)
			continue
		}
		out.creds = append(out.creds, c)
	}
	return out, nil
}

func (s *Server) registerPasskeyRoutes(r chi.Router) {
	// Sign-in ceremonies are open by necessity: they are how you get a session.
	r.Post("/auth/passkey/login/begin", s.handlePasskeyLoginBegin)
	r.Post("/auth/passkey/login/finish", s.handlePasskeyLoginFinish)

	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)
		r.Get("/auth/passkeys", s.handleListPasskeys)
		r.Post("/auth/passkey/register/begin", s.handlePasskeyRegisterBegin)
		r.Post("/auth/passkey/register/finish", s.handlePasskeyRegisterFinish)
		r.Delete("/auth/passkeys/{credID}", s.handleDeletePasskey)
	})
}

func (s *Server) setChallengeCookie(w http.ResponseWriter, key string) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookie,
		Value:    key,
		Path:     "/api/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cookieSecure(),
		MaxAge:   int(challengeTTL / time.Second),
	})
}

func (s *Server) takeChallenge(r *http.Request) (*challengeEntry, bool) {
	c, err := r.Cookie(challengeCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	return s.Challenges.take(c.Value)
}

// ─── registration ─────────────────────────────────────────────────────────

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userContextKey).(store.User)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	waUser, err := s.loadWebAuthnUser(r, user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	options, session, err := wa.BeginRegistration(waUser,
		// Ask for a discoverable credential so signing in later needs no
		// username — which is the whole appeal of a passkey.
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		// Exclude what is already registered so a second tap on the same key
		// is refused by the browser rather than silently making a duplicate.
		webauthn.WithExclusions(webauthn.Credentials(waUser.creds).CredentialDescriptors()),
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	key, err := s.Challenges.put(*session, user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setChallengeCookie(w, key)
	writeJSON(w, http.StatusOK, options)
}

type finishRegisterRequest struct {
	Name string `json:"name"`
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userContextKey).(store.User)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	entry, ok := s.takeChallenge(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "no registration in progress")
		return
	}
	if entry.userID != user.ID {
		// The ceremony was started by a different account. Finishing it here
		// would attach somebody else's key to this one.
		writeErr(w, http.StatusBadRequest, "registration belongs to another account")
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 64 {
		name = name[:64]
	}

	wa, err := s.webAuthn()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	waUser, err := s.loadWebAuthnUser(r, user)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	credential, err := wa.FinishRegistration(waUser, entry.session, r)
	if err != nil {
		s.audit(r.Context(), "passkey.register.failed", user.Username, s.clientIP(r), err.Error())
		writeErr(w, http.StatusBadRequest, "registration failed: "+err.Error())
		return
	}

	data, err := json.Marshal(credential)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	err = s.DB.CreateCredential(r.Context(), store.Credential{
		ID: id.New(), UserID: user.ID,
		CredentialID: credential.ID, UserHandle: []byte(user.ID),
		Data: data, Name: name, SignCount: credential.Authenticator.SignCount,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r.Context(), "passkey.registered", user.Username, s.clientIP(r), name)
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// ─── sign-in ──────────────────────────────────────────────────────────────

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webAuthn()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Discoverable: the browser offers whichever key it holds for this site,
	// so nothing has to be typed and no username is disclosed before the key
	// proves itself.
	options, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	key, err := s.Challenges.put(*session, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setChallengeCookie(w, key)
	writeJSON(w, http.StatusOK, options)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	if s.Auth != nil {
		if wait, blocked := s.Auth.Throttle.Delay(ip, time.Now()); blocked {
			writeErr(w, http.StatusTooManyRequests,
				"too many attempts; try again in "+wait.Round(time.Second).String())
			return
		}
	}
	entry, ok := s.takeChallenge(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "no sign-in in progress")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var matched store.User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		rows, err := s.DB.CredentialsByUserHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, errors.New("unknown credential")
		}
		user, err := s.DB.UserByID(ctx, rows[0].UserID)
		if err != nil {
			return nil, err
		}
		matched = user
		out := &webAuthnUser{user: user}
		for _, row := range rows {
			var c webauthn.Credential
			if err := json.Unmarshal(row.Data, &c); err != nil {
				continue
			}
			out.creds = append(out.creds, c)
		}
		return out, nil
	}

	_, credential, err := wa.FinishPasskeyLogin(handler, entry.session, r)
	if err != nil {
		s.failLogin(ctx, matched.Username, ip, "passkey: "+err.Error())
		writeErr(w, http.StatusUnauthorized, "passkey was not accepted")
		return
	}
	if credential.Authenticator.CloneWarning {
		// The authenticator's counter went backwards, which is the documented
		// signal that the credential has been copied. Refusing is the whole
		// point of the counter existing.
		s.audit(ctx, "passkey.clone_warning", matched.Username, ip, "sign count went backwards")
		writeErr(w, http.StatusUnauthorized, "this passkey looks cloned; it has been refused")
		return
	}

	if data, merr := json.Marshal(credential); merr == nil {
		if err := s.DB.UpdateCredentialUse(ctx, credential.ID,
			credential.Authenticator.SignCount, data); err != nil {
			s.Log.Warn("update credential use", "err", err)
		}
	}
	if s.Auth != nil {
		s.Auth.Throttle.Succeed(ip)
	}
	s.audit(ctx, "login", matched.Username, ip, "passkey")
	s.issueSession(w, r, matched)
	writeJSON(w, http.StatusOK, authState{
		Configured: true, Authenticated: true, Username: matched.Username,
		PasskeysUsable: s.Cfg.PasskeysUsable(),
	})
}

// ─── management ───────────────────────────────────────────────────────────

func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userContextKey).(store.User)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	rows, err := s.DB.ListCredentials(r.Context(), user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(rows))
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userContextKey).(store.User)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if err := s.DB.DeleteCredential(r.Context(), chi.URLParam(r, "credID"), user.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "passkey.removed", user.Username, s.clientIP(r), "")
	w.WriteHeader(http.StatusNoContent)
}
