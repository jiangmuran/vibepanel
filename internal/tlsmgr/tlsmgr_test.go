package tlsmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
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
