package httpapi

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/vnc"
)

// fakeDisplay listens on loopback and speaks just enough RFB to get through
// the handshake, then sends a marker.
//
// A real listener rather than a mock, for the reason the tmux wrapper is
// tested against a real tmux: what is being checked is that bytes cross
// between a TCP socket and a WebSocket in the right order, and a mock of the
// TCP side reproduces none of the ways that goes wrong.
func fakeDisplay(t *testing.T, password string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeDisplay(c, password)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func serveFakeDisplay(c net.Conn, password string) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("RFB 003.008\n")); err != nil {
		return
	}
	var version [12]byte
	if _, err := io.ReadFull(c, version[:]); err != nil {
		return
	}
	types := []byte{1, 1} // count, None
	if password != "" {
		types = []byte{1, 2} // count, VNC authentication
	}
	if _, err := c.Write(types); err != nil {
		return
	}
	var picked [1]byte
	if _, err := io.ReadFull(c, picked[:]); err != nil {
		return
	}
	if picked[0] == 2 {
		challenge := bytes.Repeat([]byte{0x42}, 16)
		if _, err := c.Write(challenge); err != nil {
			return
		}
		var answer [16]byte
		if _, err := io.ReadFull(c, answer[:]); err != nil {
			return
		}
	}
	if _, err := c.Write([]byte{0, 0, 0, 0}); err != nil {
		return
	}
	// Stands in for ServerInit. Recognisable on purpose: an off-by-four in the
	// handshake shows up as this arriving shifted rather than as an error.
	_, _ = c.Write([]byte("DESKTOP-IS-HERE"))
	_, _ = io.Copy(io.Discard, c)
}

// openDisplay does the browser half of the panel's own handshake and returns
// the first bytes of what the display sent.
func openDisplay(t *testing.T, ts *httptest.Server, targetID string) (*websocket.Conn, []byte, error) {
	t.Helper()
	url := strings.Replace(ts.URL, "http://", "ws://", 1) + "/api/vnc/targets/" + targetID + "/socket"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, wsDialOptions(t, ts))
	if err != nil {
		return nil, nil, err
	}
	conn := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var greeting [12]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return c, nil, err
	}
	if string(greeting[:]) != "RFB 003.008\n" {
		t.Fatalf("the panel greeted the browser with %q", greeting)
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return c, nil, err
	}
	var count [1]byte
	if _, err := io.ReadFull(conn, count[:]); err != nil {
		return c, nil, err
	}
	offered := make([]byte, count[0])
	if _, err := io.ReadFull(conn, offered); err != nil {
		return c, nil, err
	}
	if len(offered) != 1 || offered[0] != 1 {
		t.Fatalf("the panel offered the browser security types %v, want [1]", offered)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		return c, nil, err
	}
	var result [4]byte
	if _, err := io.ReadFull(conn, result[:]); err != nil {
		return c, nil, err
	}
	body := make([]byte, 15)
	if _, err := io.ReadFull(conn, body); err != nil {
		return c, nil, err
	}
	return c, body, nil
}

func makeTarget(t *testing.T, ts *httptest.Server, body string) store.VncTarget {
	t.Helper()
	return postJSON[store.VncTarget](t, ts, "/api/vnc/targets", body)
}

// The whole path: a stored row, a WebSocket, a TCP connection, and RFB bytes
// arriving in the browser with the panel having authenticated on its behalf.
func TestADisplayReachesTheBrowserWithThePasswordStayingHere(t *testing.T) {
	ts, _ := newTestServer(t)
	port := fakeDisplay(t, "hunter2")

	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":`+
		strconv.Itoa(port)+`,"viewOnly":false,"password":"hunter2"}`)
	if !target.HasPassword {
		t.Error("hasPassword is false on a display that was given one")
	}

	c, body, err := openDisplay(t, ts, target.ID)
	if err != nil {
		t.Fatalf("open the display: %v", err)
	}
	defer c.CloseNow()
	if string(body) != "DESKTOP-IS-HERE" {
		t.Errorf("the browser received %q; the stream is out of step", body)
	}
}

