package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// anonymousClient is a client with no session, for checking what a stranger
// can reach.
func anonymousClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

// openRoutes are the only routes a stranger may reach, each for a stated
// reason. Adding to this list is the deliberate act; adding a route is not.
// Exact paths, not prefixes. `/api/auth/` as a prefix looked right and exempted
// four routes that do require a session — listing and deleting passkeys, and
// both halves of registering one. They answer 401 today because each handler
// asks currentUser itself; a prefix exemption is a promise that they always
// will, made on their behalf by a test that would then never check.
var openRoutes = map[string]string{
	"/api/health":                    "a probe, and it says nothing sensitive",
	"/api/hook/state":                "runs outside the browser as a child of an agent, and carries its own token",
	"/api/auth/login":                "how you get in; the login throttle guards it instead",
	"/api/auth/logout":               "ending a session you may not have is not an error worth a 401",
	"/api/auth/setup":                "the one-time setup token is the credential, and it closes forever after",
	"/api/auth/state":                "tells the sign-in page which doors exist; it is what a stranger needs",
	"/api/auth/passkey/login/begin":  "signing in with a passkey, before there is a session to require",
	"/api/auth/passkey/login/finish": "the other half of the same",
}

// `/api/share/{token}/dashboard` is deliberately NOT in the list above, and the
// reason is worth a paragraph because it looks like an omission.
//
// A stranger holding a share link can reach it, so by the letter of the comment
// on openRoutes it belongs there. But the walk below substitutes "anything" for
// each path parameter, and "anything" is not a link — so the route answers 401
// and is checked like everything else. Listing it would exempt from the walk the
// one route in this panel that answers without a session, which is exactly the
// route that most needs a test standing over it.
//
// What that check proves is narrow: without a valid capability, this is not a
// way in. That a valid capability reaches the dashboard and nothing else is
// share_test.go's job, and it presents the token as a cookie and as a Bearer
// header against nine routes and the socket to prove it.

// TestEverythingRequiresASession walks the router rather than listing paths.
//
// This panel hands out a writable terminal. There is no such thing as a
// harmless unauthenticated endpoint on it.
//
// It used to check seven paths written out by hand, out of thirty-seven. The
// ones it did not name include DELETE /api/projects/{id}, which kills every
// session in a project, GET /api/settings/audit, which is a list of usernames
// and addresses, the upload endpoint, and /ws, which is the terminal itself.
// They were all behind RequireAuth — but nothing said so, and nothing would
// have noticed a new route registered above the group instead of inside it,
// which is one line's difference and invisible in a diff of a file this size.
func TestEverythingRequiresASession(t *testing.T) {
	ts, srv := newTestServer(t)
	anon := anonymousClient(t)

	routes, ok := srv.Routes().(chi.Routes)
	if !ok {
		t.Fatalf("Routes() is %T, which cannot be walked; this test is checking nothing", srv.Routes())
	}

	checked := 0
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// The frontend is served to strangers on purpose: the login page is
		// part of it.
		if strings.HasPrefix(route, "/*") {
			return nil
		}
		if _, open := openRoutes[route]; open {
			return nil
		}
		// Chi's pattern, with a value in place of each parameter. What it
		// names does not have to exist: the middleware answers before any
		// handler looks it up, and if it does not, that is the finding.
		path := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(route, "anything")

		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader("{}"))
		if err != nil {
			// CONNECT and friends are registered by Handle() for /ws; skip
			// what the client cannot even build.
			return nil
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := anon.Do(req)
		if err != nil {
			return nil
		}
		res.Body.Close()
		checked++
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d without a session, want 401. Either it is registered "+
				"above the RequireAuth group, or it belongs in openRoutes with a reason.",
				method, path, res.StatusCode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A walk that finds nothing passes silently, which is the failure this
	// whole test exists to avoid one layer down.
	if checked < 20 {
		t.Fatalf("only %d routes were checked; the walk is not seeing the router", checked)
	}
}

func TestWebSocketRequiresASession(t *testing.T) {
	ts, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The socket is the terminal itself. If it were reachable without a
	// session, everything else being locked would be decoration.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, res, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		c.CloseNow()
		t.Fatal("an unauthenticated websocket dial succeeded")
	}
	if res != nil && res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestHealthStaysOpen(t *testing.T) {
	ts, _ := newTestServer(t)
	// A probe that needs credentials is not a probe. It says nothing a
	// stranger could use.
	res, err := anonymousClient(t).Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	for _, leak := range []string{"password", "token", "hash"} {
		if strings.Contains(strings.ToLower(string(b)), leak) {
			t.Errorf("health mentions %q: %s", leak, b)
		}
	}
}

