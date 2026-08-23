package secondary

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	certRenewWindow        = 90 * 24 * time.Hour
	certRenewCheckInterval = 6 * time.Hour
)

type clusterCertManager struct {
	certPath string
	keyPEM   []byte

	mu       sync.RWMutex
	cert     tls.Certificate
	notAfter time.Time
}

func newClusterCertManager(certPath, keyPath string) (*clusterCertManager, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading cluster cert %q: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading cluster key %q: %w", keyPath, err)
	}
	m := &clusterCertManager{certPath: certPath, keyPEM: keyPEM}
	cert, notAfter, err := m.load(certPEM)
	if err != nil {
		return nil, err
	}
	m.cert = cert
	m.notAfter = notAfter
	return m, nil
}

func (m *clusterCertManager) load(certPEM []byte) (tls.Certificate, time.Time, error) {
	cert, err := tls.X509KeyPair(certPEM, m.keyPEM)
	if err != nil {
		return tls.Certificate{}, time.Time{}, fmt.Errorf("loading cluster cert/key: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return tls.Certificate{}, time.Time{}, fmt.Errorf("cluster cert contains no PEM data")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return tls.Certificate{}, time.Time{}, fmt.Errorf("parsing cluster cert: %w", err)
	}
	return cert, leaf.NotAfter, nil
}

func (m *clusterCertManager) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &m.cert, nil
}

func (m *clusterCertManager) expiry() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.notAfter
}

func (m *clusterCertManager) install(certPEM []byte) (time.Time, error) {
	cert, notAfter, err := m.load(certPEM)
	if err != nil {
		return time.Time{}, err
	}
	tmpPath := m.certPath + ".tmp"
	if err := os.WriteFile(tmpPath, certPEM, 0o644); err != nil {
		return time.Time{}, fmt.Errorf("writing renewed cluster cert: %w", err)
	}
	if err := os.Rename(tmpPath, m.certPath); err != nil {
		return time.Time{}, fmt.Errorf("replacing cluster cert: %w", err)
	}
	m.mu.Lock()
	m.cert = cert
	m.notAfter = notAfter
	m.mu.Unlock()
	return notAfter, nil
}

func runClusterCertRenewal(ctx context.Context, m *clusterCertManager, primaryURL string, client *http.Client) {
	ctx = logu.AddTag(ctx, "CertRenewal")
	capi := apigen.NewOpsagentClusterV1Capi(primaryURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client))
	ticker := time.NewTicker(certRenewCheckInterval)
	defer ticker.Stop()
	for {
		if time.Until(m.expiry()) < certRenewWindow {
			if err := renewClusterCert(ctx, m, capi); err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("renewing worker cluster certificate failed; will retry notAfter=%s", m.expiry()), "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func renewClusterCert(ctx context.Context, m *clusterCertManager, capi *apigen.OpsagentClusterV1Capi) error {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := capi.GetV1ClusterRenewCertificate(reqCtx)
	if err != nil {
		return err
	}
	if len(resp.CertPem) == 0 {
		return fmt.Errorf("primary returned empty certificate")
	}
	notAfter, err := m.install(resp.CertPem)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, fmt.Sprintf("renewed worker cluster certificate notAfter=%s", notAfter))
	return nil
}
