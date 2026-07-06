package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"os"
	"os/signal"
	"syscall"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/internal/installer"
	"github.com/jptrs93/opsagent/backend/localtest"
	"github.com/jptrs93/opsagent/backend/logconsumer"
	"github.com/jptrs93/opsagent/backend/middleware/ratelimit"
	"github.com/jptrs93/opsagent/backend/primary/backup"
	"github.com/jptrs93/opsagent/backend/primary/clusterserver"
	"github.com/jptrs93/opsagent/backend/primary/handler"
	"github.com/jptrs93/opsagent/backend/primary/webui"
	"github.com/jptrs93/opsagent/backend/secondary"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/version"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"log/slog"
)

//go:generate sh -c "cd ../frontend && pnpm install && pnpm run build"
//go:embed web/dist
var fsys embed.FS

var errPrimaryRestartRequired = errors.New("primary restart required")

// Storage failure policy
//
// OpenDeploy treats any failure of the on-disk SQLite store as an unrecoverable
// broken state. Outside the auth helpers (where ErrNotFound is a legitimate
// "unknown user / unknown kid" signal), all DB calls go through the Must*
// variants on the storage adapter, which panic on error. We rely on the
// supervisor (systemd / launchd / equivalent) to restart the process; the
// in-memory state is rebuilt from the database on startup.
//
// Practical rules for new code:
//   - Writes: always Must* — there is no sensible recovery from a write failure.
//   - Reads where the key is an internal invariant (e.g. tail-loop polling for
//     a deployment we just fetched): use Must*.
//   - Reads driven by user input where "not found" is an expected outcome
//     (auth lookups, login flows): use the non-Must variant and translate
//     ErrNotFound to an ApiErr.
//
// See docs/engineering/engine.md for the rationale.