func TestSetupOnlyWorksOnce(t *testing.T) {
	ts, _ := newTestServer(t) // already ran setup
	res, err := anonymousClient(t).Post(ts.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"token":"test-setup-token","username":"second","password":"another long password"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	// Leaving it open would be a second way in that nobody is watching.
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
}

func TestLoginAndLogout(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	client := anonymousClient(t)

	login := func(body string) *http.Response {
		res, err := client.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		return res
	}

	res := login(`{"username":"tester","password":"wrong"}`)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", res.StatusCode)
	}

	// The throttle now stands in the way, which is the point; wait it out.
	srv.Auth.Throttle.Succeed("127.0.0.1")

	res = login(`{"username":"tester","password":"a sufficiently long password"}`)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("correct password = %d, want 200", res.StatusCode)
	}

	stateRes, err := client.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET state: %v", err)
	}
	stateRes.Body.Close()
	if stateRes.StatusCode != http.StatusOK {
		t.Fatalf("state after login = %d", stateRes.StatusCode)
	}

	logout, err := client.Post(ts.URL+"/api/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	logout.Body.Close()

	// The session must be gone server-side, not merely forgotten by the
	// browser: a stolen cookie has to stop working.
	stateRes, _ = client.Get(ts.URL + "/api/state")
	stateRes.Body.Close()
	if stateRes.StatusCode != http.StatusUnauthorized {
		t.Errorf("state after logout = %d, want 401", stateRes.StatusCode)
	}

	entries, err := srv.DB.RecentAudit(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudit: %v", err)
	}
	var sawFailure, sawLogin bool
	for _, e := range entries {
		if e.Event == "login.failed" {
			sawFailure = true
		}
		if e.Event == "login" {
			sawLogin = true
		}
	}
	if !sawFailure || !sawLogin {
		t.Errorf("audit log is missing entries: %+v", entries)
	}
}

func TestRepeatedFailuresAreThrottled(t *testing.T) {
	ts, _ := newTestServer(t)
	client := anonymousClient(t)

	post := func() int {
		res, err := client.Post(ts.URL+"/api/auth/login", "application/json",
			strings.NewReader(`{"username":"tester","password":"nope"}`))
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := post(); code != http.StatusUnauthorized {
		t.Fatalf("first attempt = %d, want 401", code)
	}
	// An unthrottled login endpoint on the open internet is a free
	// password-guessing service.
	if code := post(); code != http.StatusTooManyRequests {
		t.Errorf("second immediate attempt = %d, want 429", code)
	}
}

func TestUnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	ts, srv := newTestServer(t)
	client := anonymousClient(t)

	body := func(username string) string {
		res, err := client.Post(ts.URL+"/api/auth/login", "application/json",
			strings.NewReader(`{"username":"`+username+`","password":"definitely wrong"}`))
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return fmt.Sprintf("%d %s", res.StatusCode, b)
	}

	// Different answers here tell an attacker which usernames exist, which is
	// the first half of guessing a password.
	wrongPassword := body("tester")
	srv.Auth.Throttle.Succeed("127.0.0.1")
	noSuchUser := body("nobody-at-all")
	if wrongPassword != noSuchUser {
		t.Errorf("responses differ:\n  wrong password: %q\n  unknown user:   %q",
			wrongPassword, noSuchUser)
	}
}

