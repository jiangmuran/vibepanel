package config

import (
	"flag"
	"fmt"
	"io"
)

// Load resolves configuration from defaults, then environment, then flags.
//
// Flags are parsed last so that an operator debugging a systemd-managed
// instance can override one value on the command line without editing the
// unit's Environment= lines.
//
// A ParseError from the flag package (including -h) is returned as-is so the
// caller can exit quietly rather than printing a stack of usage twice.
func Load(args []string, out io.Writer) (Config, error) {
	c := Default()
	c.envOverlay()

	fs := flag.NewFlagSet("vibepanel", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "vibepanel — a web console for many parallel coding sessions.\n\n")
		fmt.Fprintf(out, "Usage:\n  vibepanel [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(out, "\nEvery flag has a VIBEPANEL_<UPPER_SNAKE> environment equivalent.\n")
	}

	var tlsMode string
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "directory for the database, tmux config and ACME state")
	fs.StringVar(&c.Addr, "addr", c.Addr, "listen address")
	fs.StringVar(&c.Domain, "domain", c.Domain, "public hostname; also the WebAuthn Relying Party ID (must not be an IP)")
	fs.StringVar(&tlsMode, "tls", string(c.TLSMode), "TLS mode: off | files | acme")
	fs.StringVar(&c.CertFile, "tls-cert", c.CertFile, "certificate file (tls=files)")
	fs.StringVar(&c.KeyFile, "tls-key", c.KeyFile, "private key file (tls=files)")
	fs.StringVar(&c.ACMEEmail, "acme-email", c.ACMEEmail, "contact address for the CA (tls=acme)")
	fs.StringVar(&c.ACMEDirectory, "acme-directory", c.ACMEDirectory, "ACME directory URL; empty means Let's Encrypt production")
	fs.StringVar(&c.ACMEDNSProvider, "acme-dns", c.ACMEDNSProvider, "DNS-01 provider for ACME, e.g. cloudflare")
	fs.StringVar(&c.TmuxSocket, "tmux-socket", c.TmuxSocket, "tmux -L socket name; keep it dedicated to stay isolated from your own sessions")
	fs.StringVar(&c.StaticDir, "static-dir", c.StaticDir, "serve the frontend from this directory instead of the embedded build")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For is trusted")
	allowFrom := fs.String("allow-from", "", "comma-separated CIDRs allowed to reach the panel; empty allows all")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	c.TLSMode = TLSMode(tlsMode)

	// Which flags were actually typed, rather than which have a non-empty
	// value.
	//
	// Every other flag is registered with the environment-derived value as its
	// default, so "not passed" and "passed the same value" are the same thing
	// and precedence falls out. These two cannot be: they are joined strings
	// that have to be split. Testing them for emptiness instead made
	// `--allow-from=""` a no-op whenever the environment had set one — so an
	// operator locked out by an allowlist could not turn it off from the
	// command line, which is the exact scenario this function's ordering exists
	// to support. It looked like the flag did nothing, because it did nothing.
	typed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { typed[f.Name] = true })
	if typed["trusted-proxies"] {
		c.TrustedProxies = splitAndTrim(*proxies)
	}
	if typed["allow-from"] {
		c.AllowFrom = splitAndTrim(*allowFrom)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
