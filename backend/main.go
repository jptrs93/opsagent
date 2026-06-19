package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"os"

	"github.com/jptrs93/opsagent/backend/acmedebug"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/backup"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/internal/installer"
	"github.com/jptrs93/opsagent/backend/logconsumer"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/secondary"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/version"

	"log/slog"
	"net/http"

	"github.com/jptrs93/opsagent/backend/handler"
	"golang.org/x/crypto/acme/autocert"
)

//go:generate sh -c "cd ../frontend && pnpm install && pnpm run build"
//go:embed web/dist
var fsys embed.FS

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
	case ainit.CommandLogConsumer:
		if err := logconsumer.RunBinaryProcess(os.Args); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
		return
	case ainit.CommandPrimary:
		runPrimary()
		return
	case ainit.CommandSecondary:
		runSecondary()
		return
	default:
		panic(fmt.Sprintf("unsupported command after argument parsing: %s", ainit.Args.Command))
	}

}

func runPrimary() {
	subFS, err := fs.Sub(fsys, "web/dist")
	if err != nil {
		panic(fmt.Sprintf("creating embedded sub fs: %v", err))
	}
	machineName := ainit.StaticConfig.PrimaryName
	slog.Info(fmt.Sprintf("opendeploy starting primary version=%v machine=%v", version.Version, machineName))
	h, err := handler.New(subFS, machineName)
	if err != nil {
		panic(fmt.Sprintf("creating handler: %v", err))
	}
	backup.StartReplication(h.ConfigService)
	cfg := h.Config
	clusterMaterial, err := cluster.BootstrapPrimary(h.Secrets, machineName)
	if err != nil {
		panic(fmt.Sprintf("bootstrapping cluster TLS material: %v", err))
	}

	// Primary cluster and enrollment listeners start for every primary.
	startPrimaryCluster(h, clusterMaterial, cfg)
	startPrimaryEnrollment(h, clusterMaterial, cfg)
	m := apigen.CreateOpsagentHttpV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
	})
	if cfg.WebHTTPOnly {
		httpServer := http.Server{Handler: m, Addr: cfg.WebListen}
		slog.Info("starting http-only server", "addr", httpServer.Addr)
		err := httpServer.ListenAndServe()
		panic(fmt.Sprintf("http-only server ended: %v", err))
	}

	certCacheDir := filepath.Join(ainit.StaticConfig.DataDir, ".certs")
	certManager := &autocert.Manager{
		Prompt:      autocert.AcceptTOS,
		Cache:       autocert.DirCache(certCacheDir),
		HostPolicy:  autocert.HostWhitelist(cfg.AcmeHosts...),
		Email:       cfg.AcmeEmail,
		RenewBefore: 168 * time.Hour,
	}
	tlsConfig := certManager.TLSConfig()
	acmedebug.Enable(certManager)
	// TLS-ALPN-01 ACME challenge runs inside the port 443 listener
	httpsServer := http.Server{
		Handler:   m,
		Addr:      cfg.WebListen,
		TLSConfig: tlsConfig,
	}
	slog.Info("starting https server", "addr", cfg.WebListen)
	err = httpsServer.ListenAndServeTLS("", "")
	panic(fmt.Sprintf("https server ended: %v", err))
}

func runSecondary() {
	cfg := ainit.StaticConfig
	if cfg.PrimaryClusterAddr == "" {
		panic("OPENDEPLOY_PRIMARY_CLUSTER_ADDR must be set when running secondary")
	}
	if cfg.PrimaryEnrollmentAddr == "" {
		panic("OPENDEPLOY_PRIMARY_ENROLLMENT_ADDR must be set when running secondary")
	}

	caPath, certPath, keyPath := workerTLSPaths(cfg.DataDir)
	if !workerTLSMaterialExists(caPath, certPath, keyPath) {
		slog.Info("worker cluster certs missing; starting enrollment", "enrollmentAddr", cfg.PrimaryEnrollmentAddr)
		if err := secondary.Enroll(secondary.EnrollmentConfig{
			PrimaryEnrollmentAddr: cfg.PrimaryEnrollmentAddr,
			DataDir:               cfg.DataDir,
			ClusterCAPath:         caPath,
			ClusterCertPath:       certPath,
			ClusterKeyPath:        keyPath,
			OpendeployVersion:     version.Version,
		}); err != nil {
			panic(fmt.Sprintf("worker enrollment: %v", err))
		}
	}
	tlsCfg := certu.MustLoadTLSConfig(caPath, certPath, keyPath)
	machineName := certu.MustCertLoadCommonName(certPath)
	slog.Info(fmt.Sprintf("opendeploy starting secondary version=%v machine=%v clusterAddr=%v primaryName=%v", version.Version, machineName, cfg.PrimaryClusterAddr, cfg.PrimaryName))
	secondary.Run(secondary.Config{
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

func startPrimaryCluster(h *handler.Handler, material *cluster.Material, cfg ainit.DynamicConfiguration) {
	tlsCfg := certu.MustLoadTLSConfigFromPEM(material.CACert, material.PrimaryCert, material.PrimaryKey)

	p := primary.New(h.Store, h.Assets, h.Github, h.Secrets)
	h.ClusterPrimary = p

	// The cluster transport is a separate mTLS HTTP/2-only listener, distinct
	// mux; peer identity comes from the client cert CN. The server emits its own
	// health-check PINGs (HTTP2Config) so it detects a dead worker.
	clusterMux := apigen.CreateOpsagentClusterV1Mux(p, &apigen.MuxConfig{
		VerifyAuth:         primary.VerifyClusterPeer,
		MaxRequestBodySize: 16 * 1024 * 1024, // 16 MB cap on a single inbound stream frame
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	srv := &http.Server{
		Addr:      cfg.ClusterListen,
		Handler:   clusterMux,
		TLSConfig: tlsCfg,
		Protocols: protocols,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 5 * time.Second,  // PING a silent worker after 5s idle
			PingTimeout:     10 * time.Second, // tear down if no ACK within 10s (~15s total to detect a dead worker)
		},
	}
	slog.Info("starting primary cluster", "addr", cfg.ClusterListen)
	go func() {
		err := srv.ListenAndServeTLS("", "")
		panic(fmt.Sprintf("cluster server ended: %v", err))
	}()
}

func startPrimaryEnrollment(h *handler.Handler, material *cluster.Material, cfg ainit.DynamicConfiguration) {
	mux := apigen.CreateEnrollmentV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyEnrollmentRequest,
		MaxRequestBodySize: 1 * 1024 * 1024,
		Middlewares: []apigen.MiddlewareFunc{
			func(next apigen.HandlerFunc) apigen.HandlerFunc {
				return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
					_ = http.NewResponseController(w).EnableFullDuplex()
					next(ctx, w, r)
				}
			},
		},
	})
	srv := &http.Server{
		Addr:      cfg.EnrollmentListen,
		Handler:   mux,
		TLSConfig: certu.MustLoadServerTLSConfigFromPEM(material.PrimaryCert, material.PrimaryKey),
	}
	slog.Info("starting primary enrollment", "addr", cfg.EnrollmentListen)
	go func() {
		err := srv.ListenAndServeTLS("", "")
		panic(fmt.Sprintf("enrollment server ended: %v", err))
	}()
}