func TestAllowlistBlocksOtherAddresses(t *testing.T) {
	ts, srv := newTestServer(t)
	nets, err := auth.ParseCIDRs([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.Allow = nets

	// The test client comes from loopback, which is not on the list.
	res, err := ts.Client().Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
}

func TestAuthStateDescribesPasskeyAvailability(t *testing.T) {
	ts, srv := newTestServer(t)

	// A disabled button with no explanation is worse than no button. The
	// browser's own error for this is opaque.
	srv.Cfg.Domain = "192.168.8.4"
	res, err := ts.Client().Get(ts.URL + "/api/auth/state")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var state authState
	json.NewDecoder(res.Body).Decode(&state) //nolint:errcheck
	res.Body.Close()
	if state.PasskeysUsable {
		t.Error("passkeys reported usable with an IP address as the domain")
	}
	if !strings.Contains(state.PasskeyReason, "IP address") {
		t.Errorf("reason = %q, want it to mention the IP address", state.PasskeyReason)
	}
}

func TestPasswordHashIsNeverReturned(t *testing.T) {
	ts, srv := newTestServer(t)
	user, err := srv.DB.UserByName(context.Background(), "tester")
	if err != nil {
		t.Fatalf("UserByName: %v", err)
	}
	if user.PasswordHash == "" {
		t.Fatal("no stored hash")
	}
	// The struct has a json:"-" on it; this checks nothing else serialises it.
	b, err := json.Marshal(store.User{ID: user.ID, Username: user.Username, PasswordHash: user.PasswordHash})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "argon2") {
		t.Errorf("a serialised user carries its password hash: %s", b)
	}
	res, _ := ts.Client().Get(ts.URL + "/api/auth/state")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "argon2") {
		t.Errorf("auth state leaks the hash: %s", body)
	}
}

