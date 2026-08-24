package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// CookieName is the session cookie.
const CookieName = "vibepanel_session"

// SessionTTL is how long a sign-in lasts.
//
// Long, because this is a tool someone leaves open on a phone and a desktop
// and does not want to think about; revocable, because the sessions live in
// the database and can be deleted individually.
const SessionTTL = 30 * 24 * time.Hour

// tokenBytes is the size of a session token. 32 bytes of crypto/rand is well
// beyond guessing.
const tokenBytes = 32

// NewToken returns a fresh session token.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns what gets stored.
//
// SHA-256 rather than a password hash: the token is already 32 random bytes,
// so there is nothing to brute force and no reason to make every request pay
// for argon2. Hashing at all is so that a leaked database does not hand over
// live sessions.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// SetCookie writes the session cookie.
func SetCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:  CookieName,
		Value: token,
		Path:  "/",
		// HttpOnly: script must not be able to read a token that opens a
		// terminal. SameSite=Strict: no cross-site request should ever carry
		// it, and this panel is never embedded anywhere.
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		Expires:  time.Now().Add(SessionTTL),
		MaxAge:   int(SessionTTL / time.Second),
	})
}

// ClearCookie removes the session cookie.
func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// TokenFromRequest reads the session token, if there is one.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
