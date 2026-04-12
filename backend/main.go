package main

import (
	"context"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/slave"

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
// Opsagent treats any failure of the on-disk log stores as an unrecoverable
// broken state. Outside the auth helpers (where ErrNotFound is a legitimate
// "unknown user / unknown kid" signal), all DB calls go through the Must*
// variants on logstore, which panic on error. We rely on the supervisor
// (systemd / launchd / equivalent) to restart the process; the in-memory
// state is rebuilt from the append-only log on startup.
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
	fmt.Println(fmt.Sprintf("opsagent starting version=%v", version))

	// Slave mode: if OPSAGENT_PRIMARY_ADDR is set, this node is a worker.
	// It connects to the primary, receives state, and runs operators — no
	// local storage, no HTTP server.
	if ainit.Config.PrimaryAddr != "" {
		runSlave()
		return
	}

	subFS, err := fs.Sub(fsys, "web/dist")
	if err != nil {
		panic(fmt.Sprintf("creating embedded sub fs: %v", err))
	}
	machineName := resolvePrimaryMachineName()
	slog.Info("starting in primary mode", "machine", machineName)
	h, err := handler.New(subFS, machineName)
	if err != nil {
		panic(fmt.Sprintf("creating handler: %v", err))
	}

	// Primary cluster listener: if cluster TLS is configured, start the
	// mTLS listener alongside the public HTTP server.
	if ainit.Config.ClusterCA != "" {
		startPrimaryCluster(h)
	}
	opt := &apigen.MuxOptions{MaxRequestBodySize: ptru.To(20_000_000)}
	m := apigen.CreateMux(h, h.VerifyAuth, opt, func(next apigen.HandlerFunc) apigen.HandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
			slog.Info(fmt.Sprintf("req: %v %v", r.Method, r.URL.Path))
			next(ctx, w, r)
		}
	})
	if ainit.Config.IsLocalDev == "true" {
		devServer := http.Server{
			Handler: m,
			Addr:    "0.0.0.0:5001",
		}
		slog.Info("starting dev http1.1/2 Server")
		if err := devServer.ListenAndServe(); err != nil {
			panic(fmt.Sprintf("serving dev server: %v", err))
		}
	} else {
		certManager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(resolveCacheDir()),
			HostPolicy: autocert.HostWhitelist(ainit.Config.AcmeHosts...),
			Email:      ainit.Config.AcmeEmail,
		}

		// TLS-ALPN-01 ACME challenge runs inside the port 443 listener —
		// no port 80 is used. certManager.TLSConfig() wires GetCertificate
		// and adds "acme-tls/1" to NextProtos so autocert can complete the
		// challenge in-band on the same TLS listener.
		httpsAddr := net.JoinHostPort(ainit.Config.BindAddr, "443")
		httpsServer := http.Server{
			Handler:   m,
			Addr:      httpsAddr,
			TLSConfig: certManager.TLSConfig(),
		}

		slog.Info("starting https server", "addr", httpsAddr)
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
			panic(fmt.Sprintf("serving https server: %v", err))
		}
	}
}

func runSlave() {
	cfg := ainit.Config
	tlsCfg, err := cluster.LoadTLSConfig(cfg.ClusterCA, cfg.ClusterCert, cfg.ClusterKey)
	if err != nil {
		panic(fmt.Sprintf("loading cluster TLS config: %v", err))
	}

	machineName := machineNameFromCert(cfg.ClusterCert)

	slog.Info("starting in slave mode", "machine", machineName, "primary", cfg.PrimaryAddr, "primaryName", cfg.PrimaryName)
	if err := slave.Run(context.Background(), slave.Config{
		TLS:         tlsCfg,
		PrimaryAddr: cfg.PrimaryAddr,
		PrimaryName: cfg.PrimaryName,
		MachineName: machineName,
		DataDir:     cfg.DataDir,
		GithubToken: cfg.GithubToken,
	}); err != nil {
		panic(fmt.Sprintf("slave exited: %v", err))
	}
}

func startPrimaryCluster(h *handler.Handler) {
	cfg := ainit.Config
	tlsCfg, err := cluster.LoadTLSConfig(cfg.ClusterCA, cfg.ClusterCert, cfg.ClusterKey)
	if err != nil {
		panic(fmt.Sprintf("loading cluster TLS config: %v", err))
	}

	p, err := primary.New(h.Store, tlsCfg, cfg.ClusterListen)
	if err != nil {
		panic(fmt.Sprintf("creating cluster primary: %v", err))
	}

	h.ClusterPrimary = p
	p.OnSlaveConnect = func(machine string) {
		h.Store.EnsureSystemDeployment(machine)
	}
	p.Start(context.Background())
	slog.Info("cluster primary started", "addr", cfg.ClusterListen)
}

// resolvePrimaryMachineName derives the primary's machine name. If a
// cluster cert is configured, it uses the cert CN (matching how slaves
// and the cluster listener identify peers). In local dev with no cert
// configured, it falls back to "localhost" so single-node setups work
// without any TLS wiring.
func resolvePrimaryMachineName() string {
	if ainit.Config.ClusterCert != "" {
		return machineNameFromCert(ainit.Config.ClusterCert)
	}
	if ainit.Config.IsLocalDev == "true" {
		return "localhost"
	}
	panic("OPSAGENT_CLUSTER_CERT must be set to identify this machine (or enable OPSAGENT_LOCAL_DEV for a localhost fallback)")
}

func machineNameFromCert(certPath string) string {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		panic(fmt.Sprintf("reading cluster cert %q: %v", certPath, err))
	}
	block, _ := pem.Decode(certBytes)
	if block == nil {
		panic(fmt.Sprintf("cluster cert %q contains no PEM data", certPath))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("parsing cluster cert %q: %v", certPath, err))
	}
	if cert.Subject.CommonName == "" {
		panic(fmt.Sprintf("cluster cert %q has no CN", certPath))
	}
	return cert.Subject.CommonName
}

func resolveCacheDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("resolving home dir: %v", err))
	}
	cacheDir := filepath.Join(homeDir, ".certs", "opsagent")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		panic(fmt.Sprintf("creating acme cache dir: %v", err))
	}
	return cacheDir
}
