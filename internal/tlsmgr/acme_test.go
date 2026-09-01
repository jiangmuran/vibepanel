package tlsmgr

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

// A certificate the panel obtained at startup has to keep being maintained.
//
// certmagic asks the cache's GetConfigForCert for the config that manages each
// cached certificate, on every maintenance tick, and refuses any config bound
// to a different cache. Handing back certmagic.NewDefault() -- a config on
// certmagic's package-level cache, with certmagic's default storage and no
// issuers -- fails that check, so maintenance logs "unable to get
// configuration to manage certificate; unable to renew" and moves on. Nothing
// else says anything: the panel serves the certificate it started with until
// it expires, which for Let's Encrypt is day 90.
//
// Storage is seeded twice here rather than a CA being contacted: the cached
// certificate is about to expire, storage then holds a fresh one, and
// maintenance is supposed to notice and reload it. It can only do that through
// this panel's config, because that is what knows where the certificates are.
func TestMaintenanceUsesThisPanelsConfig(t *testing.T) {
	const domain = "panel.example.test"
	t.Setenv("CLOUDFLARE_API_TOKEN", "not-used-by-this-test")

	solver, err := dnsSolver("cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	// A CA that resolves to nothing: this test must not reach the network, and
	// a hostname is what would take it there if some path did try.
	cache, cfg := newManaged(ACMEOptions{
		Domain:     domain,
		Email:      "nobody@example.test",
		Directory:  "https://127.0.0.1:1/acme/directory",
		Provider:   "cloudflare",
		StorageDir: t.TempDir(),
	}, solver)
	defer cache.Stop()

	ctx := context.Background()
	issuerKey := cfg.Issuers[0].IssuerKey()

	// 10 days left of a 90-day certificate: inside certmagic's renewal window,
	// which opens at a third of the lifetime remaining.
	expiring := storeCert(t, cfg.Storage, issuerKey, domain, -80*24*time.Hour, 10*24*time.Hour)
	if _, err := cfg.CacheManagedCertificate(ctx, domain); err != nil {
		t.Fatal(err)
	}
	// As a renewal would leave it -- written here, so that the whole exchange
	// stays off the network.
	fresh := storeCert(t, cfg.Storage, issuerKey, domain, -time.Hour, 89*24*time.Hour)

	if err := cache.RenewManagedCertificates(ctx); err != nil {
		t.Fatal(err)
	}

	certs := cache.AllMatchingCertificates(domain)
	if len(certs) != 1 {
		t.Fatalf("cache holds %d certificates for %s, want 1", len(certs), domain)
	}
	switch got := certs[0].Leaf.NotAfter; {
	case got.Equal(fresh):
		// Maintenance read this panel's storage and reloaded.
	case got.Equal(expiring):
		t.Fatal("maintenance left the expiring certificate in place: the cache was handed a config it refuses, so nothing is ever renewed")
	default:
		t.Fatalf("certificate expires %s, want %s", got, fresh)
	}
}

// storeCert writes a self-signed certificate for domain where certmagic's
// managed-certificate loader looks for it, and returns its expiry.
func storeCert(t *testing.T, storage certmagic.Storage, issuerKey, domain string, notBefore, notAfter time.Duration) time.Time {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    now.Add(notBefore),
		NotAfter:     now.Add(notAfter),
		DNSNames:     []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := new(bytes.Buffer)
	if err := pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	keyPEM := new(bytes.Buffer)
	if err := pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for k, value := range map[string][]byte{
		certmagic.StorageKeys.SiteCert(issuerKey, domain):       certPEM.Bytes(),
		certmagic.StorageKeys.SitePrivateKey(issuerKey, domain): keyPEM.Bytes(),
		certmagic.StorageKeys.SiteMeta(issuerKey, domain):       []byte(`{"sans":["` + domain + `"]}`),
	} {
		if err := storage.Store(ctx, k, value); err != nil {
			t.Fatal(err)
		}
	}
	return tmpl.NotAfter
}
