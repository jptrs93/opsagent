package secondary

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

type EnrollmentConfig struct {
	PrimaryEnrollmentAddr        string
	PrimaryEnrollmentFingerprint string
	DataDir                      string
	ClusterCAPath                string
	ClusterCertPath              string
	ClusterKeyPath               string
	OpendeployVersion            string
	UnderlayAddress              string
}

func Enroll(ctx context.Context, cfg EnrollmentConfig) error {
	if strings.TrimSpace(cfg.PrimaryEnrollmentAddr) == "" {
		return fmt.Errorf("primary enrollment address is empty")
	}
	machineID, err := ensureRequestingMachineID(cfg.DataDir)
	if err != nil {
		return err
	}
	client, err := enrollmentHTTPClient(cfg.PrimaryEnrollmentFingerprint)
	if err != nil {
		return err
	}
	capi := apigen.NewEnrollmentV1Capi(enrollmentBaseURL(cfg.PrimaryEnrollmentAddr), apigen.WithEnrollmentV1CapiHTTPClient(client))

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		connectedAt := time.Now()
		err := runEnrollmentSession(ctx, capi, machineID, cfg)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second
		}
		slog.Warn("worker enrollment disconnected; reconnecting",
			"addr", cfg.PrimaryEnrollmentAddr,
			"requestingMachineID", machineID,
			"connected_for", time.Since(connectedAt).Round(time.Second),
			"retry_in", backoff,
			"err", err)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func runEnrollmentSession(ctx context.Context, capi *apigen.EnrollmentV1Capi, machineID string, cfg EnrollmentConfig) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	csrPEM, keyPEM, err := certu.GenerateWorkerCertificateRequest(machineID)
	if err != nil {
		return err
	}

	reqs := func(yield func(*apigen.EnrollmentWorkerMsg, error) bool) {
		if !yield(&apigen.EnrollmentWorkerMsg{Hello: &apigen.EnrollmentHello{RequestingMachineID: machineID, WorkerCertificateRequest: csrPEM, OpendeployVersion: strings.TrimSpace(cfg.OpendeployVersion), UnderlayAddress: cfg.UnderlayAddress}}, nil) {
			return
		}
		<-ctx.Done()
	}

	for msg, err := range capi.PostV1EnrollmentRequest(ctx, reqs) {
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		if msg.RequestStatus != nil {
			slog.Info("worker enrollment request registered", "id", msg.RequestStatus.ID, "status", msg.RequestStatus.Status)
		}
		if msg.Accepted != nil {
			if err := cacheEnrollmentBootstrapState(cfg, msg.Accepted); err != nil {
				return err
			}
			if err := writeEnrollmentTLSBundle(cfg, msg.Accepted, keyPEM); err != nil {
				return err
			}
			slog.Info("worker enrollment accepted", "id", msg.Accepted.ID, "machine", msg.Accepted.WorkerName)
			return nil
		}
	}
	return fmt.Errorf("enrollment stream ended before acceptance")
}

func cacheEnrollmentBootstrapState(cfg EnrollmentConfig, accepted *apigen.EnrollmentAccepted) error {
	if accepted == nil {
		return fmt.Errorf("accepted enrollment response is missing")
	}
	info := accepted.ClusterNetwork
	if info == nil || len(info.UlaPrefix) == 0 {
		return fmt.Errorf("accepted enrollment response missing cluster network")
	}
	if accepted.NodeDeployment == nil || accepted.NodeDeployment.Config.ID == 0 {
		return fmt.Errorf("accepted enrollment response missing node deployment")
	}
	if accepted.NodeNetDeployment == nil || accepted.NodeNetDeployment.Config.ID == 0 {
		return fmt.Errorf("accepted enrollment response missing node net deployment")
	}
	store := state.Open(filepath.Join(cfg.DataDir, "secondary.db"))
	defer store.Close()
	prefix, err := network.ParsePrefix(info.UlaPrefix)
	if err != nil {
		return fmt.Errorf("parsing enrollment cluster network: %w", err)
	}
	if accepted.ClusterNetMap != nil {
		nodeID := accepted.NodeNetDeployment.Instance.NodeID
		if _, _, err := validateClusterNetMap(accepted.ClusterNetMap, nodeID, prefix); err != nil {
			return fmt.Errorf("validating enrollment cluster network map: %w", err)
		}
	}
	store.MustSetLocalKV(storage.LocalKVClusterNetwork, info.Encode())
	cacheEnrollmentInstance(store, accepted.NodeDeployment)
	cacheEnrollmentInstance(store, accepted.NodeNetDeployment)
	if accepted.ClusterNetMap != nil {
		nodeID := accepted.NodeNetDeployment.Instance.NodeID
		if _, err := acceptClusterNetMap(store, accepted.ClusterNetMap, nodeID, prefix); err != nil {
			return fmt.Errorf("accepting enrollment cluster network map: %w", err)
		}
	}
	return nil
}

func cacheEnrollmentInstance(store *state.Service, state *apigen.ScheduledInstanceState) {
	if state == nil {
		return
	}
	store.MustWriteScheduledInstanceAssignment(state)
}

func writeEnrollmentTLSBundle(cfg EnrollmentConfig, accepted *apigen.EnrollmentAccepted, keyPEM []byte) error {
	if len(accepted.CaCertificate) == 0 || len(accepted.WorkerCertificate) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("accepted enrollment response missing TLS material")
	}
	for _, path := range []string{cfg.ClusterCAPath, cfg.ClusterCertPath, cfg.ClusterKeyPath} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("cluster TLS output path is empty")
		}
	}
	if err := os.WriteFile(cfg.ClusterCAPath, accepted.CaCertificate, 0o644); err != nil {
		return fmt.Errorf("writing cluster CA: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterCertPath, accepted.WorkerCertificate, 0o644); err != nil {
		return fmt.Errorf("writing worker cert: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterKeyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing worker key: %w", err)
	}
	return nil
}

func enrollmentHTTPClient(expectedFingerprint string) (*http.Client, error) {
	expected, err := certu.ParseSHA256Fingerprint(expectedFingerprint)
	if err != nil {
		return nil, fmt.Errorf("primary enrollment fingerprint: %w", err)
	}
	// The worker has no cluster trust root before enrollment. TLS still prevents
	// passive capture; SPKI pinning authenticates the bootstrap server identity.
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("enrollment TLS server did not present a certificate")
			}
			got := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], expected) != 1 {
				return fmt.Errorf("enrollment TLS fingerprint mismatch: got %s, want %s", certu.FormatSHA256Fingerprint(got[:]), certu.FormatSHA256Fingerprint(expected))
			}
			return nil
		},
	}}}, nil
}

func ensureRequestingMachineID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "enrollment-machine-id")
	if b, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(b))
		if id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading enrollment machine id: %w", err)
	}
	id := uuid.NewString()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing enrollment machine id: %w", err)
	}
	return id, nil
}

func enrollmentBaseURL(addr string) string {
	if strings.HasPrefix(addr, "https://") {
		return addr
	}
	if strings.HasPrefix(addr, "http://") {
		return "https://" + strings.TrimPrefix(addr, "http://")
	}
	return "https://" + addr
}
