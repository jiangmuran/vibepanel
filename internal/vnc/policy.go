// Package vnc connects a browser's RFB stream to a VNC server's TCP port.
//
// The panel is a proxy and never a client: it copies RFB bytes, and the only
// part of the protocol it understands is the handshake, which it terminates on
// both sides so that a stored VNC password never reaches the browser. See
// rfb.go for that, proxy.go for the copy, and this file for the one question
// that has to be answered before any of it runs -- which addresses this
// process is willing to open a TCP connection to.
package vnc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// dialTimeout bounds one connection attempt.
//
// A VNC server on a machine that is up but not listening answers immediately;
// one behind a firewall that drops rather than rejects never answers at all,
// and without this the browser's socket sits open with nothing on screen until
// somebody closes the tab. Eight seconds is long enough for a slow LAN and
// short enough that "it is not there" is an answer rather than a hang.
const dialTimeout = 8 * time.Second

// ErrRefused is returned when the policy will not reach an address. Callers
// distinguish it because it is the one failure that is not about the network
// and will not fix itself on a retry.
var ErrRefused = errors.New("vnc: address not reachable by policy")

// Policy decides which addresses the panel may open a TCP connection to.
//
// This is the SSRF boundary, and it is deliberately NOT shaped like
// --allow-from. There an empty list means "everything", and that is right:
// that flag narrows who may reach a panel which is useful without it. Here an
// empty list means loopback and nothing else, because what is being decided is
// where this process will send bytes on somebody else's behalf. "Nobody
// configured it" must not be the setting under which a signed-in browser can
// walk the network the panel sits on, one 400ms connect at a time, and read
// back whether each port spoke RFB.
//
// The list REPLACES the default rather than adding to it. An operator who
// writes --vnc-allow 192.168.1.0/24 and then cannot reach 127.0.0.1 is told
// so by name at the moment they save the target; an always-on loopback
// exception would be one more rule nobody can see from the flag, and it would
// mean the flag cannot express "this container may not talk to itself".
//
// What this does NOT stop, and it is worth being blunt: an operator who writes
// --vnc-allow 0.0.0.0/0 has asked for an authenticated SSRF primitive and has
// one. Even at the default, a signed-in user can point a target at any port on
// localhost and speak arbitrary bytes to it after the handshake -- RFB after
// ClientInit is a byte pipe and this is a proxy. Neither is an escalation
// *for that user*, who already has a writable terminal on this machine as the
// account that runs the panel. The reason to bound it anyway is that the
// bound is what keeps a future bug elsewhere -- a route registered above
// RequireAuth, a share link that grew a second route -- from being a scanner
// as well as a leak.
type Policy struct {
	// Allow is the set of networks that may be dialled. Empty means loopback.
	Allow []*net.IPNet

	// Lookup resolves a hostname. Nil means the process resolver.
	//
	// Injected so the tests can hand back an address the machine running them
	// does not have to own -- including the pair of records that makes the
	// "check all of them" rule below matter.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

// Permits reports whether one address is inside the policy.
func (p Policy) Permits(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// No normalisation of ::ffff:127.0.0.1 here, and its absence is the
	// interesting part: net.IPNet.Contains and net.IP.IsLoopback both call
	// To4() themselves, so the v4-mapped spelling is already the same address
	// to both of them. A normalising line was written here first and a
	// mutation run showed it changed nothing under any test — which is what
	// dead code looks like from the outside, and it read as the thing keeping
	// the mapped form from being a way around the list.
	//
	// What actually keeps that true is using those two functions rather than
	// comparing bytes by hand, and TestTheMappedSpellingIsNotAWayAround stands
	// over exactly that.
	if len(p.Allow) == 0 {
		return ip.IsLoopback()
	}
	for _, n := range p.Allow {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Describe names the policy for an error message, so a refusal says what would
// have had to be true instead of only that it was refused.
func (p Policy) Describe() string {
	if len(p.Allow) == 0 {
		return "loopback only (set --vnc-allow to widen it)"
	}
	out := make([]string, 0, len(p.Allow))
	for _, n := range p.Allow {
		out = append(out, n.String())
	}
	return "--vnc-allow " + strings.Join(out, ", ")
}

// Resolve turns a stored host into the addresses this panel is willing to
// dial, and refuses unless EVERY one of them is inside the policy.
//
// Every one, not the first that passes, and this is the rule the whole file
// exists for. A name with two A records -- one inside the allowed network and
// one outside -- would otherwise be a coin flip on each connection, and a name
// the panel does not control can be given a second record at any moment.
// Checking all of them makes the answer stable, and it makes DNS rebinding
// pointless in the other direction too: Dial connects to the literal this
// returned, so there is no second lookup between the check and the connect for
// a rebind to land in.
func (p Policy) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrRefused)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !p.Permits(ip) {
			return nil, fmt.Errorf("%w: %s is outside %s", ErrRefused, ip, p.Describe())
		}
		return []net.IP{ip}, nil
	}
	lookup := p.Lookup
	if lookup == nil {
		lookup = defaultLookup
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("vnc: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("vnc: %s resolves to nothing", host)
	}
	for _, ip := range ips {
		if !p.Permits(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s, which is outside %s",
				ErrRefused, host, ip, p.Describe())
		}
	}
	return ips, nil
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// ValidPort reports whether a stored port can be dialled at all.
//
// Checked where a target is written rather than only where it is used: port 0
// means "any port" to the kernel and "the user left the field empty" to
// everyone else, and a row carrying it is a row that fails at connect time
// with an error about the network.
func ValidPort(port int) bool { return port > 0 && port <= 65535 }

// Dial opens the connection, after Resolve has agreed to it.
func (p Policy) Dial(ctx context.Context, host string, port int) (net.Conn, error) {
	if !ValidPort(port) {
		return nil, fmt.Errorf("%w: port %d", ErrRefused, port)
	}
	ips, err := p.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	var last error
	for _, ip := range ips {
		// The address literal, never the name. Passing the hostname here would
		// hand the resolver a second chance to answer differently from the one
		// Resolve checked, which is the whole of DNS rebinding.
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, fmt.Errorf("vnc: dial %s: %w", net.JoinHostPort(host, strconv.Itoa(port)), last)
}
