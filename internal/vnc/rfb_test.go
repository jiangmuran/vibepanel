package vnc

import (
	"bytes"
	"crypto/des" //nolint:gosec // comparing against the unreversed key on purpose; see the test.
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer runs one side of a pipe as a VNC server, following a script.
//
// A real conversation rather than a recorded byte string: the failures worth
// catching here are off-by-one reads, and a script that replies to what it was
// sent is the only thing that shows one.
type fakeServer struct {
	version  string // what the server greets with
	types    []byte // 3.7+ security types; nil for a 3.3 server
	type33   uint32 // 3.3 security type
	password string // what the server will accept, "" for none
	// afterHandshake is written once the security phase is over. The tests use
	// a recognisable ServerInit stand-in to prove the stream is not one message
	// out of step.
	afterHandshake []byte

	err error
}

func (f *fakeServer) run(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	write := func(b []byte) bool {
		_, err := c.Write(b)
		if err != nil {
			f.err = err
		}
		return err == nil
	}
	if !write([]byte(f.version)) {
		return
	}
	var reply [12]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		f.err = err
		return
	}
	minor, err := parseVersion(reply[:])
	if err != nil {
		f.err = err
		return
	}

	chosen := int(f.type33)
	if minor >= 7 {
		if !write(append([]byte{byte(len(f.types))}, f.types...)) {
			return
		}
		var picked [1]byte
		if _, err := io.ReadFull(c, picked[:]); err != nil {
			f.err = err
			return
		}
		chosen = int(picked[0])
	} else {
		if !write([]byte{byte(f.type33 >> 24), byte(f.type33 >> 16), byte(f.type33 >> 8), byte(f.type33)}) {
			return
		}
	}

	if chosen == secVNCAuth {
		challenge := bytes.Repeat([]byte{0xa5}, 16)
		if !write(challenge) {
			return
		}
		var answer [16]byte
		if _, err := io.ReadFull(c, answer[:]); err != nil {
			f.err = err
			return
		}
		want, _ := vncResponse(f.password, challenge)
		if !bytes.Equal(answer[:], want) {
			f.err = errors.New("the proxy sent the wrong challenge response")
			if !write([]byte{0, 0, 0, 1}) {
				return
			}
			return
		}
	}
	if minor >= 8 || chosen == secVNCAuth {
		if !write([]byte{0, 0, 0, 0}) {
			return
		}
	}
	write(f.afterHandshake)
}

// browserSide plays the panel's own client: version, security choice, result.
// Returns everything it was sent after the SecurityResult.
func browserSide(t *testing.T, c net.Conn) []byte {
	t.Helper()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var greeting [12]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil {
		return nil
	}
	if _, err := c.Write([]byte("RFB 003.008\n")); err != nil {
		return nil
	}
	var count [1]byte
	if _, err := io.ReadFull(c, count[:]); err != nil {
		return nil
	}
	offered := make([]byte, count[0])
	if _, err := io.ReadFull(c, offered); err != nil {
		return nil
	}
	if _, err := c.Write([]byte{offered[0]}); err != nil {
		return nil
	}
	var result [4]byte
	if _, err := io.ReadFull(c, result[:]); err != nil {
		return nil
	}
	rest, _ := io.ReadAll(c)
	return append(append(greeting[:], offered...), rest...)
}

// runHandshake wires a fake server and a fake browser to the real Handshake.
func runHandshake(t *testing.T, f *fakeServer, password string) (error, []byte, net.Conn) {
	t.Helper()
	serverA, serverB := net.Pipe()
	browserA, browserB := net.Pipe()
	go f.run(serverB)
	seen := make(chan []byte, 1)
	go func() { seen <- browserSide(t, browserB) }()

	err := Handshake(serverA, browserA, password)
	browserA.Close()
	return err, <-seen, serverA
}

