package vnc

import (
	"crypto/des" //nolint:gosec // VNC's challenge is DES-ECB by definition; see vncResponse.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"strconv"
	"time"
)

// The two security types this proxy speaks upstream.
//
// Not VeNCrypt, not TLS, not RA2 -- see Handshake for what a server offering
// only those is told. Adding one means adding a way for the panel to hold
// another kind of credential, which is a decision, not a compatibility fix.
const (
	secNone    = 1
	secVNCAuth = 2
)

// HandshakeTimeout bounds the whole negotiation on both sides.
//
// A TCP port that accepts a connection and then says nothing is the shape of a
// firewall, of a service that is not VNC at all, and of a VNC server that is
// still starting X. Without a deadline the browser's socket stays open and the
// tab shows a blank rectangle indefinitely, which is the failure this panel's
// whole status vocabulary exists to avoid.
const HandshakeTimeout = 15 * time.Second

// deadliner is what the two ends have in common: both are net.Conn in
// production, and both are pipes in the tests.
type deadliner interface {
	io.ReadWriter
	SetDeadline(time.Time) error
}

// Handshake negotiates RFB with the server and with the browser, and leaves
// both connections positioned at ClientInit.
//
// This is the part of the protocol the panel understands, and terminating it
// on both sides is the whole reason the panel can hold a VNC password at all.
// The alternative -- a straight byte pipe from the first byte -- means the
// browser performs the authentication, which means the browser is sent the
// password, which means the password is in a JavaScript heap, in a fetch
// response, and in whatever the browser's developer tools have recorded. Eight
// bytes of DES are weak enough already without also being handed out.
//
// So: upstream sees a real RFB client that authenticates. The browser sees a
// server speaking RFB 3.8 offering security type None, whatever the real
// server speaks. That substitution is free because from ClientInit onwards
// 3.3, 3.7 and 3.8 are the same protocol -- which is also why everything after
// this function is a copy and not a parser.
func Handshake(upstream, browser deadliner, password string) error {
	deadline := time.Now().Add(HandshakeTimeout)
	_ = upstream.SetDeadline(deadline)
	_ = browser.SetDeadline(deadline)
	defer func() {
		// Cleared, not extended. A deadline left on the connection would stop
		// the proxy that runs after this: a desktop nobody is touching sends
		// nothing for minutes, and that is not a timeout, it is a desktop.
		_ = upstream.SetDeadline(time.Time{})
		_ = browser.SetDeadline(time.Time{})
	}()

	minor, err := negotiateVersion(upstream)
	if err != nil {
		return err
	}
	if err := authenticate(upstream, minor, password); err != nil {
		return err
	}
	return offerToBrowser(browser)
}

// negotiateVersion reads the server's ProtocolVersion and answers with the
// highest both ends speak.
func negotiateVersion(upstream deadliner) (int, error) {
	var greeting [12]byte
	if _, err := io.ReadFull(upstream, greeting[:]); err != nil {
		return 0, fmt.Errorf("vnc: no RFB greeting: %w", err)
	}
	minor, err := parseVersion(greeting[:])
	if err != nil {
		return 0, err
	}
	// 3.3, 3.7 and 3.8 are the three that exist. Anything higher is answered
	// with 3.8 because that is what the spec says a client does, and Apple's
	// 003.889 is the reason the case is not theoretical.
	chosen := 8
	switch {
	case minor < 7:
		chosen = 3
	case minor < 8:
		chosen = 7
	}
	if _, err := upstream.Write([]byte(fmt.Sprintf("RFB 003.%03d\n", chosen))); err != nil {
		return 0, fmt.Errorf("vnc: send version: %w", err)
	}
	return chosen, nil
}

