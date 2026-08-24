// Package tlsmgr provides the server's TLS configuration.
//
// Two ways in. Certificate files, reloaded when they change so an external
// renewal does not need a restart; or ACME with a DNS-01 challenge, because
// the panel is expected on a non-standard port and HTTP-01 requires port 80.
package tlsmgr

import (
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

	mu       sync.RWMutex
	cert     *tls.Certificate
	certMod  time.Time
	keyMod   time.Time
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
	certInfo, err := os.Stat(f.CertPath)
	if err != nil {
		return fmt.Errorf("tlsmgr: certificate: %w", err)
	}
	keyInfo, err := os.Stat(f.KeyPath)
	if err != nil {
		return fmt.Errorf("tlsmgr: key: %w", err)
	}

	f.mu.RLock()
	unchanged := f.cert != nil &&
		certInfo.ModTime().Equal(f.certMod) && keyInfo.ModTime().Equal(f.keyMod)
	f.mu.RUnlock()
	if unchanged {
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
	f.cert, f.certMod, f.keyMod = &cert, certInfo.ModTime(), keyInfo.ModTime()
	f.mu.Unlock()
	return nil
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