// The browser is offered security type None whatever the server wanted, and
// the challenge and the password never cross to it.
//
// This is the whole reason the handshake is terminated here rather than piped
// through. Remove the substitution and the browser performs the
// authentication, which means the password is in a JavaScript heap.
func TestTheBrowserNeverSeesTheChallengeOrThePassword(t *testing.T) {
	f := &fakeServer{
		version:        "RFB 003.008\n",
		types:          []byte{secVNCAuth},
		password:       "hunter2",
		afterHandshake: []byte("SERVERINIT"),
	}
	err, sawBytes, upstream := runHandshake(t, f, "hunter2")
	if err != nil {
		t.Fatalf("Handshake: %v (server: %v)", err, f.err)
	}
	if f.err != nil {
		t.Fatalf("the server rejected the proxy: %v", f.err)
	}
	saw := string(sawBytes)
	if !strings.HasPrefix(saw, "RFB 003.008\n") {
		t.Errorf("the browser was greeted with %q, want RFB 003.008", saw[:12])
	}
	if strings.Contains(saw, "hunter2") {
		t.Error("the password was written to the browser")
	}
	if bytes.Contains(sawBytes, bytes.Repeat([]byte{0xa5}, 16)) {
		t.Error("the server's challenge was forwarded to the browser")
	}
	if !bytes.Contains(sawBytes, []byte{secNone}) {
		t.Error("the browser was not offered security type None")
	}
	// And the upstream stream is positioned exactly at ServerInit, not one
	// message early or late.
	var init [10]byte
	_ = upstream.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(upstream, init[:]); err != nil {
		t.Fatalf("read past the handshake: %v", err)
	}
	if string(init[:]) != "SERVERINIT" {
		t.Errorf("the byte after the handshake is %q; the stream is out of step", init)
	}
	upstream.Close()
}

// RFB 3.3 with security type None sends no SecurityResult.
//
// Reading one anyway eats the first four bytes of ServerInit, and every frame
// after that is nonsense — with no error anywhere, which is the worst shape a
// protocol bug can have.
func TestAThirtyThreeServerWithNoAuthSendsNoSecurityResult(t *testing.T) {
	f := &fakeServer{
		version:        "RFB 003.003\n",
		type33:         secNone,
		afterHandshake: []byte("SERVERINIT"),
	}
	err, _, upstream := runHandshake(t, f, "")
	if err != nil {
		t.Fatalf("Handshake: %v (server: %v)", err, f.err)
	}
	var init [10]byte
	_ = upstream.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(upstream, init[:]); err != nil {
		t.Fatalf("read past the handshake: %v", err)
	}
	if string(init[:]) != "SERVERINIT" {
		t.Errorf("the byte after the handshake is %q; a SecurityResult was read that "+
			"a 3.3 server never sent", init)
	}
	upstream.Close()
}

// A 3.3 server that does want a password still sends a SecurityResult.
func TestAThirtyThreeServerWithAPasswordStillSendsASecurityResult(t *testing.T) {
	f := &fakeServer{
		version:        "RFB 003.003\n",
		type33:         secVNCAuth,
		password:       "sekret",
		afterHandshake: []byte("SERVERINIT"),
	}
	err, _, upstream := runHandshake(t, f, "sekret")
	if err != nil {
		t.Fatalf("Handshake: %v (server: %v)", err, f.err)
	}
	var init [10]byte
	_ = upstream.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(upstream, init[:]); err != nil {
		t.Fatalf("read past the handshake: %v", err)
	}
	if string(init[:]) != "SERVERINIT" {
		t.Errorf("the byte after the handshake is %q; the stream is out of step", init)
	}
	upstream.Close()
}

func TestASecurityTypeThisProxyCannotSpeakIsRefusedByName(t *testing.T) {
	f := &fakeServer{version: "RFB 003.008\n", types: []byte{19}} // VeNCrypt
	err, _, upstream := runHandshake(t, f, "")
	upstream.Close()
	if err == nil {
		t.Fatal("a VeNCrypt-only server was accepted")
	}
	if !strings.Contains(err.Error(), "19") {
		t.Errorf("the refusal does not name what was offered: %v", err)
	}
}

