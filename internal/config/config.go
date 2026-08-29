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

	// Addr is the listen address, e.g. ":18443" or "127.0.0.1:18443".
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

// DefaultPort is the port the panel listens on when nothing says otherwise.
//
// 18443 rather than 8443. The low one is crowded -- it is the conventional
// second HTTPS port, and the machine this was first installed on already had a
// TLS proxy sitting on it, so the panel bound, failed, and was restarted every
// few seconds. A default that collides on an ordinary developer's box is a
// default that costs everybody the same twenty minutes.
//
// Three files state it and this is the one they are checked against:
// deploy/install.sh's fallback when there is no env file, and the
// VIBEPANEL_ADDR line in deploy/vibepanel.env. TestTheInstallerAgreesAboutThePort
// is what stops them drifting.
const DefaultPort = 18443

// Default returns the configuration used when nothing is specified.
func Default() Config {
	return Config{
		DataDir:    defaultDataDir(),
		Addr:       fmt.Sprintf(":%d", DefaultPort),
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

// RestoreDir holds the scrollback handed to a pane being rebuilt.
//
// A file rather than an argument or an environment variable: the archive is a
// couple of hundred kilobytes, and both of those routes go through the process
// argument space. The pane's own first command reads it and deletes it, so a
// file here means either a restore that is still in flight or one whose pane
// never started.
func (c Config) RestoreDir() string { return filepath.Join(c.DataDir, "restore") }

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
	for _, dir := range []string{c.DataDir, c.TmuxDir(), c.ACMEDir(), c.RestoreDir()} {
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

// BindHost is the single interface the listener is bound to, or "" when it
// listens on all of them.
//
// The wildcard forms — ":8443", "0.0.0.0:8443", "[::]:8443" — all include the
// loopback interface, so callers that just want to reach the panel from this
// machine can use 127.0.0.1. A specific address means they cannot: nothing is
// listening on loopback, and the difference is invisible until something that
// assumed otherwise stops working.
func (c Config) BindHost() string {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		return ""
	}
	return host
}

// PlaintextOnANetwork reports whether the panel is about to serve a terminal,
// and the form you type your password into, unencrypted on an interface other
// people can reach.
//
// The defaults make this the out-of-the-box state: `:18443` is every interface,
// and `--tls off` is the default mode. Nothing said so. The banner prints
// `url http://…`, and one letter is the whole warning — easy to read past on
// the run where you also copy the setup token.
//
// Not a refusal. Plaintext on a trusted LAN, or behind a reverse proxy that
// terminates TLS itself, is a legitimate way to run this, and a panel that
// refused to start would be worked around within the minute. It just has to be
// said out loud, where the operator is already looking.
func (c Config) PlaintextOnANetwork() bool {
	if c.TLSMode != TLSOff {
		return false
	}
	host := c.BindHost()
	if host == "" {
		return true // wildcard: every interface this machine has
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	// An unparseable host is a name, and a name that is not "localhost" is
	// something this machine is reachable by.
	return ip == nil || !ip.IsLoopback()
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

// LoopbackURL is where a hook running inside a session posts its state.
//
// Always loopback, never the public URL: the hook runs beside the panel, and
// sending its reports out to the internet and back would put a secret on the
// wire for no reason. From this machine to this machine, which is why the
// script that uses it passes --insecure — under TLS the certificate is issued
// for the public hostname and will never match a local address.
//
// Loopback only when the panel is actually listening there. Bound to one
// interface — "--addr 192.168.8.20:8443", which is an ordinary way to narrow
// exposure — nothing answers on 127.0.0.1, and the hook script suppresses every
// error by design. The result is hooks that report nothing, a settings page
// that says they are installed, and states that stay guessed with no
// explanation available anywhere.
//
// It lives on Config rather than on the server because the admin CLI creates
// sessions too, and it was building a different, shorter set of hook variables
// — so a session started with `vibepanel session new` could never report its
// state. One definition, two callers.
func (c Config) LoopbackURL() string {
	port := c.Port()
	if port == 0 {
		port = DefaultPort
	}
	scheme := "http"
	if c.TLSMode != TLSOff {
		scheme = "https"
	}
	host := "127.0.0.1"
	if bound := c.BindHost(); bound != "" {
		host = bound
	}
	// An IPv6 literal needs brackets before it can go in a URL.
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}
