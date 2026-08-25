// Package tlsmgr provides the server's TLS configuration.
//
// Two ways in. Certificate files, reloaded when they change so an external
// renewal does not need a restart; or ACME with a DNS-01 challenge, because
// the panel is expected on a non-standard port and HTTP-01 requires port 80.
package tlsmgr

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// reloadInterval is how often certificate files are re-checked.
//
// Polling rather than watching: a renewal happens a handful of times a year,
// the check is two stat calls, and a file watcher brings its own failure modes
// on the network filesystems people put certificates on.
const reloadInterval = time.Minute

// FileSource serves a certificate from disk and reloads it when it changes.
type FileSource struct {
	CertPath string
	KeyPath  string
	Log      *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate
	// digest is over the bytes of both files. See reload for why not mtime.
	digest   [32]byte
	warned   bool
	stopOnce sync.Once
	stop     chan struct{}
}

// NewFileSource loads the pair once so that a bad path fails at startup rather
// than at the first connection.
func NewFileSource(certPath, keyPath string, log *slog.Logger) (*FileSource, error) {
	if certPath == "" || keyPath == "" {
		return nil, errors.New("tlsmgr: both a certificate and a key are required")
	}
	f := &FileSource{CertPath: certPath, KeyPath: keyPath, Log: log, stop: make(chan struct{})}
	if err := f.reload(); err != nil {
		return nil, err
	}
	go f.watch()
	return f, nil
}

// TLSConfig returns a configuration that always serves the current pair.
func (f *FileSource) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Resolved per handshake rather than captured once, so a reload takes
		// effect on the next connection instead of the next restart.
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			f.mu.RLock()
			defer f.mu.RUnlock()
			if f.cert == nil {
				return nil, errors.New("tlsmgr: no certificate loaded")
			}
			return f.cert, nil
		},
	}
}

// Close stops the reload loop.
func (f *FileSource) Close() {
	f.stopOnce.Do(func() { close(f.stop) })
}

func (f *FileSource) reload() error {
	// The bytes, not the timestamps.
	//
	// This compared modification times, which is wrong for the one event it
	// exists to catch. A renewal that preserves timestamps — `cp -p`, `install
	// -p`, rsync with --times, anything that restores mtime after writing —
	// leaves the panel serving the old certificate until it expires, and then
	// serving an expired one, silently, because nothing here would ever look
	// again. Hashing two files of a few kilobytes once a minute costs nothing
	// and cannot be fooled by a timestamp.
	sum, err := pairDigest(f.CertPath, f.KeyPath)
	if err != nil {
		return err
	}

	f.mu.RLock()
	unchanged := f.cert != nil && sum == f.digest
	f.mu.RUnlock()
	if unchanged {
		f.warnIfExpiring()
		return nil
	}

	cert, err := tls.LoadX509KeyPair(f.CertPath, f.KeyPath)
	if err != nil {
		// Keep serving the old pair. A renewal that writes the certificate and
		// the key a moment apart would otherwise take the panel down for the
		// length of that gap.
		return fmt.Errorf("tlsmgr: load pair: %w", err)
	}

	f.mu.Lock()
	f.cert, f.digest = &cert, sum
	// A new pair deserves a fresh judgement about whether it is about to run
	// out; otherwise a renewal that fixed the problem would leave the warning
	// suppressed and a renewal that did not would never repeat it.
	f.warned = false
	f.mu.Unlock()
	f.warnIfExpiring()
	return nil
}

// pairDigest is a single hash over both files, so either changing is a change.
func pairDigest(paths ...string) ([32]byte, error) {
	h := sha256.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return [32]byte{}, fmt.Errorf("tlsmgr: %w", err)
		}
		// Length-prefixed, so that moving a byte from one file to the other
		// cannot produce the same hash.
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// ExpiresAt is when the served certificate stops being valid, or the zero time
// if none is loaded.
func (f *FileSource) ExpiresAt() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.cert == nil || f.cert.Leaf == nil {
		return time.Time{}
	}
	return f.cert.Leaf.NotAfter
}

// expiryWarning is how long before the end a certificate is worth mentioning.
//
// Two weeks: an ACME renewal is attempted at thirty days, so anything inside
// this window means renewal has already failed more than once, and there is
// still time to do something about it by hand.
const expiryWarning = 14 * 24 * time.Hour

// warnIfExpiring says so once, rather than every minute forever.
//
// The failure this guards is a panel that has been serving a certificate
// nobody renewed. Detecting the file changing is only half of it: a file that
// never changes is exactly what an abandoned renewal looks like, and the old
// code could not have told anyone either way.
func (f *FileSource) warnIfExpiring() {
	if f.Log == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.warned || f.cert == nil || f.cert.Leaf == nil {
		return
	}
	left := time.Until(f.cert.Leaf.NotAfter)
	if left > expiryWarning {
		return
	}
	f.warned = true
	if left <= 0 {
		f.Log.Error("the certificate being served has expired",
			"expired", f.cert.Leaf.NotAfter, "cert", f.CertPath)
		return
	}
	f.Log.Warn("the certificate being served is about to expire",
		"expires", f.cert.Leaf.NotAfter, "in", left.Round(time.Hour), "cert", f.CertPath)
}

func (f *FileSource) watch() {
	t := time.NewTicker(reloadInterval)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
			if err := f.reload(); err != nil && f.Log != nil {
				f.Log.Warn("certificate reload", "err", err)
			}
		}
	}
}
