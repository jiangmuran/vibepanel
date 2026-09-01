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

// maxChallenges caps how many ceremonies can be in flight at once.
//
// `login/begin` is necessarily public — it is how you sign in — and it
// allocates. Without a cap, an anonymous caller decides how much of the
// panel's memory to use and how much of its CPU to spend, because every put
// and take sweeps the whole map for expired entries.
//
// Measured before the cap, one laptop against a local panel: 70,238
// unauthenticated requests in 25 seconds, none refused, resident memory up 21
// MiB, and the request rate falling from about 6,300 a second to about 1,300
// as the sweep grew. It self-limits, in the sense that a fire self-limits when
// it runs out of house.
//
// Four thousand is far past any real use. A ceremony lasts three minutes, so
// reaching this needs thousands of people tapping a key at the same moment. At
// the cap the sweep stays cheap and the refusal is instant, which is the point:
// the cost of an attack becomes flat.
const maxChallenges = 4096

// challengeStatus keeps a full store from being reported as a panel fault.
// It is a temporary refusal, and 500 would send whoever is on call looking for
// a bug that is not there.
func challengeStatus(err error) int {
	if errors.Is(err, errTooManyChallenges) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// errTooManyChallenges is returned when the cap is reached. Password sign-in is
// unaffected, which is what the handler says: passkeys are an addition, never
// the only door.
var errTooManyChallenges = errors.New(
	"too many sign-ins in progress; use your password, or try again in a moment")

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
	// After the sweep, so that a burst that has aged out costs nothing.
	if len(c.items) >= maxChallenges {
		return "", errTooManyChallenges
	}
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
func (s *Server) webAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	if !s.Cfg.PasskeysUsable() {
		return nil, errors.New("passkeys are not available with this configuration")
	}
	// Every origin this panel can legitimately be browsed at, not just the one
	// it would build for itself.
	//
	// go-webauthn compares the ceremony's origin exactly, port included. With
	// a proxy in front, the origin the browser sends is `https://panel` while
	// `PublicURL()` said `http://panel:18443`, so every registration and every
	// sign-in failed on the server side -- and the sign-in failure counts
	// against the login throttle, so a misconfigured origin looks like a bad
	// key and then locks the account out.
	origins := s.publicOrigins(r)
	if u := s.Cfg.PublicURL(); u != "" {
		origins = append(origins, u)
	}
	return webauthn.New(&webauthn.Config{
		RPID:          s.Cfg.Domain,
		RPDisplayName: "vibepanel",
		RPOrigins:     origins,
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

// setChallengeCookie parks the key to a ceremony in flight in the browser.
//
// Secure is answered from the request for the same reason the session cookie's
// is: behind the nginx/Caddy deployment this panel documents, TLSMode is "off"
// while the browser is on https, so a cookie trusting TLSMode goes out with no
// Secure flag and rides the next plain-http request to that hostname in clear.
// Shorter-lived than the session token, but it is still the handle to an
// authentication in progress.
func (s *Server) setChallengeCookie(w http.ResponseWriter, r *http.Request, key string) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookie,
		Value:    key,
		Path:     "/api/auth",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cookieSecureFor(r),
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
	wa, err := s.webAuthn(r)
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
		writeErr(w, challengeStatus(err), err.Error())
		return
	}
	s.setChallengeCookie(w, r, key)
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
	// Runes, not bytes. len() and slicing are both byte-wise, so a cut at 64
	// bytes lands inside a multi-byte character and stores an invalid sequence
	// that renders as a replacement character. Sixty-four bytes is about
	// twenty-one Chinese characters, which a descriptive name reaches easily —
	// and these names are typed by hand, in whatever language the person uses.
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}

	wa, err := s.webAuthn(r)
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
	wa, err := s.webAuthn(r)
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
		writeErr(w, challengeStatus(err), err.Error())
		return
	}
	s.setChallengeCookie(w, r, key)
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
	wa, err := s.webAuthn(r)
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
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "passkey.removed", user.Username, s.clientIP(r), "")
	w.WriteHeader(http.StatusNoContent)
}