// parseVersion reads the minor number out of an RFB greeting.
//
// Strict about the shape rather than lenient, because the interesting case is
// not a slightly odd VNC server: it is a target pointed at a port that is not
// VNC at all. An HTTP server answers a connection with its own greeting, and a
// lenient parser turns "you have the wrong port" into a stall further down.
func parseVersion(b []byte) (int, error) {
	if len(b) != 12 || string(b[:8]) != "RFB 003." || b[11] != '\n' {
		return 0, fmt.Errorf("vnc: not an RFB server (greeting %q)", printable(b))
	}
	minor, err := strconv.Atoi(string(b[8:11]))
	if err != nil {
		return 0, fmt.Errorf("vnc: not an RFB server (greeting %q)", printable(b))
	}
	return minor, nil
}

// authenticate runs the security handshake against the server.
func authenticate(upstream deadliner, minor int, password string) error {
	chosen, err := chooseSecurity(upstream, minor, password)
	if err != nil {
		return err
	}
	if chosen == secVNCAuth {
		if password == "" {
			return errors.New("vnc: the server wants a password and none is stored for this display")
		}
		var challenge [16]byte
		if _, err := io.ReadFull(upstream, challenge[:]); err != nil {
			return fmt.Errorf("vnc: read challenge: %w", err)
		}
		answer, err := vncResponse(password, challenge[:])
		if err != nil {
			return err
		}
		if _, err := upstream.Write(answer); err != nil {
			return fmt.Errorf("vnc: send challenge response: %w", err)
		}
	}
	// SecurityResult is sent for every type from 3.8 on. Before that it was
	// sent only when there was something to authenticate, so reading it
	// unconditionally against a 3.3 server offering None consumes the first
	// four bytes of ServerInit and every frame afterwards is nonsense.
	if minor >= 8 || chosen == secVNCAuth {
		var result [4]byte
		if _, err := io.ReadFull(upstream, result[:]); err != nil {
			return fmt.Errorf("vnc: read security result: %w", err)
		}
		if binary.BigEndian.Uint32(result[:]) != 0 {
			if minor >= 8 {
				return fmt.Errorf("vnc: the server refused: %s", readReason(upstream))
			}
			return errors.New("vnc: the server refused the password")
		}
	}
	return nil
}

// chooseSecurity performs the type negotiation and returns what was agreed.
func chooseSecurity(upstream deadliner, minor int, password string) (int, error) {
	if minor < 7 {
		// 3.3: the server decides, and the client's only move is to accept it
		// or hang up.
		var raw [4]byte
		if _, err := io.ReadFull(upstream, raw[:]); err != nil {
			return 0, fmt.Errorf("vnc: read security type: %w", err)
		}
		chosen := int(binary.BigEndian.Uint32(raw[:]))
		if chosen == 0 {
			return 0, fmt.Errorf("vnc: the server refused: %s", readReason(upstream))
		}
		if chosen != secNone && chosen != secVNCAuth {
			return 0, fmt.Errorf("vnc: the server requires security type %d, which this proxy does not speak", chosen)
		}
		return chosen, nil
	}

	var count [1]byte
	if _, err := io.ReadFull(upstream, count[:]); err != nil {
		return 0, fmt.Errorf("vnc: read security types: %w", err)
	}
	if count[0] == 0 {
		return 0, fmt.Errorf("vnc: the server refused: %s", readReason(upstream))
	}
	offered := make([]byte, count[0])
	if _, err := io.ReadFull(upstream, offered); err != nil {
		return 0, fmt.Errorf("vnc: read security types: %w", err)
	}
	chosen := pickSecurity(offered, password)
	if chosen == 0 {
		return 0, fmt.Errorf("vnc: the server offers security types %v; this proxy speaks None and VNC password",
			offered)
	}
	if _, err := upstream.Write([]byte{byte(chosen)}); err != nil {
		return 0, fmt.Errorf("vnc: send security type: %w", err)
	}
	return chosen, nil
}

// pickSecurity chooses among what the server offered, or 0 for none of them.
//
// A stored password wins over None when both are offered. The server would
// accept either, but somebody typed that password into this panel on purpose,
// and silently connecting unauthenticated to a display they believed was
// password-protected is the kind of thing they would find out about later.
func pickSecurity(offered []byte, password string) int {
	has := func(t byte) bool {
		for _, o := range offered {
			if o == t {
				return true
			}
		}
		return false
	}
	if password != "" && has(secVNCAuth) {
		return secVNCAuth
	}
	if has(secNone) {
		return secNone
	}
	if has(secVNCAuth) {
		return secVNCAuth
	}
	return 0
}

