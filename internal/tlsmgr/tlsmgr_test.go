package tlsmgr

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePair generates a self-signed certificate for testing.
func writePair(t *testing.T, dir, commonName string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{commonName},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, commonName+".crt")
	keyPath = filepath.Join(dir, commonName+".key")

	certPEM, _ := os.Create(certPath)
	pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}) //nolint:errcheck
	certPEM.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := os.Create(keyPath)
	pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}) //nolint:errcheck
	keyPEM.Close()
	return certPath, keyPath
}

func commonNameOf(t *testing.T, cert *tls.Certificate) string {
	t.Helper()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Subject.CommonName
}

func TestFileSourceServesTheCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.example")

	src, err := NewFileSource(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	cert, err := src.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got := commonNameOf(t, cert); got != "first.example" {
		t.Errorf("common name = %q", got)
	}
}

func TestMissingFilesFailAtStartup(t *testing.T) {
	// Not at the first connection, which is when nobody is watching the logs.
	if _, err := NewFileSource("/nope/cert.pem", "/nope/key.pem", nil); err == nil {
		t.Error("missing files were accepted")
	}
	if _, err := NewFileSource("", "", nil); err == nil {
		t.Error("empty paths were accepted")
	}
}

func TestReloadPicksUpARenewal(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.example")
	src, err := NewFileSource(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	// A renewal writes over the same paths. Picking it up without a restart is
	// the whole reason this watches at all.
	newCert, newKey := writePair(t, dir, "second.example")
	copyOver(t, newCert, certPath)
	copyOver(t, newKey, keyPath)

	if err := src.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	cert, err := src.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got := commonNameOf(t, cert); got != "second.example" {
		t.Errorf("common name after renewal = %q, want second.example", got)
	}
}

func TestABrokenReloadKeepsTheOldPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.example")
	src, err := NewFileSource(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	// A renewal that writes the certificate and the key a moment apart leaves
	// a mismatched pair on disk. Serving the old one through that window is
	// far better than serving nothing.
	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := src.reload(); err == nil {
		t.Error("a broken pair was accepted")
	}
	cert, err := src.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate after a broken reload: %v", err)
	}
	if got := commonNameOf(t, cert); got != "first.example" {
		t.Errorf("common name = %q, want the previous certificate", got)
	}
}

func copyOver(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Ensure the modification time actually differs on filesystems with a
	// coarse clock, or the reload check would see no change.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(to, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestACMENeedsADomain(t *testing.T) {
	_, err := NewACME(context.Background(), ACMEOptions{Provider: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Errorf("err = %v, want a complaint about the domain", err)
	}
}

func TestACMEExplainsAMissingToken(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CF_API_TOKEN", "")
	// The failure a user is most likely to hit, so it has to name the variable
	// rather than surface a provider error from three layers down.
	_, err := NewACME(context.Background(), ACMEOptions{
		Domain: "panel.example.com", Provider: "cloudflare",
	})
	if !errors.Is(err, ErrNoDNSToken) {
		t.Errorf("err = %v, want ErrNoDNSToken", err)
	}
}

func TestACMERejectsAnUnknownProvider(t *testing.T) {
	_, err := NewACME(context.Background(), ACMEOptions{
		Domain: "panel.example.com", Provider: "route53",
	})
	if err == nil || !strings.Contains(err.Error(), "route53") {
		t.Errorf("err = %v, want it to name the provider", err)
	}
}

func TestACMERefusesHTTPOnlyChallenge(t *testing.T) {
	// HTTP-01 needs port 80, which this panel does not expect to have. Saying
	// so beats a certificate request that fails after a minute of retries.
	_, err := NewACME(context.Background(), ACMEOptions{Domain: "panel.example.com"})
	if err == nil || !strings.Contains(err.Error(), "DNS provider") {
		t.Errorf("err = %v, want it to ask for a DNS provider", err)
	}
}

// A renewal that keeps the old timestamps still has to be noticed.
//
// This used to compare modification times, which is wrong for the one event it
// exists to catch: `cp -p`, `install -p`, rsync with --times and anything else
// that restores mtime after writing would leave the panel serving the previous
// certificate until it expired — and then serving an expired one, silently,
// because nothing would ever look again.
func TestRenewalWithPreservedTimestampsIsNoticed(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.example")

	certInfo, err := os.Stat(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	src, err := NewFileSource(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	newCert, newKey := writePair(t, dir, "second.example")
	copyOver(t, newCert, certPath)
	copyOver(t, newKey, keyPath)
	// The part that matters: put the timestamps back exactly as they were, the
	// way a preserving copy does.
	if err := os.Chtimes(certPath, certInfo.ModTime(), certInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, keyInfo.ModTime(), keyInfo.ModTime()); err != nil {
		t.Fatal(err)
	}

	if err := src.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	cert, err := src.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got := commonNameOf(t, cert); got != "second.example" {
		t.Errorf("common name after a timestamp-preserving renewal = %q, want second.example; "+
			"the panel is still serving the certificate that was replaced", got)
	}
}

// An expiry warning must not silence the expiry itself.
//
// Seen to fail: collapsing the two flags back into one `warned bool` makes
// this report that the certificate expired and the panel said nothing. A test
// that has not been seen to fail is a decoration.
//
// The bug: one flag gated both messages and was reset only when a new pair
// loaded. A certificate warned about at fourteen days and then never renewed —
// which is exactly "a panel serving a certificate nobody renewed", the case
// this function exists for — passed its expiry in silence.
//
// Nothing in this package covered warnIfExpiring at all: every other test
// passes a nil logger, so the body returns on its first line.
//
// One thing to watch under -race, flagged because it could not be run here:
// the line below writes cert.Leaf.NotAfter without holding f.mu, while
// NewFileSource has already started the watcher. reloadInterval is a minute so
// the tick should never land inside this test, but if the detector ever
// complains about that field, this is why — take the lock, or stop the watcher
// before touching it.
func TestAnExpiryWarningDoesNotSilenceTheExpiry(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "expiring.example")

	var logged bytes.Buffer
	src, err := NewFileSource(certPath, keyPath, slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	// writePair issues for a day, which is inside the fourteen-day window, so
	// loading it should already have said so once.
	if !strings.Contains(logged.String(), "about to expire") {
		t.Fatalf("a certificate with a day left said nothing:\n%s", logged.String())
	}
	logged.Reset()

	// Time passing, without a renewal — the file never changes, so nothing
	// reloads and nothing resets the flags. TestExpiresAtReportsTheLeaf
	// establishes that Leaf is populated.
	src.cert.Leaf.NotAfter = time.Now().Add(-time.Hour)
	src.warnIfExpiring()

	if !strings.Contains(logged.String(), "has expired") {
		t.Errorf("the certificate expired and the panel said nothing:\n%s\n"+
			"every browser now refuses it and the log does not say why", logged.String())
	}
}

// The expiry is readable, so something other than a log line can act on it.
func TestExpiresAtReportsTheLeaf(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "dated.example")
	src, err := NewFileSource(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	at := src.ExpiresAt()
	if at.IsZero() {
		t.Fatal("ExpiresAt is zero; the leaf was not parsed, so nothing can warn about expiry")
	}
	// writePair issues for a day.
	if d := time.Until(at); d < 20*time.Hour || d > 26*time.Hour {
		t.Errorf("expires in %v, want about 24h", d)
	}
}
