// Package config resolves runtime configuration from flags, environment and
// sensible defaults, in that order of precedence.
//
// Everything that touches the filesystem is overridable. A panel that can only
// run out of one hard-coded directory cannot be packaged, cannot run twice on
// one host for testing, and cannot be dropped into a container — all three of
// which this project needs.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TLSMode selects how the server obtains its certificate.
type TLSMode string

const (
	// TLSOff serves plain HTTP. Passkeys will not work — WebAuthn requires a
	// secure context — so the login page falls back to password only.
	TLSOff TLSMode = "off"

	// TLSFiles uses an operator-supplied certificate and key, reloaded when the
	// files change so an external renewal cron does not require a restart.
	TLSFiles TLSMode = "files"

	// TLSACME obtains and renews a certificate automatically over ACME using a
	// DNS-01 challenge. DNS-01 rather than HTTP-01 because the panel is
	// expected to listen on a non-standard port, and HTTP-01 requires port 80.
	TLSACME TLSMode = "acme"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// DataDir holds the database, the generated tmux config and ACME state.
	DataDir string

	// Addr is the listen address, e.g. ":8443" or "127.0.0.1:8443".
	Addr string

	// Domain is the public hostname. It doubles as the WebAuthn Relying Party
	// ID, which is why an IP address is rejected: the spec allows only
	// registrable domain names as RP IDs, so passkeys silently fail to register
	// against a bare address.
	Domain string

	TLSMode  TLSMode
	CertFile string
	KeyFile  string

	// ACMEEmail receives expiry warnings from the CA.
	ACMEEmail string
	// ACMEDirectory overrides the CA endpoint; empty means Let's Encrypt
	// production. Point it at the staging directory while testing so a broken
	// config cannot burn the real rate limit.
	ACMEDirectory string
	// ACMEDNSProvider names the DNS-01 provider, e.g. "cloudflare".
	ACMEDNSProvider string

	// TmuxSocket is the -L socket name. Dedicated by default so the panel can
	// never see, resize or kill the user's own tmux sessions.
	TmuxSocket string

	// StaticDir serves the frontend from disk instead of the embedded copy.
	// Used during development; empty means use the embedded build.
	StaticDir string

	// TrustedProxies lists CIDRs whose X-Forwarded-For header is believed.
	// Empty means trust nobody, which is correct when the panel is the edge.
	TrustedProxies []string

	// AllowFrom restricts which addresses may reach the panel at all. Empty
	// allows everything, which is the default — this is a way to narrow an
	// internet-facing deployment, not a requirement for a local one.
	AllowFrom []string

	// UnknownEnv lists VIBEPANEL_* variables that nothing here reads. Not an
	// error — a future version may add names this build does not know — but
	// worth saying out loud, because the failure mode is a setting that
	// quietly does nothing, and the setting people misspell is the TLS one.
	UnknownEnv []string
}

// Default returns the configuration used when nothing is specified.
func Default() Config {
	return Config{
		DataDir:    defaultDataDir(),
		Addr:       ":8443",
		TLSMode:    TLSOff,
		TmuxSocket: "vibepanel",
	}
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "vibepanel")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// A daemon with no resolvable home still has to put its database
		// somewhere it can find again after a restart.
		return "/var/lib/vibepanel"
	}
	return filepath.Join(home, ".local", "share", "vibepanel")
}

const envPrefix = "VIBEPANEL_"

