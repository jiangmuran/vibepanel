package vnc

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func cidrs(t *testing.T, values ...string) []*net.IPNet {
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

// An unconfigured panel reaches loopback and nothing else.
//
// This is the inversion of --allow-from and the one property most likely to be
// "corrected" by somebody making the two flags consistent. If the empty list
// ever means "everything", a panel nobody configured becomes a port scanner
// pointed at whatever network it is on.
func TestAnEmptyPolicyIsLoopbackOnly(t *testing.T) {
	var p Policy
	for _, ok := range []string{"127.0.0.1", "127.5.5.5", "::1", "::ffff:127.0.0.1"} {
		if !p.Permits(net.ParseIP(ok)) {
			t.Errorf("%s is loopback and was refused", ok)
		}
	}
	for _, no := range []string{"10.0.0.1", "192.168.1.10", "8.8.8.8", "169.254.169.254", "0.0.0.0"} {
		if p.Permits(net.ParseIP(no)) {
			t.Errorf("%s was permitted by a policy nobody configured", no)
		}
	}
}

// The list replaces the default rather than adding to it, so an operator can
// say "not even itself" and be believed.
func TestAConfiguredListReplacesTheDefault(t *testing.T) {
	p := Policy{Allow: cidrs(t, "192.168.1.0/24")}
	if !p.Permits(net.ParseIP("192.168.1.7")) {
		t.Error("an address inside the configured network was refused")
	}
	if p.Permits(net.ParseIP("127.0.0.1")) {
		t.Error("loopback was permitted although the configured list does not name it; " +
			"an invisible exception is one nobody can turn off")
	}
}

// ::ffff:10.0.0.1 is 10.0.0.1 written the other way. Without normalisation a
// v4 CIDR does not contain it, so the mapped spelling is a way past the list.
func TestTheMappedSpellingIsNotAWayAround(t *testing.T) {
	p := Policy{Allow: cidrs(t, "127.0.0.0/8")}
	if p.Permits(net.ParseIP("::ffff:10.0.0.1")) {
		t.Error("::ffff:10.0.0.1 was permitted by a loopback-only list")
	}
	if !p.Permits(net.ParseIP("::ffff:127.0.0.1")) {
		t.Error("::ffff:127.0.0.1 is loopback and was refused")
	}
}

func TestPortsOutsideTheRangeAreRefused(t *testing.T) {
	for _, bad := range []int{0, -1, 65536, 1 << 20} {
		if ValidPort(bad) {
			t.Errorf("port %d is valid according to ValidPort", bad)
		}
	}
	for _, good := range []int{1, 5900, 65535} {
		if !ValidPort(good) {
			t.Errorf("port %d was refused", good)
		}
	}
	// And Dial refuses before it resolves anything, so a stored zero cannot
	// become a connection attempt to whatever the kernel picks.
	if _, err := (Policy{}).Dial(context.Background(), "127.0.0.1", 0); !errors.Is(err, ErrRefused) {
		t.Errorf("dialling port 0 gave %v, want ErrRefused", err)
	}
}

// A name with one record inside the policy and one outside is refused, not
// accepted on the strength of the good one.
//
// This is the rule that makes DNS rebinding pointless. A "first match wins"
// policy makes every connection a coin flip, and a name the panel does not
// control can grow a second record at any time.
func TestEveryResolvedAddressHasToPass(t *testing.T) {
	p := Policy{
		Allow: cidrs(t, "127.0.0.0/8"),
		Lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("169.254.169.254")}, nil
		},
	}
	_, err := p.Resolve(context.Background(), "desk.local")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Resolve gave %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "169.254.169.254") {
		t.Errorf("the refusal does not name the address that caused it: %v", err)
	}
}

func TestANameWhollyInsideThePolicyResolves(t *testing.T) {
	p := Policy{
		Allow: cidrs(t, "10.0.0.0/8"),
		Lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("10.1.2.3"), net.ParseIP("10.4.5.6")}, nil
		},
	}
	ips, err := p.Resolve(context.Background(), "desk.local")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("Resolve returned %d addresses, want 2", len(ips))
	}
}

// A name that resolves to nothing is an error rather than an empty list that
// Dial would loop zero times over and report as a nil connection.
func TestANameThatResolvesToNothingIsAnError(t *testing.T) {
	p := Policy{Lookup: func(context.Context, string) ([]net.IP, error) { return nil, nil }}
	if _, err := p.Resolve(context.Background(), "nowhere.local"); err == nil {
		t.Fatal("a host with no addresses resolved without error")
	}
}

// Dial connects to the address Resolve agreed to, and never asks the resolver
// a second time.
//
// The lookup counter is the assertion: a second call between the check and the
// connect is the window a rebind lands in, and the only way to see it from
// outside is to count.
func TestDialResolvesOnceAndConnectsToWhatItChecked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	lookups := 0
	p := Policy{
		Allow: cidrs(t, "127.0.0.0/8"),
		Lookup: func(context.Context, string) ([]net.IP, error) {
			lookups++
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	}
	conn, err := p.Dial(context.Background(), "desk.local", port)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
	if lookups != 1 {
		t.Errorf("Dial resolved %d times; a second lookup is the rebinding window", lookups)
	}
}

func TestARefusedAddressNeverReachesTheNetwork(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- struct{}{}
		c.Close()
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	// A policy that does not cover loopback, dialling loopback.
	p := Policy{Allow: cidrs(t, "192.168.0.0/16")}
	if _, err := p.Dial(context.Background(), "127.0.0.1", port); !errors.Is(err, ErrRefused) {
		t.Fatalf("Dial gave %v, want ErrRefused", err)
	}
	select {
	case <-accepted:
		t.Fatal("the listener accepted a connection the policy refused")
	default:
	}
}

// The refusal says what would have had to be true, because "refused" alone
// leaves somebody guessing at a flag they may not know exists.
func TestARefusalNamesThePolicy(t *testing.T) {
	if got := (Policy{}).Describe(); !strings.Contains(got, "vnc-allow") {
		t.Errorf("Describe() = %q and does not name the flag that would change it", got)
	}
	got := Policy{Allow: cidrs(t, "10.0.0.0/8")}.Describe()
	if !strings.Contains(got, "10.0.0.0/8") {
		t.Errorf("Describe() = %q and does not name the configured network", got)
	}
}
