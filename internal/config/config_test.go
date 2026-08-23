package config

import (
	"io"
	"strings"
	"testing"
)

func TestPasskeysUsable(t *testing.T) {
	// The login page shows or hides the passkey button based on this, so a
	// wrong answer means either a dead button or a confusing browser error.
	cases := []struct {
		name   string
		domain string
		tls    TLSMode
		want   bool
	}{
		{"domain with TLS", "panel.example.com", TLSACME, true},
		{"domain with cert files", "panel.example.com", TLSFiles, true},
		{"domain without TLS", "panel.example.com", TLSOff, false},
		{"localhost without TLS is a secure context", "localhost", TLSOff, true},
		{"IPv4 is never a valid RP ID", "192.168.8.4", TLSFiles, false},
		{"IPv6 is never a valid RP ID", "::1", TLSFiles, false},
		{"no domain at all", "", TLSFiles, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Domain: tc.domain, TLSMode: tc.tls}
			if got := c.PasskeysUsable(); got != tc.want {
				t.Errorf("PasskeysUsable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	base := func() Config {
		c := Default()
		c.DataDir = "/tmp/vibepanel-test"
		return c
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"defaults are valid", func(*Config) {}, ""},
		{"empty data dir", func(c *Config) { c.DataDir = "" }, "data dir"},
		{"addr without port", func(c *Config) { c.Addr = "localhost" }, "invalid addr"},
		{"files mode without cert", func(c *Config) { c.TLSMode = TLSFiles }, "tls-cert"},
		{"acme without domain", func(c *Config) { c.TLSMode = TLSACME }, "needs --domain"},
		{"acme without dns provider", func(c *Config) {
			c.TLSMode, c.Domain = TLSACME, "panel.example.com"
		}, "acme-dns"},
		{"acme fully specified", func(c *Config) {
			c.TLSMode, c.Domain, c.ACMEDNSProvider = TLSACME, "panel.example.com", "cloudflare"
		}, ""},
		{"unknown tls mode", func(c *Config) { c.TLSMode = "sure" }, "unknown tls mode"},
		{"domain as IP", func(c *Config) { c.Domain = "192.168.8.4" }, "not the IP"},
		{"bad trusted proxy", func(c *Config) { c.TrustedProxies = []string{"nonsense"} }, "not a CIDR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadPrecedence(t *testing.T) {
	// Environment sets a baseline (systemd unit); a flag overrides one value
	// (operator debugging by hand). Getting this backwards would make the
	// command line silently useless.
	t.Setenv("VIBEPANEL_ADDR", ":9001")
	t.Setenv("VIBEPANEL_TMUX_SOCKET", "from-env")
	t.Setenv("VIBEPANEL_DATA_DIR", "/tmp/vibepanel-env")

	c, err := Load([]string{"--tmux-socket", "from-flag"}, io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":9001" {
		t.Errorf("Addr = %q, want the environment value :9001", c.Addr)
	}
	if c.TmuxSocket != "from-flag" {
		t.Errorf("TmuxSocket = %q, want the flag to beat the environment", c.TmuxSocket)
	}
}

func TestLoadRejectsIPDomain(t *testing.T) {
	_, err := Load([]string{"--domain", "192.168.8.4"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Relying Party") {
		t.Fatalf("expected an RP ID explanation, got %v", err)
	}
}

func TestPublicURL(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Domain: "p.example.com", Addr: ":8443", TLSMode: TLSACME}, "https://p.example.com:8443"},
		{Config{Domain: "p.example.com", Addr: ":443", TLSMode: TLSFiles}, "https://p.example.com"},
		{Config{Addr: ":7700", TLSMode: TLSOff}, "http://localhost:7700"},
	}
	for _, tc := range cases {
		if got := tc.cfg.PublicURL(); got != tc.want {
			t.Errorf("PublicURL() = %q, want %q", got, tc.want)
		}
	}
}
