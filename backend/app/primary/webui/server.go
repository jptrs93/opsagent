package webui

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"github.com/jptrs93/opsagent/backend/util/debug/acmedebug"
	"github.com/jptrs93/opsagent/backend/util/stringu"
	"golang.org/x/crypto/acme/autocert"
)

const primaryServerShutdownTimeout = 20 * time.Second

const (
	serverHTTP  = "web-http"
	serverHTTPS = "web-https"
)

type listenSpec struct {
	enabled bool
	listen  string
}

type serverKind struct {
	name    string
	field   string
	spec    func(systemconfig.Loader, *apigen.ClusterSettings) listenSpec
	changed func(a, b apigen.ClusterSettings) bool
	tls     bool
}

var httpKind = serverKind{
	name:  serverHTTP,
	field: "http_web.listen",
	spec: func(loader systemconfig.Loader, cfg *apigen.ClusterSettings) listenSpec {
		return listenSpec{
			enabled: loader.MustLoadBoolSetting(cfg.HttpWeb.Enabled),
			listen:  strings.TrimSpace(loader.MustLoadStringSetting(cfg.HttpWeb.Listen)),
		}
	},
	changed: func(a, b apigen.ClusterSettings) bool {
		return !bytes.Equal(a.HttpWeb.Encode(), b.HttpWeb.Encode())
	},
}

var httpsKind = serverKind{
	name:  serverHTTPS,
	field: "https_web.listen",
	spec: func(loader systemconfig.Loader, cfg *apigen.ClusterSettings) listenSpec {
		return listenSpec{
			enabled: loader.MustLoadBoolSetting(cfg.HttpsWeb.Enabled),
			listen:  strings.TrimSpace(loader.MustLoadStringSetting(cfg.HttpsWeb.Listen)),
		}
	},
	changed: func(a, b apigen.ClusterSettings) bool {
		return !bytes.Equal(a.HttpsWeb.Encode(), b.HttpsWeb.Encode())
	},
	tls: true,
}

type Manager struct {
	cs      *systemconfig.Service
	secrets *secrets.Manager
	listen  func(string) (net.Listener, error)

	mu      sync.Mutex
	pending map[string]*pendingListener
}

type pendingListener struct {
	listen string
	ln     net.Listener
}

type Prepared struct {
	m       *Manager
	entries []*pendingListener
}

func NewManager(cs *systemconfig.Service, secretsMgr *secrets.Manager) *Manager {
	return &Manager{
		cs:      cs,
		secrets: secretsMgr,
		listen:  func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) },
		pending: make(map[string]*pendingListener),
	}
}

func (m *Manager) Prepare(resolved *apigen.ClusterSettings) (*Prepared, error) {
	current := m.cs.Snapshot().Settings
	prepared := &Prepared{m: m}
	for _, kind := range []serverKind{httpKind, httpsKind} {
		want := kind.spec(m.cs, resolved)
		have := kind.spec(m.cs, &current)
		if !want.enabled || want == have {
			continue
		}
		ln, err := m.listen(want.listen)
		if err != nil {
			prepared.Release()
			return nil, fmt.Errorf("%s: cannot listen on %s: %w", kind.field, want.listen, err)
		}
		entry := &pendingListener{listen: want.listen, ln: ln}
		m.mu.Lock()
		if previous := m.pending[kind.name]; previous != nil {
			_ = previous.ln.Close()
		}
		m.pending[kind.name] = entry
		m.mu.Unlock()
		prepared.entries = append(prepared.entries, entry)
	}
	return prepared, nil
}

func (p *Prepared) Release() {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	for _, entry := range p.entries {
		for name, pending := range p.m.pending {
			if pending == entry {
				delete(p.m.pending, name)
			}
		}
		_ = entry.ln.Close()
	}
	p.entries = nil
}

func (m *Manager) take(name, listen string) net.Listener {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := m.pending[name]
	if pending == nil {
		return nil
	}
	if pending.listen != listen {
		return nil
	}
	delete(m.pending, name)
	return pending.ln
}

func (m *Manager) obtain(name, listen string) (net.Listener, error) {
	if ln := m.take(name, listen); ln != nil {
		return ln, nil
	}
	return m.listen(listen)
}

func (m *Manager) RunHTTP(ctx context.Context, webHandler http.Handler) error {
	return m.run(ctx, httpKind, webHandler)
}

func (m *Manager) RunHTTPS(ctx context.Context, webHandler http.Handler) error {
	return m.run(ctx, httpsKind, webHandler)
}

type runningServer struct {
	listen    string
	srv       *http.Server
	tlsConfig *atomic.Pointer[tls.Config]
	serveDone chan error
}

