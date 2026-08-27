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
// Commands is what `--help` says this binary can be asked to do.
//
// It said nothing. The usage line was "vibepanel [flags]" followed by a flag
// list, so a person who installed the release archive and asked the binary
// what it does was never told that `doctor`, `project` or `session` exist —
// while the runbook opens by telling them to run `vibepanel doctor`.
//
// The list was already written, in the error for an unknown command. It was
// simply never shown to anyone who asked politely.
//
// Here rather than in main because this is the only place that prints usage,
// and a second copy of the list is how the two stop agreeing.
const Commands = `  serve      run the panel (the default with no command)
  project    add, list and remove projects
  session    create, list and kill sessions
  hook       install or remove the agent state reporter
  service    status, start, stop, logs, token, upgrade, uninstall
  account    create the first account without the browser
  doctor     check tmux, the database, disk and isolation
  version    print the version`

func Load(args []string, out io.Writer) (Config, error) {
	c := Default()
	c.envOverlay()

	fs := flag.NewFlagSet("vibepanel", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "vibepanel — a web console for many parallel coding sessions.\n\n")
		fmt.Fprintf(out, "Usage:\n  vibepanel [command] [flags]\n\nCommands:\n%s\n", Commands)
		fmt.Fprintf(out, "Flags (for serve, which is what runs with no command):\n")
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
	fs.BoolVar(&c.VNC, "vnc", c.VNC, "turn on the built-in VNC viewer; off means its routes do not exist")
	vncAllow := fs.String("vnc-allow", "", "comma-separated CIDRs the VNC viewer may connect out to; empty means loopback only")

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
	// Same treatment, and it matters more here: clearing this one narrows the
	// policy rather than widening it, so `--vnc-allow=""` has to be able to
	// take a unit's Environment= line back to loopback-only.
	if typed["vnc-allow"] {
		c.VNCAllow = splitAndTrim(*vncAllow)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