// offerToBrowser plays the server side to the panel's own client.
//
// RFB 3.8, security None, always. The browser is not being trusted with less
// here than it would otherwise have: it reached this socket through
// RequireAuth, and by the time these bytes are written the panel has already
// authenticated to the display on its behalf.
//
// The version is required to come back as 003.008 rather than merely being
// read. This is not a public endpoint that has to interoperate -- the only
// thing on the other end is the noVNC build shipped inside this binary -- and
// the two older dialects differ in exactly the byte sequence being written
// below. Accepting a 3.7 answer and then sending it a SecurityResult it is not
// expecting is a desynchronised stream and a blank rectangle, which is much
// harder to read than a refusal that says the version.
func offerToBrowser(browser deadliner) error {
	if _, err := browser.Write([]byte("RFB 003.008\n")); err != nil {
		return fmt.Errorf("vnc: greet the browser: %w", err)
	}
	var version [12]byte
	if _, err := io.ReadFull(browser, version[:]); err != nil {
		return fmt.Errorf("vnc: the browser sent no version: %w", err)
	}
	minor, err := parseVersion(version[:])
	if err != nil {
		return err
	}
	if minor != 8 {
		return fmt.Errorf("vnc: the browser answered RFB 003.%03d; this proxy speaks 003.008 to it", minor)
	}
	if _, err := browser.Write([]byte{1, secNone}); err != nil {
		return fmt.Errorf("vnc: offer security to the browser: %w", err)
	}
	var picked [1]byte
	if _, err := io.ReadFull(browser, picked[:]); err != nil {
		return fmt.Errorf("vnc: the browser chose no security type: %w", err)
	}
	if picked[0] != secNone {
		return fmt.Errorf("vnc: the browser chose security type %d, which was not offered", picked[0])
	}
	if _, err := browser.Write([]byte{0, 0, 0, 0}); err != nil {
		return fmt.Errorf("vnc: send security result: %w", err)
	}
	return nil
}

// maxReasonBytes bounds the failure string a server sends with a refusal.
//
// It is a length-prefixed u32 from a host the panel was told to dial, so the
// prefix is a number an unfriendly server chooses. Four kilobytes is far more
// than any reason and small enough that being lied to costs nothing.
const maxReasonBytes = 4 << 10

// readReason reads the human-readable failure a server sends after a refusal.
//
// Never returns an error: it is called on a path that already has one, and the
// only question left is whether there is anything to add to it.
func readReason(r io.Reader) string {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return "no reason given"
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 {
		return "no reason given"
	}
	if n > maxReasonBytes {
		n = maxReasonBytes
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "no reason given"
	}
	return printable(buf)
}

// printable makes bytes from a socket safe to put in an error that ends up in
// a log line and in a WebSocket close reason.
func printable(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			c = '.'
		}
		out = append(out, rune(c))
		if len(out) >= 120 {
			break
		}
	}
	return string(out)
}

// vncResponse is the challenge response for security type 2.
//
// Single-DES in ECB over both halves of the challenge, with the password
// truncated or zero-padded to eight bytes and the bits of every byte reversed.
// The bit reversal is VNC's, not DES's, and it is why crypto/des cannot be
// handed the password directly.
//
// ECB and 56 bits are the protocol, not an oversight here: this is what every
// VNC server on the far end will verify against, and there is no version of
// this function that is stronger while still working. It is the reason the
// panel says on screen that a VNC password is not a secret-keeping mechanism.
func vncResponse(password string, challenge []byte) ([]byte, error) {
	var key [8]byte
	copy(key[:], password) // truncates past 8 bytes, which is what a VNC server does too
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("vnc: des: %w", err)
	}
	out := make([]byte, 16)
	block.Encrypt(out[0:8], challenge[0:8])
	block.Encrypt(out[8:16], challenge[8:16])
	return out, nil
}
