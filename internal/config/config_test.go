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

// A setting whose feature has been removed reads exactly like a misspelling,
// and it has to.
//
// VNC is gone. Nobody is going to edit a unit file because a release note said
// to, so the upgrade this has to survive is a machine that starts with
// `Environment=VIBEPANEL_VNC_ALLOW=10.0.0.0/8` still in it. The panel starts --
// this is not a fatal error, a future version may add names this build does
// not know -- but it says the line is doing nothing, which is the whole point
// of the report: a security setting that is inert and looks applied is the
// failure this mechanism exists for, and a retired name is the one case where
// it *used* to be applied.
func TestARetiredSettingIsReportedRatherThanIgnored(t *testing.T) {
	t.Setenv("VIBEPANEL_VNC_ALLOW", "10.0.0.0/8")
	c := Default()
	c.envOverlay()

	if len(c.UnknownEnv) != 1 || c.UnknownEnv[0] != "VIBEPANEL_VNC_ALLOW" {
		t.Errorf("UnknownEnv = %v, want exactly [VIBEPANEL_VNC_ALLOW]. A name that was read "+
			"by the previous release and is read by nothing now has to be said out loud, "+
			"or the operator's allowlist is silently not there any more.", c.UnknownEnv)
	}
}

// A retired *flag* is refused, and that is the opposite treatment from the
// environment variable above. The difference is deliberate and this pins it.
//
// The tempting kindness is to keep accepting `--vnc` and do nothing with it for
// a version, so an upgrade does not stop a panel. It is the wrong kindness:
// there is no viewer, no proxy and no settings page any more, so a panel that
// starts on `--vnc` is one whose operator believes they have a feature that is
// not in the binary -- which is precisely the "a setting that is not applied
// looks exactly like one that is" failure the UnknownEnv warning exists for,
// except a flag can do better than a warning because a flag is refused before
// anything starts.
//
// The environment cannot be treated that way: a VIBEPANEL_* name this build
// does not know may belong to a newer one, and refusing to start over a
// variable would make every rename a breaking change. A flag typed on a command
// line has an author, a machine that failed to come up, and `flag provided but
// not defined: -vnc` naming the word to delete.
func TestTheRetiredVncFlagsAreRefusedRatherThanAcceptedAndIgnored(t *testing.T) {
	for _, args := range [][]string{
		{"--domain", "localhost", "--vnc"},
		{"--domain", "localhost", "--vnc-allow", "10.0.0.0/8"},
	} {
		if _, err := Load(args, io.Discard); err == nil {
			t.Errorf("%v was accepted. A flag that is taken and ignored leaves an operator "+
				"believing the panel has a VNC viewer; there is no longer any code behind "+
				"the word.", args)
		}
	}
}