// A header must never decide who the caller is.
//
// chi's middleware.RealIP used to run in front of everything and rewrite
// r.RemoteAddr from X-Forwarded-For or X-Real-IP with no trust model, so the
// two controls that exist to keep strangers out both read an address the
// stranger supplied. Both were measured bypassable before this was fixed.
func TestAddressCannotBeSpoofedByAHeader(t *testing.T) {
	ts, srv := newTestServer(t)
	// A network the loopback interface is certainly not on.
	nets, err := auth.ParseCIDRs([]string{"10.99.99.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.Allow = nets

	get := func(headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	if code := get(nil); code != http.StatusForbidden {
		t.Fatalf("an address outside the allowlist got %d, want 403", code)
	}
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		if code := get(map[string]string{header: "10.99.99.5"}); code != http.StatusForbidden {
			t.Errorf("%s: claiming an allowed address got %d; the allowlist is bypassable "+
				"with one header", header, code)
		}
	}
	// Chained values are the other shape of the same attempt.
	if code := get(map[string]string{"X-Forwarded-For": "10.99.99.5, 203.0.113.1"}); code != http.StatusForbidden {
		t.Errorf("a chained X-Forwarded-For got %d, want 403", code)
	}
}

// The throttle is worthless if a new header value buys a fresh budget: an
// attacker rotates it per request and never sees a 429.
func TestThrottleIsNotResetByAHeader(t *testing.T) {
	ts, _ := newTestServer(t) // the account already exists

	attempt := func(forwarded string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/login",
			strings.NewReader(`{"username":"tester","password":"definitely wrong"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	if code := attempt("198.51.100.1"); code != http.StatusUnauthorized {
		t.Fatalf("first wrong password got %d, want 401", code)
	}
	// A different claimed address, immediately after. Same real caller, so the
	// throttle must already be holding.
	if code := attempt("198.51.100.2"); code != http.StatusTooManyRequests {
		t.Errorf("a second wrong password from a new X-Forwarded-For got %d, want 429; "+
			"brute force is unthrottled", code)
	}
}

// Changing the password, which used to be impossible from anywhere.
func TestChangePassword(t *testing.T) {
	const current = "a sufficiently long password"
	const next = "an even longer password here"

	post := func(t *testing.T, ts *httptest.Server, client *http.Client, body string) *http.Response {
		t.Helper()
		res, err := client.Post(ts.URL+"/api/auth/password", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return res
	}

	t.Run("requires the current one", func(t *testing.T) {
		// A stolen cookie must not be enough to lock the owner out of their
		// own panel; that is the difference between an intruder who can read
		// your terminals and one who owns them.
		ts, _ := newTestServer(t)
		res := post(t, ts, ts.Client(), `{"current":"wrong wrong wrong","next":"`+next+`"}`)
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", res.StatusCode)
		}
	})

	t.Run("refuses a short one", func(t *testing.T) {
		ts, _ := newTestServer(t)
		res := post(t, ts, ts.Client(), `{"current":"`+current+`","next":"short"}`)
		defer res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", res.StatusCode)
		}
	})

	t.Run("changes it and signs other browsers out", func(t *testing.T) {
		ts, srv := newTestServer(t)

		// A second browser, signed in with the old password.
		other := anonymousClient(t)
		lres, err := other.Post(ts.URL+"/api/auth/login", "application/json",
			strings.NewReader(`{"username":"tester","password":"`+current+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		lres.Body.Close()
		if lres.StatusCode != http.StatusOK {
			t.Fatalf("the second browser could not sign in: %d", lres.StatusCode)
		}
		check, err := other.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		check.Body.Close()
		if check.StatusCode != http.StatusOK {
			t.Fatalf("the second browser was not authenticated to begin with: %d", check.StatusCode)
		}

		res := post(t, ts, ts.Client(), `{"current":"`+current+`","next":"`+next+`"}`)
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("status = %d, want 204: %s", res.StatusCode, body)
		}

		// The old password no longer works.
		fail := anonymousClient(t)
		fres, err := fail.Post(ts.URL+"/api/auth/login", "application/json",
			strings.NewReader(`{"username":"tester","password":"`+current+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		fres.Body.Close()
		if fres.StatusCode == http.StatusOK {
			t.Error("the old password still signs in")
		}

		// The other browser is out. The point of changing a password is that
		// whoever had the old one stops having access; leaving their session
		// alive makes the change decorative.
		after, err := other.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		after.Body.Close()
		if after.StatusCode != http.StatusUnauthorized {
			t.Errorf("the other browser is still signed in (%d)", after.StatusCode)
		}

		// This browser is not, because being logged out of the page you just
		// used to change your password reads as the change having failed.
		mine, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		mine.Body.Close()
		if mine.StatusCode != http.StatusOK {
			t.Errorf("the browser that changed the password was signed out (%d)", mine.StatusCode)
		}
		_ = srv
	})
}

// A storage fault must not sign anybody out.
//
// currentUser has three outcomes — signed in, not signed in, and "the database
// cannot say" — and the third exists because collapsing it into the second once
// told every viewer on a full disk to sign in, into the same broken database,
// until the login throttle locked them out of a panel that was only short of
// space. RequireAuth answers 503 for it. The two endpoints here are registered
// outside the authenticated group and do their own check, so they had to be
// fixed separately, and this is what pins them.
//
// The injection has to be precise, and the obvious one is wrong. Closing the
// database makes `CountUsers` at the top of handleAuthState fail too, so the
// endpoint answers 500 — which the client already treats as unreachable, so the
// bug does not appear and a test built that way passes either way. The fault
// that matters is the split one named in the finding: CountUsers succeeds and
// the session lookup does not. Dropping `auth_sessions` from a second
// connection produces exactly that, and leaves `users` intact.
func TestAStorageFaultDoesNotSignAnybodyOut(t *testing.T) {
	ts, srv := newTestServer(t)

	// Positive control. Without this a broken sign-in would look like the
	// behaviour under test, which is how a check ends up proving nothing.
	var before authState
	res, err := ts.Client().Get(ts.URL + "/api/auth/state")
	if err != nil {
		t.Fatalf("auth state: %v", err)
	}
	if err := json.NewDecoder(res.Body).Decode(&before); err != nil {
		t.Fatalf("decode: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !before.Authenticated {
		t.Fatalf("not signed in on a healthy panel, so this test proves nothing: %s %+v",
			res.Status, before)
	}

	raw, err := sql.Open("sqlite", srv.Cfg.DBPath())
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP TABLE auth_sessions`); err != nil {
		t.Fatalf("drop auth_sessions: %v", err)
	}

	// The dangerous answer is not an error of any kind — it is a cheerful 200
	// saying nobody is signed in, because that is the one reply App.tsx treats
	// as final: `if (!state.authenticated) onSignOut()`.
	res, err = ts.Client().Get(ts.URL + "/api/auth/state")
	if err != nil {
		t.Fatalf("auth state: %v", err)
	}
	var after authState
	_ = json.NewDecoder(res.Body).Decode(&after)
	res.Body.Close()
	if res.StatusCode == http.StatusOK && !after.Authenticated {
		t.Error("/api/auth/state answered 200 authenticated:false while the database " +
			"could not say — the client signs the user out on that, into a login " +
			"form that reads the same database")
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/api/auth/state answered %s for a storage fault, want 503", res.Status)
	}

	// Same collapse, second endpoint: 401 here is turned into a return to the
	// sign-in screen by the frontend's UnauthorizedError guard.
	res, err = ts.Client().Post(ts.URL+"/api/auth/password", "application/json",
		strings.NewReader(`{"current":"a sufficiently long password","next":"another sufficiently long one"}`))
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		t.Error("/api/auth/password answered 401 for a storage fault; the frontend " +
			"sends that to the sign-in screen, which reads the same database")
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/api/auth/password answered %s for a storage fault, want 503", res.Status)
	}
}
