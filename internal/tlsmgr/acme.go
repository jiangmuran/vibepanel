package tlsmgr

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
)

// ACMEOptions configures automatic certificates.
type ACMEOptions struct {
	Domain string
	Email  string
	// Directory overrides the CA endpoint. Empty means Let's Encrypt
	// production. Point it at the staging endpoint while debugging: the
	// production rate limits are strict enough that a retry loop exhausts them
	// and locks the domain out for a week.
	Directory string
	// Provider names the DNS-01 provider.
	Provider string
	// StorageDir holds certificates and the account key.
	StorageDir string
	Log        *slog.Logger
}

// ErrNoDNSToken means the provider credential is missing.
var ErrNoDNSToken = errors.New("tlsmgr: no DNS API token; set CLOUDFLARE_API_TOKEN")

// ACMESource owns a certmagic cache and config so additional domains (such as
// a dedicated share origin) can be issued and auto-renewed live via SNI
// without restarting the server.
type ACMESource struct {
	cache  *certmagic.Cache
	cfg    *certmagic.Config
	tlsCfg *tls.Config
	log    *slog.Logger
}

// NewACMESource obtains the initial certificate for o.Domain and returns a
// live source capable of adding more managed domains afterwards.
//
// DNS-01 rather than HTTP-01, because the panel is expected to listen on a
// non-standard port and HTTP-01 requires port 80 — which either is not free or
// means running something else in front, defeating the point of a single
// binary.
func NewACMESource(ctx context.Context, o ACMEOptions) (*ACMESource, error) {
	if o.Domain == "" {
		return nil, errors.New("tlsmgr: acme needs a domain")
	}
	solver, err := dnsSolver(o.Provider)
	if err != nil {
		return nil, err
	}

	cache, cfg := newManaged(o, solver)

	// Synchronous: a panel that starts, listens, and only then discovers it
	// has no certificate would greet its first visitor with a handshake error
	// and nothing to explain it.
	if err := cfg.ManageSync(ctx, []string{o.Domain}); err != nil {
		cache.Stop()
		return nil, fmt.Errorf("tlsmgr: obtain certificate for %s: %w", o.Domain, err)
	}
	if o.Log != nil {
		o.Log.Info("certificate ready", "domain", o.Domain, "ca", caEndpoint(o.Directory))
	}

	tlsCfg := cfg.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12
	// certmagic sets NextProtos for its own HTTP-01 solver; the panel speaks
	// HTTP/1.1 and HTTP/2 and nothing else.
	tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	return &ACMESource{cache: cache, cfg: cfg, tlsCfg: tlsCfg, log: o.Log}, nil
}

// TLSConfig returns the SNI-capable TLS configuration for the HTTP server.
func (a *ACMESource) TLSConfig() *tls.Config { return a.tlsCfg }

// Manage obtains and begins maintaining a certificate for an additional domain
// on the live TLS listener without restarting.
func (a *ACMESource) Manage(ctx context.Context, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return errors.New("tlsmgr: domain is required")
	}
	if err := a.cfg.ManageSync(ctx, []string{domain}); err != nil {
		return fmt.Errorf("tlsmgr: obtain certificate for %s: %w", domain, err)
	}
	if a.log != nil {
		a.log.Info("certificate ready", "domain", domain)
	}
	return nil
}

// Close stops background certificate maintenance.
func (a *ACMESource) Close() {
	if a != nil && a.cache != nil {
		a.cache.Stop()
	}
}

// NewACME obtains a certificate and returns a TLS configuration that keeps it
// renewed.
func NewACME(ctx context.Context, o ACMEOptions) (*tls.Config, error) {
	src, err := NewACMESource(ctx, o)
	if err != nil {
		return nil, err
	}
	return src.TLSConfig(), nil
}

// newManaged builds the certificate cache and the config that maintains what
// is in it.
//
// The callback has to hand back this config, and it used to return
// certmagic.NewDefault(). certmagic asks it for the config that manages each
// cached certificate on every maintenance tick and refuses any config bound to
// a different cache; a default one is bound to certmagic's package-level cache
// -- with certmagic's own storage, and no issuers, so neither the panel's
// StorageDir nor the DNS-01 solver below is in it. Maintenance then logged
// "unable to get configuration to manage certificate; unable to renew" and
// moved on, in a process nothing else complains in, and the certificate the
// panel obtained at startup was renewed by restarting the panel and by nothing
// else: at day 90 every browser gets an expired certificate.
func newManaged(o ACMEOptions, solver *certmagic.DNS01Solver) (*certmagic.Cache, *certmagic.Config) {
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return cfg, nil
		},
	})
	cfg = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: o.StorageDir},
		Logger:  nil,
	})
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:          caEndpoint(o.Directory),
		Email:       o.Email,
		Agreed:      true,
		DNS01Solver: solver,
	})
	cfg.Issuers = []certmagic.Issuer{issuer}
	return cache, cfg
}

func caEndpoint(directory string) string {
	if directory != "" {
		return directory
	}
	return certmagic.LetsEncryptProductionCA
}

// dnsSolver builds the DNS-01 solver for a provider name.
func dnsSolver(provider string) (*certmagic.DNS01Solver, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "cloudflare":
		token := firstEnv("CLOUDFLARE_API_TOKEN", "CF_API_TOKEN")
		if token == "" {
			return nil, ErrNoDNSToken
		}
		return &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{
				DNSProvider: &cloudflare.Provider{APIToken: token},
			},
		}, nil
	case "":
		return nil, errors.New("tlsmgr: acme needs a DNS provider; HTTP-01 cannot work on a non-standard port")
	default:
		return nil, fmt.Errorf("tlsmgr: unknown DNS provider %q (supported: cloudflare)", provider)
	}
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
