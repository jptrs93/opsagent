package secondary

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type EnrollmentConfig struct {
	PrimaryAddr     string
	DataDir         string
	ClusterCAPath   string
	ClusterCertPath string
	ClusterKeyPath  string
}

func Enroll(cfg EnrollmentConfig) error {
	if strings.TrimSpace(cfg.PrimaryAddr) == "" {
		return fmt.Errorf("primary address is empty")
	}
	machineID, err := ensureRequestingMachineID(cfg.DataDir)
	if err != nil {
		return err
	}
	capi := apigen.NewEnrollmentV1Capi(enrollmentBaseURL(cfg.PrimaryAddr))

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		connectedAt := time.Now()
		err := runEnrollmentSession(capi, machineID, cfg)
		if err == nil {
			return nil
		}
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second
		}
		slog.Warn("worker enrollment disconnected; reconnecting",
			"addr", cfg.PrimaryAddr,
			"requestingMachineID", machineID,
			"connected_for", time.Since(connectedAt).Round(time.Second),
			"retry_in", backoff,
			"err", err)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func runEnrollmentSession(capi *apigen.EnrollmentV1Capi, machineID string, cfg EnrollmentConfig) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reqs := func(yield func(*apigen.EnrollmentWorkerMsg, error) bool) {
		if !yield(&apigen.EnrollmentWorkerMsg{Hello: &apigen.EnrollmentHello{RequestingMachineID: machineID}}, nil) {
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
			if err := writeEnrollmentTLSBundle(cfg, msg.Accepted); err != nil {
				return err
			}
			slog.Info("worker enrollment accepted", "id", msg.Accepted.ID, "machine", msg.Accepted.WorkerName)
			return nil
		}
	}
	return fmt.Errorf("enrollment stream ended before acceptance")
}

func writeEnrollmentTLSBundle(cfg EnrollmentConfig, accepted *apigen.EnrollmentAccepted) error {
	if len(accepted.CaCertificate) == 0 || len(accepted.WorkerCertificate) == 0 || len(accepted.WorkerPrivateKey) == 0 {
		return fmt.Errorf("accepted enrollment response missing TLS material")
	}
	for _, path := range []string{cfg.ClusterCAPath, cfg.ClusterCertPath, cfg.ClusterKeyPath} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("cluster TLS output path is empty")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("creating TLS dir %q: %w", filepath.Dir(path), err)
		}
	}
	if err := os.WriteFile(cfg.ClusterCAPath, accepted.CaCertificate, 0o644); err != nil {
		return fmt.Errorf("writing cluster CA: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterCertPath, accepted.WorkerCertificate, 0o644); err != nil {
		return fmt.Errorf("writing worker cert: %w", err)
	}
	if err := os.WriteFile(cfg.ClusterKeyPath, accepted.WorkerPrivateKey, 0o600); err != nil {
		return fmt.Errorf("writing worker key: %w", err)
	}
	return nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating data dir for enrollment machine id: %w", err)
	}
	id := uuid.NewString()
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing enrollment machine id: %w", err)
	}
	return id, nil
}

func enrollmentBaseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
