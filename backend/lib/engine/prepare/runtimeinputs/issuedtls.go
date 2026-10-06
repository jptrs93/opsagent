package runtimeinputs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type IssuedTLSValue struct {
	CertPEM     []byte    `json:"cert_pem"`
	KeyPEM      []byte    `json:"key_pem"`
	CACertPEM   []byte    `json:"ca_cert_pem"`
	IssuedAt    time.Time `json:"issued_at"`
	NotAfter    time.Time `json:"not_after"`
	SpecVersion uint32    `json:"spec_version"`
}

type IssuedTLSProvider interface {
	FetchIssuedTLS(ctx context.Context, deploymentID uint64, specVersion uint32) (*IssuedTLSValue, error)
}

type IssuedTLSPersistence interface {
	LoadIssuedTLS() (map[uint64]*IssuedTLSValue, error)
	StoreIssuedTLS(map[uint64]*IssuedTLSValue) error
	RetainIssuedTLS(keep map[uint64]struct{}) (int, error)
}

func IssuedTLSMountOf(cfg *apigen.DeploymentRecord) *apigen.IssuedTLSMount {
	if cfg == nil {
		return nil
	}
	container := cfg.Deployment.Spec.Container()
	if container == nil || !container.Runtime.IssuedTlsMount.Present {
		return nil
	}
	return &container.Runtime.IssuedTlsMount.Value
}

func (r *RuntimeInputs) SetIssuedTLSProvider(p IssuedTLSProvider) {
	r.issuedTLS = p
}

func (r *RuntimeInputs) EnsureIssuedTLSReady(ctx context.Context, cfg *apigen.DeploymentRecord) error {
	mount := IssuedTLSMountOf(cfg)
	if mount == nil {
		return nil
	}
	r.mu.RLock()
	held := r.issuedTLSValues[cfg.Deployment.ID]
	r.mu.RUnlock()
	if held != nil && held.SpecVersion == cfg.Meta.SpecVersion {
		return nil
	}
	if r.issuedTLS == nil {
		if held != nil {
			return nil
		}
		return fmt.Errorf("no issued TLS provider configured")
	}
	value, err := r.issuedTLS.FetchIssuedTLS(ctx, cfg.Deployment.ID, cfg.Meta.SpecVersion)
	if err != nil {
		if held != nil && time.Now().Before(held.NotAfter) {
			return nil
		}
		return fmt.Errorf("fetching issued TLS: %w", err)
	}
	if len(value.CACertPEM) == 0 {
		return fmt.Errorf("issued TLS provider returned empty CA material")
	}
	if !mount.CaOnly && (len(value.CertPEM) == 0 || len(value.KeyPEM) == 0) {
		return fmt.Errorf("issued TLS provider returned empty material")
	}
	r.mu.Lock()
	r.issuedTLSValues[cfg.Deployment.ID] = value
	r.mu.Unlock()
	r.persistIssuedTLS(ctx, map[uint64]*IssuedTLSValue{cfg.Deployment.ID: value})
	return nil
}

func (r *RuntimeInputs) ResolveIssuedTLS(deploymentID uint64) (*IssuedTLSValue, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.issuedTLSValues[deploymentID]
	return value, ok
}

func (r *RuntimeInputs) RetainIssuedTLS(keep map[uint64]struct{}) (int, error) {
	r.mu.Lock()
	for id := range r.issuedTLSValues {
		if _, ok := keep[id]; !ok {
			delete(r.issuedTLSValues, id)
		}
	}
	r.mu.Unlock()
	p, ok := r.persistence.(IssuedTLSPersistence)
	if !ok {
		return 0, nil
	}
	return p.RetainIssuedTLS(keep)
}

func IssuedTLSRoot() string {
	return filepath.Join(ainit.StaticConfig.DataDir, "issued-tls")
}

func IssuedTLSHostDir(deploymentID uint64) string {
	return filepath.Join(IssuedTLSRoot(), strconv.FormatUint(deploymentID, 10))
}

func RetainIssuedTLSDirs(keep map[uint64]struct{}) (int, error) {
	entries, err := os.ReadDir(IssuedTLSRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("listing issued TLS dirs: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		id, err := strconv.ParseUint(entry.Name(), 10, 64)
		if err != nil || id == 0 {
			continue
		}
		if _, keeping := keep[id]; keeping {
			continue
		}
		if err := os.RemoveAll(filepath.Join(IssuedTLSRoot(), entry.Name())); err != nil {
			return removed, fmt.Errorf("removing issued TLS dir %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

func (r *RuntimeInputs) persistIssuedTLS(ctx context.Context, values map[uint64]*IssuedTLSValue) {
	p, ok := r.persistence.(IssuedTLSPersistence)
	if !ok {
		return
	}
	if err := p.StoreIssuedTLS(values); err != nil {
		slog.WarnContext(ctx, "runtimeinputs: persisting issued TLS locally failed; it will be refetched after a restart", "err", err)
	}
}
