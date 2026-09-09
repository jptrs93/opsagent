package webui

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/debug/acmedebug"
	"github.com/jptrs93/opsagent/backend/util/stringu"
	"golang.org/x/crypto/acme/autocert"
)

const primaryServerShutdownTimeout = 20 * time.Second

func RunPrimaryHTTPWebUI(ctx context.Context, cs *systemconfig.Service, webHandler http.Handler) error {
	ctx = logu.AddTag(ctx, "WebUI")
	cfg := cs.Snapshot().Settings
	if !cs.MustLoadBoolSetting(cfg.HttpWeb.Enabled) {
		return nil
	}
	listen := cs.MustLoadStringSetting(cfg.HttpWeb.Listen)
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return fmt.Errorf("web-http listen address is required")
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("starting web-http listener on %s: %w", listen, err)
	}
	srv := &http.Server{
		Addr:        listen,
		Handler:     webHandler,
		BaseContext: primaryServerBaseContext(ctx),
	}
	slog.InfoContext(ctx, fmt.Sprintf("starting HTTP Web UI server server=web-http addr=%v", listen))
	return runManagedWebUIServer(ctx, "web-http", srv, func() error { return srv.Serve(ln) })
}

func RunPrimaryHTTPSWebUI(ctx context.Context, cs *systemconfig.Service, secretsMgr *secrets.Manager, webHandler http.Handler) error {
	ctx = logu.AddTag(ctx, "WebUI")
	cfg := cs.Snapshot().Settings
	if !cs.MustLoadBoolSetting(cfg.HttpsWeb.Enabled) {
		return nil
	}
	listen := cs.MustLoadStringSetting(cfg.HttpsWeb.Listen)
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return fmt.Errorf("web-https listen address is required")
	}
	tlsConfig, err := webUITLSConfig(cs, secretsMgr, &cfg)
	if err != nil {
		return fmt.Errorf("building web-https TLS config: %w", err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("starting web-https listener on %s: %w", listen, err)
	}
	srv := &http.Server{
		Addr:        listen,
		Handler:     webHandler,
		TLSConfig:   tlsConfig,
		BaseContext: primaryServerBaseContext(ctx),
	}
	slog.InfoContext(ctx, fmt.Sprintf("starting HTTPS Web UI server server=web-https addr=%v", listen))
	return runManagedWebUIServer(ctx, "web-https", srv, func() error { return srv.ServeTLS(ln, "", "") })
}

func runManagedWebUIServer(ctx context.Context, name string, srv *http.Server, serve func() error) error {
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve()
	}()

	select {
	case err := <-serveDone:
		return managedWebUIServerResult(ctx, name, err)
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), primaryServerShutdownTimeout)
		defer shutdownCancel()
		slog.InfoContext(ctx, fmt.Sprintf("stopping Web UI server server=%v", name))
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("Web UI server graceful shutdown failed; closing server=%v", name), "err", err)
			_ = srv.Close()
		}
		if err := managedWebUIServerResult(ctx, name, <-serveDone); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("Web UI server ended during shutdown server=%v", name), "err", err)
		}
		return nil
	}
}

func managedWebUIServerResult(ctx context.Context, name string, err error) error {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server ended: %w", name, err)
	}
	if ctx.Err() == nil {
		return fmt.Errorf("%s server ended unexpectedly", name)
	}
	return nil
}

func webUITLSConfig(
	cs *systemconfig.Service,
	secretsMgr *secrets.Manager,
	cfg *apigen.ClusterSettings) (*tls.Config, error) {
	tlsSelfManaged := cs.MustLoadBoolSetting(cfg.HttpsWeb.TlsSelfManaged)
	if tlsSelfManaged {
		return selfManagedWebUITLSConfig(secretsMgr, cs, cfg)
	}
	acmeHosts := cs.MustLoadStringSetting(cfg.HttpsWeb.AcmeHosts)
	acmeEmail := cs.MustLoadStringSetting(cfg.HttpsWeb.AcmeEmail)
	certManager := &autocert.Manager{
		Prompt:      autocert.AcceptTOS,
		Cache:       autocert.DirCache(ainit.StaticConfig.ACMECacheDir),
		HostPolicy:  autocert.HostWhitelist(stringu.ParseStringList(acmeHosts)...),
		Email:       acmeEmail,
		RenewBefore: 168 * time.Hour,
	}
	acmedebug.Enable(certManager)
	tlsConfig := certManager.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS13
	return tlsConfig, nil
}

func selfManagedWebUITLSConfig(store *secrets.Manager, loader systemconfig.Loader, cfg *apigen.ClusterSettings) (*tls.Config, error) {
	bundle, err := webUITLSBundle(store, loader, cfg)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		return nil, fmt.Errorf("loading Web UI TLS certificate bundle: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, nil
}

func webUITLSBundle(store *secrets.Manager, loader systemconfig.Loader, cfg *apigen.ClusterSettings) ([]byte, error) {
	if id := cfg.HttpsWeb.TlsCertPem.VersionID; id != 0 {
		value, err := store.RevealByID(id)
		if err != nil {
			return nil, err
		}
		return value, nil
	}
	// No operator bundle: serve a leaf under the local CA, issuing or
	// reissuing it here so enabling self-managed TLS later, or changing the
	// hostnames, never leaves the listener without a certificate.
	bundle, caCertPEM, err := pki.EnsureWebUILocalTLS(store, webUITLSNames(loader, cfg))
	if err != nil {
		return nil, err
	}
	if err := certu.WriteWebUILocalCAFile(ainit.StaticConfig.DataDir, caCertPEM); err != nil {
		return nil, fmt.Errorf("exporting Web UI CA certificate: %w", err)
	}
	return bundle, nil
}

func webUITLSNames(loader systemconfig.Loader, cfg *apigen.ClusterSettings) []string {
	acmeHosts := loader.MustLoadStringSetting(cfg.HttpsWeb.AcmeHosts)
	listen := loader.MustLoadStringSetting(cfg.HttpsWeb.Listen)
	names := append([]string{}, stringu.ParseStringList(acmeHosts)...)
	if host := listenHost(listen); host != "" {
		names = append(names, host)
	}
	return names
}

func listenHost(addr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	return strings.Trim(host, "[]")
}

func primaryServerBaseContext(ctx context.Context) func(net.Listener) context.Context {
	return func(net.Listener) context.Context { return ctx }
}
