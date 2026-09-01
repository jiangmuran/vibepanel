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

// NewACME obtains a certificate and returns a TLS configuration that keeps it
// renewed.
//
// DNS-01 rather than HTTP-01, because the panel is expected to listen on a
// non-standard port and HTTP-01 requires port 80 — which either is not free or
// means running something else in front, defeating the point of a single
// binary.
func NewACME(ctx context.Context, o ACMEOptions) (*tls.Config, error) {
	if o.Domain == "" {
		return nil, errors.New("tlsmgr: acme needs a domain")
	}
	solver, err := dnsSolver(o.Provider)
	if err != nil {
		return nil, err
	}

	_, cfg := newManaged(o, solver)

	// Synchronous: a panel that starts, listens, and only then discovers it
	// has no certificate would greet its first visitor with a handshake error
	// and nothing to explain it.
	if err := cfg.ManageSync(ctx, []string{o.Domain}); err != nil {
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
	return tlsCfg, nil
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
