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

// The flag table in the README promises VIBEPANEL_<UPPER_SNAKE> for every
// flag. Four of them did not hold, and the one people would reach for first is
// the TLS mode — where the consequence of being ignored is a panel serving
// plaintext on a public port while its operator believes otherwise.
func TestDocumentedEnvNamesAreHonoured(t *testing.T) {
	for _, tc := range []struct {
		env   string
		value string
		check func(Config) string
	}{
		{"VIBEPANEL_TLS", "files", func(c Config) string { return string(c.TLSMode) }},
		{"VIBEPANEL_TLS_MODE", "files", func(c Config) string { return string(c.TLSMode) }},
		{"VIBEPANEL_TLS_CERT", "/x/cert.pem", func(c Config) string { return c.CertFile }},
		{"VIBEPANEL_CERT_FILE", "/x/cert.pem", func(c Config) string { return c.CertFile }},
		{"VIBEPANEL_TLS_KEY", "/x/key.pem", func(c Config) string { return c.KeyFile }},
		{"VIBEPANEL_KEY_FILE", "/x/key.pem", func(c Config) string { return c.KeyFile }},
		{"VIBEPANEL_ACME_DNS", "cloudflare", func(c Config) string { return c.ACMEDNSProvider }},
		{"VIBEPANEL_ACME_DNS_PROVIDER", "cloudflare", func(c Config) string { return c.ACMEDNSProvider }},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			c := Default()
			c.envOverlay()
			if got := tc.check(c); got != tc.value {
				t.Errorf("%s=%s was ignored; the setting reads %q", tc.env, tc.value, got)
			}
			if len(c.UnknownEnv) != 0 {
				t.Errorf("a documented name was reported as unknown: %v", c.UnknownEnv)
			}
		})
	}
}

func TestMisspelledEnvIsReported(t *testing.T) {
	t.Setenv("VIBEPANEL_TSL", "files") // the transposition someone will make
	t.Setenv("VIBEPANEL_SESSION_ID", "s-123")
	t.Setenv("VIBEPANEL_ADDR", ":9000")
	c := Default()
	c.envOverlay()

	if c.TLSMode != TLSOff {
		t.Errorf("a misspelling somehow applied: %q", c.TLSMode)
	}
	if len(c.UnknownEnv) != 1 || c.UnknownEnv[0] != "VIBEPANEL_TSL" {
		t.Errorf("UnknownEnv = %v, want exactly [VIBEPANEL_TSL]", c.UnknownEnv)
	}
	// The hook script's own variables are set inside every session and are not
	// configuration; reporting them would be noise on every start.
	for _, k := range c.UnknownEnv {
		if k == "VIBEPANEL_SESSION_ID" {
			t.Error("the hook script's session id was reported as a stray setting")
		}
	}
}
