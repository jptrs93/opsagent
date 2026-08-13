package netproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type certStore struct {
	path  string
	state atomic.Pointer[certStoreState]
}

type certStoreState struct {
	seq   int64
	certs map[string]*tls.Certificate
}

func newCertStore(path string) *certStore {
	return &certStore{path: filepath.Clean(path)}
}

func (cs *certStore) Get(certID string) (*tls.Certificate, bool) {
	state := cs.state.Load()
	if state == nil {
		return nil, false
	}
	cert, ok := state.certs[certID]
	return cert, ok
}

func (cs *certStore) Run(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating cert bundle watcher: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(cs.path)); err != nil {
		return fmt.Errorf("watching cert bundle directory: %w", err)
	}
	if err := cs.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("loading initial cert bundle failed", "path", cs.path, "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return fmt.Errorf("watching cert bundle: %w", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(event.Name) != cs.path || event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if err := cs.reload(); err != nil {
				slog.Warn("reloading cert bundle failed", "path", cs.path, "err", err)
			}
		}
	}
}

func (cs *certStore) reload() error {
	b, err := os.ReadFile(cs.path)
	if err != nil {
		return err
	}
	bundle, err := apigen.DecodeCertBundle(b)
	if err != nil {
		return err
	}
	current := cs.state.Load()
	if current != nil && bundle.Seq <= current.seq {
		return nil
	}
	next := &certStoreState{seq: bundle.Seq, certs: make(map[string]*tls.Certificate, len(bundle.Certs))}
	for _, entry := range bundle.Certs {
		if entry == nil || entry.CertID == "" {
			continue
		}
		cert, err := tls.X509KeyPair(entry.Pem, entry.Pem)
		if err != nil {
			slog.Warn("parsing cert bundle entry failed", "cert_id", entry.CertID, "err", err)
			continue
		}
		next.certs[entry.CertID] = &cert
	}
	cs.state.Store(next)
	slog.Info("cert bundle loaded", "seq", bundle.Seq, "certs", len(next.certs))
	return nil
}
