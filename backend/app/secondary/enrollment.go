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

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/enrollment"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
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
	ctx = logu.AddTag(ctx, "Enrollment")
	if strings.TrimSpace(cfg.PrimaryEnrollmentAddr) == "" {
		return fmt.Errorf("primary enrollment address is empty")
	}
	// The WireGuard keypair is minted before the first enrollment attempt so
	// the public key rides the same mTLS-pinned channel as the CSR; the
	// private key never leaves cfg.DataDir.
	nodeKey, err := wgkey.LoadOrGenerate(cfg.DataDir)
	if err != nil {
		return err
	}
	clusterKeyPEM, err := loadOrGenerateClusterKey(cfg.ClusterKeyPath)
	if err != nil {
		return err
	}
	machineID, csrPEM, err := certu.SecondaryIdentity(clusterKeyPEM)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, fmt.Sprintf("secondary enrollment identity ready requestingMachineID=%s keyPath=%s", machineID, cfg.ClusterKeyPath))
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
		err := runEnrollmentSession(ctx, capi, machineID, nodeKey.PublicBase64(), csrPEM, clusterKeyPEM, cfg)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if enrollment.Rejected(err) {
			return fmt.Errorf("primary rejected enrollment requestingMachineID=%s: %w", machineID, err)
		}
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second
		}
		slog.WarnContext(ctx, fmt.Sprintf("secondary enrollment disconnected; reconnecting addr=%s requestingMachineID=%s connected_for=%s retry_in=%s",
			cfg.PrimaryEnrollmentAddr, machineID, time.Since(connectedAt).Round(time.Second), backoff), "err", err)
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

func runEnrollmentSession(ctx context.Context, capi *apigen.EnrollmentV1Capi, machineID, wgPublicKey string, csrPEM, keyPEM []byte, cfg EnrollmentConfig) error {
	underlay, err := apigen.ParseAddr(cfg.UnderlayAddress)
	if err != nil {
		return fmt.Errorf("parsing underlay address %q: %w", cfg.UnderlayAddress, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	reqs := func(yield func(*apigen.EnrollmentSecondaryMsg, error) bool) {
		inventory := currentHostAddresses(ctx)
		if !yield(&apigen.EnrollmentSecondaryMsg{Hello: apigen.Some(apigen.EnrollmentHello{Reported: apigen.NodeReported{Identifier: machineID, UnderlayAddress: underlay, WgPublicKey: wgPublicKey, HostAddresses: inventory.wire(), HostAddressesUnknown: inventory.unknown}, SecondaryCertificateRequest: csrPEM, OpendeployVersion: strings.TrimSpace(cfg.OpendeployVersion), ClusterProtocolVersion: apigen.ClusterProtocolVersion})}, nil) {
			return
		}
		<-ctx.Done()
	}

	for msg, err := range capi.PostV1EnrollmentRequest(ctx, reqs) {
		if err != nil {
			return err
		}
		if msg.RequestStatus.Present {
			slog.InfoContext(ctx, fmt.Sprintf("secondary enrollment request registered id=%d status=%v", msg.RequestStatus.Value.ID, msg.RequestStatus.Value.Status))
		}
		if msg.Accepted.Present {
			accepted := &msg.Accepted.Value
			if err := cacheEnrollmentBootstrapState(ctx, cfg, accepted); err != nil {
				return err
			}
			if err := writeEnrollmentTLSBundle(cfg, accepted, keyPEM); err != nil {
				return err
			}
			slog.InfoContext(ctx, fmt.Sprintf("secondary enrollment accepted id=%d machine=%s", accepted.ID, accepted.NodeName))
			return nil
		}
	}
	return fmt.Errorf("enrollment stream ended before acceptance")
}

func cacheEnrollmentBootstrapState(ctx context.Context, cfg EnrollmentConfig, accepted *apigen.EnrollmentAccepted) error {
	snapshot := &accepted.NodeSnapshot
	netMap := &snapshot.NetMap.Value
	nodeID := netMap.TargetNodeID
	prefix, err := network.ParsePrefix(netMap.UlaPrefix)
	if err != nil {
		return fmt.Errorf("parsing enrollment cluster network map prefix: %w", err)
	}
	store := state.Open(filepath.Join(cfg.DataDir, "secondary.db"))
	defer store.Close()
	frame := frameOf(snapshot, true)
	if _, _, err := applyProjection(ctx, store, nodeID, frame, prefix, true, nil, nil); err != nil {
		return fmt.Errorf("accepting enrollment cluster network map: %w", err)
	}
	return nil
}

func writeEnrollmentTLSBundle(cfg EnrollmentConfig, accepted *apigen.EnrollmentAccepted, keyPEM []byte) error {
	if len(accepted.CaCertificate) == 0 || len(accepted.SecondaryCertificate) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("accepted enrollment response missing TLS material")
	}
	if err := os.WriteFile(cfg.ClusterCAPath, accepted.CaCertificate, 0o644); err != nil {
		return fmt.Errorf("writing cluster CA: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterCertPath, accepted.SecondaryCertificate, 0o644); err != nil {
		return fmt.Errorf("writing secondary cert: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterKeyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing secondary key: %w", err)
	}
	return nil
}

func enrollmentHTTPClient(expectedFingerprint string) (*http.Client, error) {
	expected, err := certu.ParseSHA256Fingerprint(expectedFingerprint)
	if err != nil {
		return nil, fmt.Errorf("primary enrollment fingerprint: %w", err)
	}
	// The secondary has no cluster trust root before enrollment. TLS still prevents
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

func loadOrGenerateClusterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("cluster key path is empty")
	}
	keyPEM, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(keyPEM))) > 0 {
		return keyPEM, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	keyPEM, err = certu.GenerateSecondaryKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return keyPEM, nil
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
