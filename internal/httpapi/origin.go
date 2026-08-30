package httpapi

import (
	"net"
	"net/http"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/auth"
)

// requestOrigin is the `scheme://host:port` the browser thinks it is talking
// to, which is what a sign-in is bound to.
//
// It has to be built from what the *browser* sent rather than from the panel's
// own configuration, because the two disagree in every case this exists for: a
// panel listening on one port is reachable under several names, and the whole
// question is which of them a given cookie came from.
//
// Host is safe to use here. A page on another port cannot change the Host
// header of a request the browser makes to this one -- the browser fills it in
// from the URL -- so in the confused-deputy case this defends against, Host is
// the panel's own name and the *Origin* header is the attacker's. A client
// that is not a browser can send whatever it likes, but a client that is not a
// browser and already holds the cookie has won without this.
func (s *Server) requestOrigin(r *http.Request) string {
	return requestScheme(r, s.trustedProxies()) + "://" + strings.ToLower(r.Host)
}

func (s *Server) trustedProxies() []*net.IPNet {
	if s.Auth == nil {
		return nil
	}
	return s.Auth.TrustedProxies
}

// requestScheme believes X-Forwarded-Proto only from a listed proxy, for the
// same reason ClientIP believes X-Forwarded-For only from one: a header
// anybody may set is a choice of which half of a binding to land in.
func requestScheme(r *http.Request, trusted []*net.IPNet) string {
	if auth.FromTrustedProxy(r, trusted) {
		if p := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))); p == "http" || p == "https" {
			return p
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// sameOrigin compares two origins for the purposes of the binding.
//
// Case-insensitive because a browser sends the Host exactly as it was typed,
// and a sign-in that stops working over the shift key is a bug report nobody
// enjoys writing up.
func sameOrigin(a, b string) bool { return strings.EqualFold(a, b) }

// unsafeMethod reports whether a request can change anything.
//
// The safe ones are the list from RFC 9110 that browsers will also issue as
// plain navigations; everything else needs the Origin header to agree.
func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// crossOriginWrite reports whether this is a state-changing request whose
// Origin header says it came from somewhere else.
//
// This is the half of the defence that SameSite=Strict does not cover, and
// which the panel needed all along. For cookies, "site" is the registrable
// domain: a different port and a different scheme on the same host are the
// *same* site, so a page served by anything else on this machine can POST here
// and the browser attaches the session cookie. On a host that is also running
// a ttyd, that is a shell.
//
// A missing Origin is allowed. Browsers send it on every request that can
// change something; the requests without one are `curl` and the panel's own
// CLI, which authenticate with a bearer token and are not what a browser can
// be tricked into making.
func crossOriginWrite(r *http.Request, self string) bool {
	if !unsafeMethod(r.Method) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return false
	}
	return !sameOrigin(origin, self)
}
