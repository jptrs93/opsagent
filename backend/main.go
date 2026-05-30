package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/primary"
	"github.com/jptrs93/opsagent/backend/slave"
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
// Opsagent treats any failure of the on-disk SQLite store as an unrecoverable
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
	fmt.Println(fmt.Sprintf("opsagent starting version=%v", version))

	// Slave mode: if OPSAGENT_PRIMARY_ADDR is set, this node is a worker.
	if ainit.Config.PrimaryAddr != "" {
		runSlave()
		return
	}

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

	// Primary cluster listener: if cluster TLS is configured, start the
	// mTLS listener alongside the public HTTP server.
	if ainit.Config.ClusterCA != "" {
		startPrimaryCluster(h)
	}
	m := apigen.CreateOpsagentHttpV1Mux(h, &apigen.MuxConfig{
		VerifyAuth:         h.VerifyAuth,
		MaxRequestBodySize: 20_000_000,
	})
	if ainit.Config.IsLocalDev == "true" {
		devServer := http.Server{Handler: m, Addr: "localhost:8080"}
		slog.Info("starting dev http Server")
		err := devServer.ListenAndServe()
		panic(fmt.Sprintf("dev server ended: %v", err))
	} else {
		certManager := &autocert.Manager{
			Prompt:      autocert.AcceptTOS,
			Cache:       autocert.DirCache(filepath.Join(ainit.Config.DataDir, ".certs")),
			HostPolicy:  autocert.HostWhitelist(ainit.Config.AcmeHosts...),
			Email:       ainit.Config.AcmeEmail,
			RenewBefore: 168 * time.Hour,
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
		err := httpsServer.ListenAndServeTLS("", "")
		panic(fmt.Sprintf("https server ended: %v", err))
	}
}

func runSlave() {
	cfg := ainit.Config
	tlsCfg := certu.MustLoadTLSConfig(cfg.ClusterCA, cfg.ClusterCert, cfg.ClusterKey)
	machineName := certu.MustCertLoadCommonName(cfg.ClusterCert)

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
	tlsCfg := certu.MustLoadTLSConfig(cfg.ClusterCA, cfg.ClusterCert, cfg.ClusterKey)

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