// The password is write-only. It goes in once and the panel never hands it
// back, so a browser with the settings page open is not a browser holding it.
func TestAStoredPasswordNeverComesBackOut(t *testing.T) {
	ts, _ := newTestServer(t)
	makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":5900,"password":"hunter2"}`)

	res, err := ts.Client().Get(ts.URL + "/api/vnc/targets")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the list handed the password back: %s", raw)
	}
	if strings.Contains(string(raw), `"password"`) {
		t.Errorf("the list declares a password field at all: %s", raw)
	}
	if !strings.Contains(string(raw), `"hasPassword":true`) {
		t.Errorf("the list does not say there is a password: %s", raw)
	}
}

// Editing a display's name must not wipe its password.
//
// The failure this exists for is silent in both directions: the field is never
// sent back, so nothing on screen changes, and the display simply stops
// connecting the next time somebody opens it.
func TestRenamingADisplayKeepsItsPassword(t *testing.T) {
	ts, srv := newTestServer(t)
	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":5900,"password":"hunter2"}`)

	patchVnc(t, ts, target.ID, `{"name":"other desk","host":"127.0.0.1","port":5900}`)

	row, err := srv.DB.GetVncTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetVncTarget: %v", err)
	}
	if row.Password != "hunter2" {
		t.Errorf("the stored password is now %q; a rename cleared it", row.Password)
	}
	if row.Name != "other desk" {
		t.Errorf("the rename did not take: %q", row.Name)
	}
}

func TestAnEmptyPasswordClearsIt(t *testing.T) {
	ts, srv := newTestServer(t)
	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":5900,"password":"hunter2"}`)
	patchVnc(t, ts, target.ID, `{"name":"desk","host":"127.0.0.1","port":5900,"password":""}`)

	row, err := srv.DB.GetVncTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetVncTarget: %v", err)
	}
	if row.Password != "" {
		t.Errorf("the password is still %q after being cleared", row.Password)
	}
}

func patchVnc(t *testing.T, ts *httptest.Server, id, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/vnc/targets/"+id, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("PATCH: %s: %s", res.Status, b)
	}
}