func (m *Manager) run(ctx context.Context, kind serverKind, webHandler http.Handler) error {
	ctx = logu.AddTag(ctx, "WebUI")
	filter := func(a, b apigen.SystemConfig) bool { return kind.changed(a.Settings, b.Settings) }
	subscribe := func() *pubsubu.Sub[apigen.SystemConfig] {
		return m.cs.SnapshotAndSubscribe(filter)
	}
	sub := subscribe()
	defer func() { sub.Unsubscribe() }()

	var current *runningServer
	stopCurrent := func() {
		if current == nil {
			return
		}
		m.stop(ctx, kind.name, current)
		current = nil
	}
	defer func() { stopCurrent() }()

	apply := func(cfg apigen.SystemConfig, initial bool) error {
		settings := cfg.Settings
		want := kind.spec(m.cs, &settings)
		if !want.enabled {
			stopCurrent()
			return nil
		}
		if want.listen == "" {
			return fmt.Errorf("%s listen address is required", kind.name)
		}
		if current != nil && current.listen == want.listen {
			if kind.tls {
				tlsConfig, err := webUITLSConfig(m.cs, m.secrets, &settings)
				if err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("Web UI server keeps its previous TLS configuration server=%v", kind.name), "err", err)
					return nil
				}
				current.tlsConfig.Store(tlsConfig)
			}
			return nil
		}
		next, err := m.start(ctx, kind, webHandler, &settings, want.listen)
		if err != nil {
			if initial {
				return err
			}
			slog.ErrorContext(ctx, fmt.Sprintf("Web UI server keeps its previous listener server=%v addr=%v", kind.name, want.listen), "err", err)
			return nil
		}
		stopCurrent()
		current = next
		return nil
	}
	if err := apply(sub.InitialValue, true); err != nil {
		return err
	}

	for {
		var serveDone chan error
		if current != nil {
			serveDone = current.serveDone
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-serveDone:
			running := current
			current = nil
			return managedWebUIServerResult(ctx, kind.name, running.listen, err)
		case cfg, ok := <-sub.Ch:
			if !ok {
				sub = subscribe()
				cfg = sub.InitialValue
			}
			if err := apply(cfg, false); err != nil {
				return err
			}
		}
	}
}

func (m *Manager) start(ctx context.Context, kind serverKind, webHandler http.Handler, settings *apigen.ClusterSettings, listen string) (*runningServer, error) {
	running := &runningServer{listen: listen, serveDone: make(chan error, 1)}
	srv := &http.Server{
		Addr:        listen,
		Handler:     webHandler,
		BaseContext: primaryServerBaseContext(ctx),
	}
	serve := func(ln net.Listener) error { return srv.Serve(ln) }
	if kind.tls {
		tlsConfig, err := webUITLSConfig(m.cs, m.secrets, settings)
		if err != nil {
			return nil, fmt.Errorf("building %s TLS config: %w", kind.name, err)
		}
		running.tlsConfig = &atomic.Pointer[tls.Config]{}
		running.tlsConfig.Store(tlsConfig)
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS13,
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return running.tlsConfig.Load(), nil
			},
		}
		serve = func(ln net.Listener) error { return srv.ServeTLS(ln, "", "") }
	}
	ln, err := m.obtain(kind.name, listen)
	if err != nil {
		return nil, fmt.Errorf("starting %s listener on %s: %w", kind.name, listen, err)
	}
	running.srv = srv
	slog.InfoContext(ctx, fmt.Sprintf("starting Web UI server server=%v addr=%v", kind.name, listen))
	go func() { running.serveDone <- serve(ln) }()
	return running, nil
}

func (m *Manager) stop(ctx context.Context, name string, running *runningServer) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), primaryServerShutdownTimeout)
	defer shutdownCancel()
	slog.InfoContext(ctx, fmt.Sprintf("stopping Web UI server server=%v addr=%v", name, running.listen))
	if err := running.srv.Shutdown(shutdownCtx); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("Web UI server graceful shutdown failed; closing server=%v", name), "err", err)
		_ = running.srv.Close()
	}
	if err := <-running.serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.WarnContext(ctx, fmt.Sprintf("Web UI server ended during shutdown server=%v", name), "err", err)
	}
}

func managedWebUIServerResult(ctx context.Context, name, listen string, err error) error {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server on %s ended: %w", name, listen, err)
	}
	if ctx.Err() == nil {
		return fmt.Errorf("%s server on %s ended unexpectedly", name, listen)
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
	return withHTTPProtos(tlsConfig), nil
}

// The server hands the per-connection config out through GetConfigForClient,
// and ALPN then negotiates against that config's NextProtos rather than the
// ones ServeTLS adds to the outer config, so h2 has to be listed here.
func withHTTPProtos(tlsConfig *tls.Config) *tls.Config {
	for _, proto := range []string{"http/1.1", "h2"} {
		if !slices.Contains(tlsConfig.NextProtos, proto) {
			tlsConfig.NextProtos = append([]string{proto}, tlsConfig.NextProtos...)
		}
	}
	return tlsConfig
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
	return withHTTPProtos(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}), nil
}

func webUITLSBundle(store *secrets.Manager, loader systemconfig.Loader, cfg *apigen.ClusterSettings) ([]byte, error) {
	if ref := cfg.HttpsWeb.TlsCertPem; ref.Present && ref.Value.Valid() {
		value, err := store.RevealByRef(ref.Value.Ref())
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