// A target pointed at a port that is not VNC is the common mistake, and the
// answer has to be "that is not VNC" rather than a stall.
func TestAPortThatIsNotVncIsRefusedImmediately(t *testing.T) {
	serverA, serverB := net.Pipe()
	browserA, browserB := net.Pipe()
	go func() {
		defer serverB.Close()
		_, _ = serverB.Write([]byte("HTTP/1.1 20"))
		_, _ = serverB.Write([]byte("0"))
	}()
	go func() { _, _ = io.Copy(io.Discard, browserB) }()

	err := Handshake(serverA, browserA, "")
	serverA.Close()
	browserA.Close()
	browserB.Close()
	if err == nil {
		t.Fatal("an HTTP server was accepted as a VNC server")
	}
	if !strings.Contains(err.Error(), "not an RFB server") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
}

// The greeting is checked shape-first, not "twelve bytes with digits in them".
//
// The end-to-end test above cannot see the difference: an HTTP greeting fails
// the digit parse as well, so a parser that only counts bytes still refuses it
// and still says the right thing. What a lenient parser lets through is a
// twelve-byte greeting that happens to parse — and then the proxy negotiates a
// version with something that is not speaking RFB, which is a stall rather
// than an error.
func TestTheGreetingIsCheckedByShapeAndNotByLength(t *testing.T) {
	for _, greeting := range []string{
		"RFB 003.008X",  // twelve bytes, digits in the right place, no terminator
		"RFB 004.008\n", // a major version this proxy does not speak
		"rfb 003.008\n", // the wrong case is a different protocol, not a typo to be kind about
		"HTTP/1.1 200",
	} {
		if _, err := parseVersion([]byte(greeting)); err == nil {
			t.Errorf("parseVersion(%q) accepted it", greeting)
		}
	}
	for _, greeting := range []string{"RFB 003.003\n", "RFB 003.007\n", "RFB 003.008\n", "RFB 003.889\n"} {
		if _, err := parseVersion([]byte(greeting)); err != nil {
			t.Errorf("parseVersion(%q) refused a real greeting: %v", greeting, err)
		}
	}
}

func TestAStoredPasswordWinsOverNone(t *testing.T) {
	// Both offered. The server would take either, and somebody typed the
	// password on purpose.
	if got := pickSecurity([]byte{secNone, secVNCAuth}, "sekret"); got != secVNCAuth {
		t.Errorf("pickSecurity with a stored password chose %d, want %d", got, secVNCAuth)
	}
	if got := pickSecurity([]byte{secNone, secVNCAuth}, ""); got != secNone {
		t.Errorf("pickSecurity with no password chose %d, want %d", got, secNone)
	}
	if got := pickSecurity([]byte{19, 18}, "sekret"); got != 0 {
		t.Errorf("pickSecurity chose %d out of types it cannot speak", got)
	}
}

func TestAPasswordlessRowAgainstAPasswordedServerSaysSo(t *testing.T) {
	f := &fakeServer{version: "RFB 003.008\n", types: []byte{secVNCAuth}, password: "x"}
	err, _, upstream := runHandshake(t, f, "")
	upstream.Close()
	if err == nil {
		t.Fatal("a server demanding a password was connected to without one")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("the error does not mention the password: %v", err)
	}
}

// The DES key is the password with the bits of every byte reversed. That
// reversal is VNC's own quirk and is the single line most likely to be
// "simplified" away, at which point every password-protected display refuses
// the panel with an error about the password being wrong.
func TestTheKeyBitsAreReversed(t *testing.T) {
	challenge := bytes.Repeat([]byte{0x5a}, 16)
	got, err := vncResponse("password", challenge)
	if err != nil {
		t.Fatalf("vncResponse: %v", err)
	}
	plain, err := des.NewCipher([]byte("password"))
	if err != nil {
		t.Fatalf("des: %v", err)
	}
	naive := make([]byte, 8)
	plain.Encrypt(naive, challenge[:8])
	if bytes.Equal(got[:8], naive) {
		t.Error("the response is plain DES with the password as the key; the VNC bit " +
			"reversal is not being applied")
	}
}

