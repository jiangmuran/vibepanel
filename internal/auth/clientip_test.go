package auth

import (
	"net/http"
	"testing"
)

func req(remote, forwarded string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return r
}

func TestClientIPIgnoresForwardedByDefault(t *testing.T) {
	// Believing this header without being told to would let anyone bypass the
	// login throttle by inventing a new address on every attempt.
	got := ClientIP(req("203.0.113.9:5555", "1.1.1.1"), nil)
	if got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the socket address", got)
	}
}

func TestClientIPUsesForwardedFromATrustedProxy(t *testing.T) {
	proxies, err := ParseCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	got := ClientIP(req("10.1.2.3:5555", "198.51.100.7"), proxies)
	if got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want the forwarded address", got)
	}
}

func TestClientIPTakesTheRightmostUntrustedHop(t *testing.T) {
	proxies, _ := ParseCIDRs([]string{"10.0.0.0/8"})
	// Everything left of the last trusted hop is supplied by the client and
	// can say anything at all.
	got := ClientIP(req("10.1.2.3:5555", "1.2.3.4, 198.51.100.7, 10.9.9.9"), proxies)
	if got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want 198.51.100.7", got)
	}
}

func TestClientIPFallsBackWhenForwardedIsNonsense(t *testing.T) {
	proxies, _ := ParseCIDRs([]string{"10.0.0.0/8"})
	got := ClientIP(req("10.1.2.3:5555", "not-an-address"), proxies)
	if got != "10.1.2.3" {
		t.Errorf("ClientIP = %q, want the socket address", got)
	}
}

func TestAllowlist(t *testing.T) {
	if !Allowed("203.0.113.9:1", nil) {
		t.Error("an empty allowlist should allow everything")
	}
	allow, _ := ParseCIDRs([]string{"192.168.8.0/24", "10.0.0.0/8"})
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"192.168.8.4:1234", true},
		{"10.9.9.9:1", true},
		{"203.0.113.9:1", false},
		{"garbage", false},
	} {
		if got := Allowed(tc.addr, allow); got != tc.want {
			t.Errorf("Allowed(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