// Flags have to be able to override the environment, including downwards.
//
// The ordering exists so that somebody debugging a unit-managed instance can
// change one value on the command line without editing Environment= lines. An
// allowlist is the value most likely to need that treatment, because getting it
// wrong is what locks you out — and clearing it used to be the one thing the
// flag could not do.
func TestFlagsOverrideTheEnvironmentInBothDirections(t *testing.T) {
	const env = "192.168.0.0/16"

	t.Run("absent flag keeps the environment", func(t *testing.T) {
		t.Setenv("VIBEPANEL_ALLOW_FROM", env)
		c, err := Load([]string{"--domain", "localhost"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.AllowFrom) != 1 || c.AllowFrom[0] != env {
			t.Errorf("AllowFrom = %v, want the environment's %q", c.AllowFrom, env)
		}
	})

	t.Run("a flag replaces the environment", func(t *testing.T) {
		t.Setenv("VIBEPANEL_ALLOW_FROM", env)
		c, err := Load([]string{"--allow-from", "10.0.0.0/8"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.AllowFrom) != 1 || c.AllowFrom[0] != "10.0.0.0/8" {
			t.Errorf("AllowFrom = %v, want the flag's value", c.AllowFrom)
		}
	})

	t.Run("an empty flag clears the environment", func(t *testing.T) {
		t.Setenv("VIBEPANEL_ALLOW_FROM", env)
		t.Setenv("VIBEPANEL_TRUSTED_PROXIES", "127.0.0.1/32")
		c, err := Load([]string{"--allow-from", "", "--trusted-proxies", ""}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.AllowFrom) != 0 {
			t.Errorf("AllowFrom = %v; --allow-from=\"\" did not turn the allowlist off",
				c.AllowFrom)
		}
		if len(c.TrustedProxies) != 0 {
			t.Errorf("TrustedProxies = %v; the flag did not clear it", c.TrustedProxies)
		}
	})
}

// BindHost decides where a hook running inside a session posts its state. Get
// it wrong and every report goes to an address nothing is listening on — which
// the hook script swallows without a word, so the panel goes on guessing while
// the settings page says hooks are installed.
func TestBindHost(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		// Wildcard forms all include the loopback interface, so loopback is
		// reachable and "" means "use it".
		{":8443", ""},
		{"0.0.0.0:8443", ""},
		{"[::]:8443", ""},
		// A single interface: nothing is listening on 127.0.0.1 then.
		{"192.168.8.20:8443", "192.168.8.20"},
		{"127.0.0.1:8443", "127.0.0.1"},
		{"localhost:8443", "localhost"},
		{"[fd00::1]:8443", "fd00::1"},
		// Malformed: answer as for the wildcard rather than inventing a host.
		// The listener will not start anyway, and a wrong guess here would send
		// hook reports somewhere real.
		{"nonsense", ""},
		{"", ""},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			c := Default()
			c.Addr = tc.addr
			if got := c.BindHost(); got != tc.want {
				t.Errorf("BindHost(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// The address a hook posts to, injected into every session's environment.
//
// It used to be hard-coded to 127.0.0.1, which is right only while the panel
// listens on every interface. Bound to one — an ordinary way to narrow
// exposure — nothing answers on loopback, every report is refused, and the
// hook script suppresses the error by design. Hooks then report nothing while
// the settings page says they are installed.
func TestHookURLFollowsTheBindAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
		tls  TLSMode
		want string
	}{
		{"wildcard", ":8443", TLSOff, "http://127.0.0.1:8443"},
		{"all interfaces", "0.0.0.0:8443", TLSOff, "http://127.0.0.1:8443"},
		{"one interface", "192.168.8.20:8443", TLSOff, "http://192.168.8.20:8443"},
		{"loopback explicitly", "127.0.0.1:9000", TLSOff, "http://127.0.0.1:9000"},
		// The script passes --insecure precisely because this certificate is
		// issued for the public hostname and a local address will never match.
		{"under tls", ":8443", TLSFiles, "https://127.0.0.1:8443"},
		// An IPv6 literal needs brackets or the URL is unparseable.
		{"ipv6", "[fd00::1]:8443", TLSOff, "http://[fd00::1]:8443"},
		// No usable port: fall back rather than emitting ":0".
		{"no port", "nonsense", TLSOff, "http://127.0.0.1:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Addr: tc.addr, TLSMode: tc.tls}
			if got := c.LoopbackURL(); got != tc.want {
				t.Errorf("LoopbackURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaintextOnANetworkIsNoticed(t *testing.T) {
	// The defaults put the panel in this state: `:8443` is every interface and
	// `off` is the default TLS mode, so out of the box a terminal and a
	// password form are served unencrypted to anything that can route to this
	// machine. The only thing that said so was one letter in
	// `url http://…:8443`, on the same screen as the setup token.
	//
	// Loopback is exempt because that is genuinely private, and a name that is
	// not "localhost" is not: it is something this machine answers to.
	cases := []struct {
		addr string
		tls  TLSMode
		want bool
	}{
		{":8443", TLSOff, true},
		{"0.0.0.0:8443", TLSOff, true},
		{"[::]:8443", TLSOff, true},
		{"192.168.1.10:8443", TLSOff, true},
		{"panel.example.com:8443", TLSOff, true},
		{"127.0.0.1:8443", TLSOff, false},
		{"[::1]:8443", TLSOff, false},
		{"localhost:8443", TLSOff, false},
		// TLS on: the traffic is not in the clear, wherever it is bound.
		{":8443", TLSFiles, false},
		{"0.0.0.0:8443", TLSACME, false},
	}
	for _, tc := range cases {
		c := Config{Addr: tc.addr, TLSMode: tc.tls}
		if got := c.PlaintextOnANetwork(); got != tc.want {
			t.Errorf("Addr=%q TLSMode=%q: PlaintextOnANetwork() = %v, want %v",
				tc.addr, tc.tls, got, tc.want)
		}
	}
}
