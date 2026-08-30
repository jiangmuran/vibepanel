package httpapi

import (
	"net"
	"net/http"
	"net/url"
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

// publicOrigins is every origin a browser may legitimately be on.
//
// The panel cannot work this out for itself behind a reverse proxy. nginx
// terminates TLS and forwards over plaintext loopback, so the request arrives
// as `http://127.0.0.1:18443` while the browser is on
// `https://panel.example.com` -- and comparing those two literally makes every
// POST a cross-origin write. That is not hypothetical: it refused every write
// on a real deployment, with a bare 403 in the console and nothing saying why.
//
// So it is told, in three ways, in descending order of how much the operator
// had to do:
//
//   - the request's own origin, which is right whenever nothing is in front;
//   - the configured domain, on either scheme and with or without the listen
//     port, because somebody who set VIBEPANEL_DOMAIN has already said what
//     this panel is called;
//   - VIBEPANEL_PUBLIC_ORIGINS, for the case the first two do not cover: a
//     second name, a different public port, a path-based proxy.
//
// What this is *not* is a switch that turns the check off. Each entry is one
// more origin that may write, named by the person who runs the panel.
func (s *Server) publicOrigins(r *http.Request) []string {
	out := []string{s.requestOrigin(r)}

	if d := strings.ToLower(strings.TrimSpace(s.Cfg.Domain)); d != "" {
		// Both schemes and both port forms. A proxy in front may listen on 443
		// and forward to 18443, and the browser then says neither of the two
		// the panel would have guessed.
		for _, scheme := range []string{"https://", "http://"} {
			out = append(out, scheme+d)
			if port := portOf(s.Cfg.Addr); port != "" {
				out = append(out, scheme+d+":"+port)
			}
		}
	}
	for _, o := range s.Cfg.PublicOrigins {
		if o = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(o, "/"))); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// portOf pulls the port out of a listen address. ":18443" and "0.0.0.0:18443"
// both give "18443"; an address with no port gives "".
func portOf(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return addr[i+1:]
}

// crossOriginWrite reports whether this is a state-changing request whose
// Origin header says it came from a different site.
//
// The comparison is on **host and port, never on scheme**, and that is the
// whole of the fix for a reverse proxy. Both halves come from the same place:
// the browser fills in Host from the URL bar, and it fills in Origin from the
// URL bar. An attacker on another host sends their own host in Origin and ours
// in Host, so they differ, which is the case this exists to refuse. The only
// party that rewrites the *scheme* between those two is a proxy in front --
// nginx terminating TLS and forwarding over plaintext -- and comparing schemes
// therefore refuses the operator rather than the attacker.
//
// It shipped comparing whole origins for a day, and the failure was worse than
// the hole. Every write was refused behind nginx: 「无法创建底部终端」, and
// then the settings page could not be saved and the tour could not be marked
// as read, because those are writes too. A check that locks the operator out
// of the settings that would fix it is not a security control, it is a brick.
//
// The port stays in the comparison, because the port is what SameSite does not
// give us: `http://host:7681` is same-site with `https://host:18443` for
// cookie purposes, and that neighbouring service is the thing being kept out.
//
// `allowed` carries the extras for what host comparison cannot reach: a proxy
// that does not forward Host at all, or one that serves the panel under a
// second name. Those need saying out loud, and the 403 says which variable.
//
// A missing Origin is allowed. Browsers send it on every request that can
// change something; the requests without one are curl and the panel's own CLI,
// which authenticate with a bearer token and are not what a browser can be
// tricked into making.
func crossOriginWrite(r *http.Request, allowed []string) bool {
	if !unsafeMethod(r.Method) {
		return false
	}
	origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
	if origin == "" || origin == "null" {
		return false
	}
	from := hostOfOrigin(origin)
	if from == "" {
		// An Origin that is not a URL. Not something a browser sends.
		return true
	}
	if sameOrigin(from, r.Host) {
		return false
	}
	for _, a := range allowed {
		// Whole origins here, and only whole origins. An operator naming a
		// second origin has named a scheme with it, so being exact costs
		// nothing -- and matching on host here as well would silently
		// duplicate the rule above, which is how a guard ends up with two
		// implementations and no test that either of them works. Found by
		// deleting the check above and watching nothing go red.
		if sameOrigin(origin, a) {
			return false
		}
	}
	return true
}

// hostOfOrigin returns the `host[:port]` of a `scheme://host[:port]` value,
// lower-cased. Anything that is not that shape gives "".
func hostOfOrigin(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}
