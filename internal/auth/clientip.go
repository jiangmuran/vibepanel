package auth

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the address to throttle and audit against.
//
// X-Forwarded-For is believed only from a proxy the operator listed. Trusting
// it by default would let anyone bypass the login throttle by inventing a new
// address on every attempt, which is the whole reason the throttle exists.
func ClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	remote := hostOnly(r.RemoteAddr)
	if len(trustedProxies) == 0 {
		return remote
	}
	ip := net.ParseIP(remote)
	if ip == nil || !inAny(ip, trustedProxies) {
		return remote
	}
	// Rightmost entry that is not itself a trusted proxy: the ones further
	// left are supplied by the client and can say anything.
	forwarded := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate == nil {
			continue
		}
		if inAny(candidate, trustedProxies) {
			continue
		}
		return candidate.String()
	}
	return remote
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func inAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseCIDRs turns configuration strings into networks.
func ParseCIDRs(values []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, v := range values {
		_, n, err := net.ParseCIDR(strings.TrimSpace(v))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// Allowed reports whether an address passes an allowlist. An empty list allows
// everything, which is the default: the panel is usable without one.
func Allowed(remote string, allow []*net.IPNet) bool {
	if len(allow) == 0 {
		return true
	}
	ip := net.ParseIP(hostOnly(remote))
	if ip == nil {
		return false
	}
	return inAny(ip, allow)
}

// FromTrustedProxy reports whether the immediate peer is one the operator
// listed, and so whether its X-Forwarded-* headers mean anything.
//
// Separate from ClientIP because the scheme needs the same decision and got it
// wrong on its own the first time: an X-Forwarded-Proto believed from anybody
// lets a client choose which half of a binding it lands in.
func FromTrustedProxy(r *http.Request, trustedProxies []*net.IPNet) bool {
	if len(trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(hostOnly(r.RemoteAddr))
	return ip != nil && inAny(ip, trustedProxies)
}
