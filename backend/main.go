package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"os"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/backup"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/internal/installer"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/secondary"
	"github.com/jptrs93/opsagent/backend/util/certu"

	"log/slog"
	"net"
	"net/http"

	"github.com/jptrs93/opsagent/backend/handler"
	"golang.org/x/crypto/acme/autocert"
)

// version is set at build time via -ldflags="-X main.version=...".
var version = "dev"

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
	// Installer subcommands run inside the same binary but are a distinct entry
	// point — they provision the host and must not start the server. ainit.init()
	// skips its server bootstrap for these same subcommands (Go runs init()
	// before main(), so the skip lives there, not here).
	if installer.IsSubcommand(os.Args) {
		if err := installer.Run(os.Args); err != nil {
			fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) < 2 {
		usage(os.Args[0])
		os.Exit(2)
	}

	fmt.Println(fmt.Sprintf("opendeploy starting version=%v", version))

	switch os.Args[1] {
	case "primary":
		runPrimary()
		return
	case "secondary":
		runSecondary()
		return
	default:
		usage(os.Args[0])
		fmt.Fprintf(os.Stderr, "\nunknown command: %s\n", os.Args[1])
		os.Exit(2)
	}

}

func usage(prog string) {
	fmt.Fprintf(os.Stderr, `%[1]s - deployment management server

Usage:
  %[1]s primary
  %[1]s secondary
  %[1]s install [--version vX.Y.Z] [--dry-run]
  %[1]s uninstall [--purge] [--yes] [--dry-run]

Commands:
  primary     Run the primary HTTP server and cluster listeners.
  secondary   Run a worker that enrolls with and connects to the primary.
  install     Fresh install or in-place upgrade.
  uninstall   Remove the service and binary; --purge also wipes all state.
`, prog)
}

func runPrimary() {
	backup.MustRestoreAndStartReplicationIfEnabled()

	subFS, err := fs.Sub(fsys, "web/dist")
	if err != nil {
		panic(fmt.Sprintf("creating embedded sub fs: %v", err))
	}
	machineName := ainit.ResolvePrimaryMachineName()
	slog.Info("starting in primary mode", "machine", machineName)
	h, err := handler.New(subFS, machineName)
	if err != nil {
		panic(fmt.Sprintf("creating handler: %v", err))
	}
	clusterMaterial, err := cluster.BootstrapPrimary(h.Secrets, machineName)
	if err != nil {
		panic(fmt.Sprintf("bootstrapping cluster TLS material: %v", err))
	}

	// Primary cluster and enrollment listeners start for every primary.
	startPrimaryCluster(h, clusterMaterial)
	startPrimaryEnrollment(h)
	m := apigen.CreateOpsagentHttpV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
	})
	if ainit.Config.HTTPOnly {
		httpAddr := net.JoinHostPort(ainit.Config.BindAddr, "8080")
		httpServer := http.Server{Handler: m, Addr: httpAddr}
		slog.Info("starting http-only server", "addr", httpServer.Addr)
		err := httpServer.ListenAndServe()
		panic(fmt.Sprintf("http-only server ended: %v", err))
	}

	certManager := &autocert.Manager{
		Prompt:      autocert.AcceptTOS,
		Cache:       autocert.DirCache(filepath.Join(ainit.Config.DataDir, ".certs")),
		HostPolicy:  autocert.HostWhitelist(ainit.Config.AcmeHosts...),
		Email:       ainit.Config.AcmeEmail,
		RenewBefore: 168 * time.Hour,
	}
	// TLS-ALPN-01 ACME challenge runs inside the port 443 listener
	httpsAddr := net.JoinHostPort(ainit.Config.BindAddr, "443")
	httpsServer := http.Server{
		Handler:   m,
		Addr:      httpsAddr,
		TLSConfig: certManager.TLSConfig(),
	}
	slog.Info("starting https server", "addr", httpsAddr)
	err = httpsServer.ListenAndServeTLS("", "")
	panic(fmt.Sprintf("https server ended: %v", err))
}

func runSecondary() {
	cfg := ainit.Config
	if cfg.PrimaryAddr == "" {
		panic("OPENDEPLOY_PRIMARY_ADDR must be set when running secondary")
	}
	caPath, certPath, keyPath := workerTLSPaths(cfg.DataDir)
	if !workerTLSMaterialExists(caPath, certPath, keyPath) {
		slog.Info("worker cluster certs missing; starting enrollment", "primaryAddr", cfg.PrimaryAddr)
		if err := secondary.Enroll(secondary.EnrollmentConfig{
			PrimaryAddr:     cfg.PrimaryAddr,
			DataDir:         cfg.DataDir,
			ClusterCAPath:   caPath,
			ClusterCertPath: certPath,
			ClusterKeyPath:  keyPath,
		}); err != nil {
			panic(fmt.Sprintf("worker enrollment: %v", err))
		}
	}
	tlsCfg := certu.MustLoadTLSConfig(caPath, certPath, keyPath)
	machineName := certu.MustCertLoadCommonName(certPath)

	slog.Info("starting in slave mode", "machine", machineName, "primary", cfg.PrimaryAddr, "primaryName", cfg.PrimaryName)
	secondary.Run(secondary.Config{
		TLS:         tlsCfg,
		PrimaryAddr: cfg.PrimaryAddr,
		PrimaryName: cfg.PrimaryName,
		MachineName: machineName,
		DataDir:     cfg.DataDir,
		GithubToken: cfg.GithubToken,
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

func startPrimaryCluster(h *handler.Handler, material *cluster.Material) {
	cfg := ainit.Config
	tlsCfg := certu.MustLoadTLSConfigFromPEM(material.CACert, material.PrimaryCert, material.PrimaryKey)

	p := primary.New(h.Store)
	h.ClusterPrimary = p

	// The cluster transport is a separate mTLS HTTP/2-only listener, distinct
	// mux; peer identity comes from the client cert CN. The server emits its own
	// health-check PINGs (HTTP2Config) so it detects a dead worker.
	mux := apigen.CreateOpsagentClusterV1Mux(p, &apigen.MuxConfig{
		VerifyAuth:         primary.VerifyClusterPeer,
		MaxRequestBodySize: 16 * 1024 * 1024, // 16 MB cap on a single inbound stream frame
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	srv := &http.Server{
		Addr:      cfg.ClusterListen,
		Handler:   mux,
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

func startPrimaryEnrollment(h *handler.Handler) {
	cfg := ainit.Config
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
		Addr:    cfg.EnrollmentListen,
		Handler: mux,
	}
	slog.Info("starting primary enrollment", "addr", cfg.EnrollmentListen)
	go func() {
		err := srv.ListenAndServe()
		panic(fmt.Sprintf("enrollment server ended: %v", err))
	}()
}