// envOverlay applies VIBEPANEL_* environment variables.
//
// Environment beats defaults but loses to explicit flags, so that a systemd
// unit can set a baseline while an operator debugging by hand can still
// override one value on the command line.
func (c *Config) envOverlay() {
	seen := map[string]bool{}
	// Both spellings for anything whose flag name does not map onto the
	// documented VIBEPANEL_<UPPER_SNAKE> convention. The originals came first
	// and are in the shipped env file; the conventional ones are what anyone
	// reading the flag table would guess, and guessing wrong used to mean the
	// setting was silently not applied — which for TLS means a panel serving
	// plaintext on a public port while its operator believes otherwise.
	str := func(dst *string, keys ...string) {
		for _, key := range keys {
			seen[key] = true
			if v, ok := os.LookupEnv(key); ok {
				*dst = v
			}
		}
	}
	str(&c.DataDir, "VIBEPANEL_DATA_DIR")
	str(&c.Addr, "VIBEPANEL_ADDR")
	str(&c.Domain, "VIBEPANEL_DOMAIN")
	str(&c.CertFile, "VIBEPANEL_CERT_FILE", "VIBEPANEL_TLS_CERT")
	str(&c.KeyFile, "VIBEPANEL_KEY_FILE", "VIBEPANEL_TLS_KEY")
	str(&c.ACMEEmail, "VIBEPANEL_ACME_EMAIL")
	str(&c.ACMEDirectory, "VIBEPANEL_ACME_DIRECTORY")
	str(&c.ACMEDNSProvider, "VIBEPANEL_ACME_DNS_PROVIDER", "VIBEPANEL_ACME_DNS")
	str(&c.TmuxSocket, "VIBEPANEL_TMUX_SOCKET")
	str(&c.StaticDir, "VIBEPANEL_STATIC_DIR")

	for _, key := range []string{"VIBEPANEL_TLS_MODE", "VIBEPANEL_TLS"} {
		seen[key] = true
		if v, ok := os.LookupEnv(key); ok {
			c.TLSMode = TLSMode(v)
		}
	}
	for _, key := range []string{"VIBEPANEL_TRUSTED_PROXIES"} {
		seen[key] = true
		if v, ok := os.LookupEnv(key); ok && v != "" {
			c.TrustedProxies = splitAndTrim(v)
		}
	}
	for _, key := range []string{"VIBEPANEL_ALLOW_FROM"} {
		seen[key] = true
		if v, ok := os.LookupEnv(key); ok && v != "" {
			c.AllowFrom = splitAndTrim(v)
		}
	}
	// Anything else starting with VIBEPANEL_ is almost certainly a typo or a
	// name from a newer version, and either way it is doing nothing. Silence
	// here is how a security setting gets to be inert without anyone knowing.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, envPrefix) || seen[key] {
			continue
		}
		// Set by the hook script inside a session; not configuration.
		if key == "VIBEPANEL_SESSION_ID" || key == "VIBEPANEL_TOKEN" ||
			key == "VIBEPANEL_URL" || strings.HasPrefix(key, "VIBEPANEL_DEBUG_") {
			continue
		}
		c.UnknownEnv = append(c.UnknownEnv, key)
	}
	sort.Strings(c.UnknownEnv)
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// DBPath is where the SQLite database lives.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "vibepanel.db") }

// TmuxDir holds the generated tmux config.
func (c Config) TmuxDir() string { return filepath.Join(c.DataDir, "tmux") }

// ACMEDir holds cached certificates and account keys.
func (c Config) ACMEDir() string { return filepath.Join(c.DataDir, "acme") }

// PasskeysUsable reports whether WebAuthn registration can succeed with this
// configuration. The login page uses it to explain why the passkey button is
// disabled rather than letting the browser fail with an opaque error.
func (c Config) PasskeysUsable() bool {
	if c.Domain == "" {
		return false
	}
	// An IP address is never a valid Relying Party ID.
	if net.ParseIP(c.Domain) != nil {
		return false
	}
	// localhost is the one origin browsers treat as secure over plain HTTP.
	if c.Domain == "localhost" {
		return true
	}
	return c.TLSMode != TLSOff
}

// Validate checks for combinations that cannot work, so the process fails at
// startup with a clear message instead of at first request with a vague one.
func (c Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("config: data dir must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("config: invalid addr %q: %w", c.Addr, err)
	}
	switch c.TLSMode {
	case TLSOff:
	case TLSFiles:
		if c.CertFile == "" || c.KeyFile == "" {
			return errors.New("config: tls mode 'files' needs both --tls-cert and --tls-key")
		}
	case TLSACME:
		if c.Domain == "" {
			return errors.New("config: tls mode 'acme' needs --domain")
		}
		if c.ACMEDNSProvider == "" {
			return errors.New("config: tls mode 'acme' needs --acme-dns (HTTP-01 cannot work on a non-standard port)")
		}
	default:
		return fmt.Errorf("config: unknown tls mode %q", c.TLSMode)
	}
	if c.Domain != "" && net.ParseIP(c.Domain) != nil {
		return fmt.Errorf("config: --domain must be a hostname, not the IP %q; "+
			"WebAuthn rejects IP addresses as Relying Party IDs", c.Domain)
	}
	for _, cidr := range c.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: trusted proxy %q is not a CIDR: %w", cidr, err)
		}
	}
	for _, cidr := range c.AllowFrom {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("config: allow-from %q is not a CIDR: %w", cidr, err)
		}
	}
	return nil
}

// EnsureDirs creates the directory tree with owner-only permissions.
//
// 0700 throughout: the database holds password hashes and passkey material,
// and the panel is frequently run as a normal user on a shared box.
func (c Config) EnsureDirs() error {
	for _, dir := range []string{c.DataDir, c.TmuxDir(), c.ACMEDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("config: create %s: %w", dir, err)
		}
	}
	return nil
}

// Port extracts the numeric port from Addr, for messages that need to print a
// usable URL.
func (c Config) Port() int {
	_, portStr, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(portStr)
	return p
}

// PublicURL renders the address a user should open. Best-effort: it is used in
// log lines and the setup message, never for anything security-sensitive.
func (c Config) PublicURL() string {
	scheme := "http"
	if c.TLSMode != TLSOff {
		scheme = "https"
	}
	host := c.Domain
	if host == "" {
		host = "localhost"
	}
	port := c.Port()
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
		return scheme + "://" + host
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}