func main() {
	switch ainit.Args.Command {
	case ainit.CommandInstall, ainit.CommandUninstall:
		if err := installer.Run(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
		return
	case ainit.CommandSplitLogConsumer:
		if err := logconsumer.RunSplitProcess(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
		return
	case ainit.CommandRawLogConsumer:
		if err := logconsumer.RunRawBinaryProcess(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
		return
	case ainit.CommandPrimary:
		if err := runPrimary(); err != nil {
			if errors.Is(err, errPrimaryRestartRequired) {
				slog.Info("opendeploy primary stopped for restart", "err", err)
			} else {
				slog.Error("opendeploy primary stopped with error", "err", err)
			}
			os.Exit(1)
		}
		return
	case ainit.CommandSecondary:
		runSecondary()
		return
	default:
		panic(fmt.Sprintf("unsupported command after argument parsing: %s", ainit.Args.Command))
	}

}

func runPrimary() error {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g, ctx := errgroup.WithContext(signalCtx)
	subFS, err := fs.Sub(fsys, "web/dist")
	if err != nil {
		return fmt.Errorf("creating embedded sub fs: %w", err)
	}
	machineName := ainit.StaticConfig.PrimaryName
	slog.Info(fmt.Sprintf("opendeploy starting primary version=%v machine=%v", version.Version, machineName))
	h, err := handler.New(ctx, subFS, machineName)
	if err != nil {
		return fmt.Errorf("creating handler: %w", err)
	}
	backupDone := backup.StartReplication(ctx, h.ConfigService, h.Secrets)
	defer func() {
		stop()
		<-backupDone
	}()
	initialConfig := h.ConfigService.Snapshot()
	clusterMaterial, err := cluster.BootstrapPrimary(h.Secrets, machineName)
	if err != nil {
		return fmt.Errorf("bootstrapping cluster TLS material: %w", err)
	}
	enrollmentFingerprint, err := certu.CertificatePEMSPKISHA256(clusterMaterial.PrimaryCert)
	if err != nil {
		return fmt.Errorf("computing enrollment TLS fingerprint: %w", err)
	}
	h.EnrollmentTLSFingerprint = enrollmentFingerprint

	// Primary cluster and enrollment listeners start for every primary.
	clusterPrimary := clusterserver.New(h.Store, h.Assets, h.Github, h.Secrets)
	h.ClusterPrimary = clusterPrimary
	enrollmentMiddlewares := []apigen.MiddlewareFunc{}
	if !localtest.Enabled() {
		enrollmentMiddlewares = append(enrollmentMiddlewares,
			ratelimit.PerIP(rate.Limit(0.2), 5, time.Minute),
		)
	}
	g.Go(func() error {
		return clusterserver.RunPrimary(ctx, clusterPrimary, h.ConfigService, clusterMaterial, initialConfig.Settings.Cluster.Listen)
	})
	g.Go(func() error {
		return clusterserver.RunEnrollment(ctx, h, h.VerifyEnrollmentRequest, h.ConfigService, clusterMaterial, initialConfig.Settings.Cluster.EnrollmentListen, enrollmentMiddlewares...)
	})
	middlewares := []apigen.MiddlewareFunc{}
	if !localtest.Enabled() {
		middlewares = append(middlewares,
			ratelimit.PerIP(rate.Limit(40), 100, time.Minute),
			ratelimit.PerIPAndPrefix("/v1/auth", rate.Limit(1), 10, time.Minute),
			ratelimit.PerIPAndPrefix("/v1/auth/master", rate.Limit(0.2), 10, time.Minute),
		)
	}
	m := apigen.CreateOpsagentHttpV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
		Middlewares:        middlewares,
	})
	g.Go(func() error { return webui.RunPrimaryHTTPWebUI(ctx, h.ConfigService, m) })
	g.Go(func() error { return webui.RunPrimaryHTTPSWebUI(ctx, h.ConfigService, h.Secrets, m) })
	g.Go(func() error { return watchPrimaryServerConfig(ctx, h.ConfigService, initialConfig) })
	err1 := g.Wait()
	err2 := signalCtx.Err()
	return errors.Join(err1, err2)
}

func watchPrimaryServerConfig(ctx context.Context, cs *config.Service, initial apigen.Config) error {
	sub := cs.SnapshotAndSubscribe(primaryServerConfigChanged)
	defer sub.UnsubscribeFunc()
	if primaryServerConfigChanged(initial, sub.InitialValue) {
		slog.Info("primary server config changed; restarting")
		return errPrimaryRestartRequired
	}
	select {
	case <-ctx.Done():
		return nil
	case _, ok := <-sub.Ch:
		if !ok {
			return nil
		}
		slog.Info("primary server config changed; restarting")
		return errPrimaryRestartRequired
	}
}

func primaryServerConfigChanged(prev, next apigen.Config) bool {
	return prev.Settings.HttpWeb != next.Settings.HttpWeb ||
		prev.Settings.HttpsWeb != next.Settings.HttpsWeb ||
		prev.Settings.Cluster != next.Settings.Cluster
}

func runSecondary() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := ainit.StaticConfig
	if cfg.PrimaryClusterAddr == "" || cfg.PrimaryEnrollmentAddr == "" {
		panic("OPENDEPLOY_PRIMARY_CLUSTER_ADDR and OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR must be set when running secondary")
	}
	caPath, certPath, keyPath := workerTLSPaths(cfg.DataDir)
	if !workerTLSMaterialExists(caPath, certPath, keyPath) {
		if cfg.PrimaryEnrollmentFingerprint == "" {
			panic("OPENDEPLOY_PRIMARY_ENROLLMENT_FINGERPRINT must be set before worker enrollment")
		}
		slog.Info(fmt.Sprintf("worker cluster certs missing; starting enrollment enrollmentAddr=%v", cfg.PrimaryEnrollmentAddr))
		if err := secondary.Enroll(secondary.EnrollmentConfig{
			PrimaryEnrollmentAddr:        cfg.PrimaryEnrollmentAddr,
			PrimaryEnrollmentFingerprint: cfg.PrimaryEnrollmentFingerprint,
			DataDir:                      cfg.DataDir,
			ClusterCAPath:                caPath,
			ClusterCertPath:              certPath,
			ClusterKeyPath:               keyPath,
			OpendeployVersion:            version.Version,
		}); err != nil {
			panic(fmt.Sprintf("worker enrollment: %v", err))
		}
	}
	tlsCfg := certu.MustLoadTLSConfig(caPath, certPath, keyPath)
	machineName := certu.MustCertLoadCommonName(certPath)
	slog.Info(fmt.Sprintf("opendeploy starting secondary version=%v machine=%v clusterAddr=%v primaryName=%v", version.Version, machineName, cfg.PrimaryClusterAddr, cfg.PrimaryName))
	secondary.Run(ctx, secondary.Config{
		TLS:                tlsCfg,
		PrimaryClusterAddr: cfg.PrimaryClusterAddr,
		PrimaryName:        cfg.PrimaryName,
		MachineName:        machineName,
		DataDir:            cfg.DataDir,
	})
}

func workerTLSPaths(dataDir string) (caPath, certPath, keyPath string) {
	tlsDir := filepath.Join(dataDir, "tls")
	return filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "node.crt"), filepath.Join(tlsDir, "node.key")
}

func workerTLSMaterialExists(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