// An address the policy will never reach is refused where somebody can see it.
//
// The alternative is a row that saves cleanly and a display that shows an
// error forever, which is indistinguishable from a machine that happens to be
// switched off. Same reasoning as refusing a share link scoped to a project
// that does not exist.
func TestATargetOutsideThePolicyIsRefusedWhenItIsSaved(t *testing.T) {
	ts, _ := newTestServer(t) // the zero policy: loopback only
	res, err := ts.Client().Post(ts.URL+"/api/vnc/targets", "application/json",
		strings.NewReader(`{"name":"somewhere","host":"169.254.169.254","port":80}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, want 400: %s", res.StatusCode, b)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "vnc-allow") {
		t.Errorf("the refusal does not say what would change it: %s", body)
	}
}

// And the same check on the update path, which is the one that drifts.
func TestATargetCannotBeMovedOutsideThePolicyByAnEdit(t *testing.T) {
	ts, srv := newTestServer(t)
	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":5900}`)

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/vnc/targets/"+target.ID,
		strings.NewReader(`{"name":"desk","host":"169.254.169.254","port":80}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	row, err := srv.DB.GetVncTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetVncTarget: %v", err)
	}
	if row.Host != "127.0.0.1" {
		t.Errorf("the row now points at %q", row.Host)
	}
}

// The connect path checks again, because a row can have been written when the
// policy was wider, restored from a backup, or edited by hand -- and because a
// name resolves to whatever it resolves to today.
func TestAStoredRowIsCheckedAgainOnEveryConnect(t *testing.T) {
	ts, srv := newTestServer(t)
	port := fakeDisplay(t, "")
	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)

	// The row is good; narrow the policy under it, which is what a restored
	// backup or a changed flag looks like from here.
	srv.VNC = vnc.Policy{Allow: mustCIDRs(t, "192.168.0.0/16")}

	url := strings.Replace(ts.URL, "http://", "ws://", 1) + "/api/vnc/targets/" + target.ID + "/socket"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, wsDialOptions(t, ts))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	_, _, readErr := c.Read(ctx)
	if readErr == nil {
		t.Fatal("the socket delivered bytes for a display the policy refuses")
	}
	if got := websocket.CloseStatus(readErr); got != websocket.StatusPolicyViolation {
		t.Errorf("close status = %d, want %d (policy violation)", got, websocket.StatusPolicyViolation)
	}
}

// A display that cannot be reached closes differently from one that is
// refused, because the two are different problems: one is a machine to switch
// on, the other is a flag to change.
func TestAnUnreachableDisplayClosesAsABadGateway(t *testing.T) {
	ts, _ := newTestServer(t)
	// A loopback port with nothing on it. Bound and released so the number is
	// one nothing else on this machine has taken.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	target := makeTarget(t, ts, `{"name":"gone","host":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)
	url := strings.Replace(ts.URL, "http://", "ws://", 1) + "/api/vnc/targets/" + target.ID + "/socket"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, wsDialOptions(t, ts))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	_, _, readErr := c.Read(ctx)
	if got := websocket.CloseStatus(readErr); got != websocket.StatusBadGateway {
		t.Errorf("close status = %d, want %d (bad gateway); %v", got, websocket.StatusBadGateway, readErr)
	}
}

// The address is never in the request. The socket route takes an id, and an
// id that names no row is a 404 rather than anything anyone can aim.
func TestAnUnknownDisplayIdIsNotAnAddress(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/vnc/targets/nope/socket")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// No route anywhere takes a host, a port or a URL and connects to it. This is
// the property the whole design rests on, and the way it would be lost is a
// "just let me test a connection before saving it" endpoint -- which is
// exactly what the webhook test button is, and why that one sends an HTTP
// request rather than opening a socket to an arbitrary port.
func TestNoVncRouteTakesAnAddress(t *testing.T) {
	ts, _ := newTestServer(t)
	_ = ts
	for _, body := range []string{
		`{"host":"169.254.169.254","port":80}`,
		`{"name":"x","host":"169.254.169.254","port":80,"viewOnly":false}`,
	} {
		res, err := ts.Client().Post(ts.URL+"/api/vnc/targets", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("an address outside the policy was accepted: %s", body)
		}
	}
}

// An empty host is refused by name.
//
// The policy would refuse it anyway — Resolve("") is ErrRefused — so this
// guard exists entirely for what it says, and the assertion is therefore on
// the message. Without one, removing the guard changes nothing any test can
// see, and the person who left the field blank is told their address is
// outside a network policy they have never heard of.
func TestAnEmptyHostIsRefusedByName(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"name":"x","host":"","port":5900}`,
		`{"name":"x","host":"   ","port":5900}`,
	} {
		res, err := ts.Client().Post(ts.URL+"/api/vnc/targets", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s gave status %d, want 400", body, res.StatusCode)
		}
		if !strings.Contains(string(raw), "needs a host") {
			t.Errorf("%s was refused with %s, which does not say the field is empty", body, raw)
		}
	}
}

func TestAPortOutsideTheRangeIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"name":"x","host":"127.0.0.1","port":0}`,
		`{"name":"x","host":"127.0.0.1","port":70000}`,
	} {
		res, err := ts.Client().Post(ts.URL+"/api/vnc/targets", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s gave status %d, want 400", body, res.StatusCode)
		}
	}
}

// Every row is an address this process will dial when asked, so the list is a
// multiplier on how much a panel can be made to connect to.
func TestTheDisplayListIsBounded(t *testing.T) {
	ts, _ := newTestServer(t)
	for i := 0; i < store.MaxVncTargets; i++ {
		makeTarget(t, ts, `{"name":"d","host":"127.0.0.1","port":`+strconv.Itoa(5900+i)+`}`)
	}
	res, err := ts.Client().Post(ts.URL+"/api/vnc/targets", "application/json",
		strings.NewReader(`{"name":"one too many","host":"127.0.0.1","port":6000}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", res.StatusCode)
	}
}

func TestAddingAndRemovingADisplayIsRecorded(t *testing.T) {
	ts, srv := newTestServer(t)
	target := makeTarget(t, ts, `{"name":"desk","host":"127.0.0.1","port":5900}`)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/vnc/targets/"+target.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()

	entries, err := srv.DB.RecentAudit(context.Background(), 50)
	if err != nil {
		t.Fatalf("RecentAudit: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Event] = true
	}
	for _, want := range []string{"vnc.added", "vnc.removed"} {
		if !seen[want] {
			t.Errorf("%s is not in the audit log; a door onto another machine was opened "+
				"and closed unrecorded", want)
		}
	}
}

func mustCIDRs(t *testing.T, values ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, v := range values {
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", v, err)
		}
		out = append(out, n)
	}
	return out
}