// ECB over both halves with one key, which is what the protocol says. A test
// rather than a comment because "encrypt the second half with the first as an
// IV" is what somebody writes when they fix this file for looking wrong.
func TestBothHalvesAreEncryptedIndependently(t *testing.T) {
	challenge := append(bytes.Repeat([]byte{0x11}, 8), bytes.Repeat([]byte{0x11}, 8)...)
	got, err := vncResponse("pw", challenge)
	if err != nil {
		t.Fatalf("vncResponse: %v", err)
	}
	if !bytes.Equal(got[:8], got[8:]) {
		t.Error("identical halves of the challenge produced different halves of the response; " +
			"this is ECB by definition and something is chaining")
	}
}

// A VNC server truncates the password at eight bytes, so the proxy has to as
// well or it disagrees with every server about a long one.
func TestAPasswordIsTruncatedAtEightBytes(t *testing.T) {
	challenge := bytes.Repeat([]byte{0x33}, 16)
	long, err := vncResponse("abcdefghIGNORED", challenge)
	if err != nil {
		t.Fatalf("vncResponse: %v", err)
	}
	short, err := vncResponse("abcdefgh", challenge)
	if err != nil {
		t.Fatalf("vncResponse: %v", err)
	}
	if !bytes.Equal(long, short) {
		t.Error("a password longer than eight bytes produced a different response; " +
			"the server will have truncated it and disagreed")
	}
}

// A browser answering with a version this proxy does not speak to it is a
// refusal rather than a desynchronised stream.
func TestABrowserOnAnOlderDialectIsRefused(t *testing.T) {
	a, b := net.Pipe()
	// offerToBrowser is called directly here, so it has none of the deadline
	// Handshake sets around it. Without one, a version check that accepts 3.3
	// goes on to read a security choice from a browser that will never send
	// one, and this test hangs until the whole package's ten-minute timeout
	// instead of failing.
	_ = a.SetDeadline(time.Now().Add(2 * time.Second))
	go func() {
		defer b.Close()
		var greeting [12]byte
		_, _ = io.ReadFull(b, greeting[:])
		_, _ = b.Write([]byte("RFB 003.003\n"))
		_, _ = io.Copy(io.Discard, b)
	}()
	err := offerToBrowser(a)
	a.Close()
	if err == nil {
		t.Fatal("a 3.3 browser was accepted by a proxy that speaks 3.8 to it")
	}
	if !strings.Contains(err.Error(), "003.003") {
		t.Errorf("the refusal does not name the version: %v", err)
	}
}

// A refusal reason is a length-prefixed string from a host the panel was told
// to dial, so the length is a number chosen by somebody else.
//
// The assertion is on the size of the buffer that gets allocated, not on the
// length of the string that comes back. Those are different things and only
// the first one is the problem: a server claiming four gigabytes and then
// hanging up produces a short string either way, because io.ReadFull fails —
// after the four gigabytes have been reserved. The first version of this test
// checked the string and passed with the bound removed.
func TestARefusalReasonIsBounded(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff}) // four gigabytes, allegedly
	buf.Write(bytes.Repeat([]byte("A"), 100))
	spy := &readSizeSpy{r: &buf}

	got := readReason(spy)
	if spy.largest > maxReasonBytes {
		t.Errorf("readReason asked for %d bytes in one read; a server it was told to dial "+
			"chose that number", spy.largest)
	}
	if len(got) > 120 {
		t.Errorf("readReason returned %d bytes", len(got))
	}
}

// readSizeSpy records the largest buffer it is asked to fill. io.ReadFull
// passes the whole destination to Read, so this sees the allocation.
type readSizeSpy struct {
	r       io.Reader
	largest int
}

func (s *readSizeSpy) Read(p []byte) (int, error) {
	if len(p) > s.largest {
		s.largest = len(p)
	}
	return s.r.Read(p)
}

// Control bytes out of a socket end up in a log line and in a WebSocket close
// frame; neither is a place to paste a terminal escape sequence.
func TestAReasonIsMadePrintable(t *testing.T) {
	got := printable([]byte{0x1b, '[', '2', 'J', 'x'})
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("printable(%q) kept an escape byte", got)
	}
}
