package netproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/net/http2"
)

const (
	httpsReadHeaderTimeout       = 10 * time.Second
	httpsIdleTimeout             = 75 * time.Second
	backendResponseHeaderTimeout = 60 * time.Second
	backendIdleConnTimeout       = 90 * time.Second
	acmeChallengePathPrefix      = "/.well-known/acme-challenge/"
)

type httpsState struct {
	hosts      map[string]*httpsHost
	challenges map[string]string
}

type httpsHost struct {
	certID string
	routes []*httpsRoute
}

type httpsRoute struct {
	prefix   string
	maxBody  int64
	proxy    *httputil.ReverseProxy
	backends *backendPool
}

type backendPool struct {
	backends []ingressBackend
	next     atomic.Uint64
}

func (p *backendPool) pick() (ingressBackend, bool) {
	if len(p.backends) == 0 {
		return ingressBackend{}, false
	}
	i := p.next.Add(1) - 1
	return p.backends[i%uint64(len(p.backends))], true
}

func (h *httpsHost) match(path string) *httpsRoute {
	for _, route := range h.routes {
		if routePrefixMatch(path, route.prefix) {
			return route
		}
	}
	return nil
}

func routePrefixMatch(path, prefix string) bool {
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	return len(path) == len(prefix) || path[len(prefix)] == '/'
}

type httpsServer struct {
	ctx            context.Context
	certs          *certStore
	currentState   func() *httpsState
	warningLimiter interface{ Allow() bool }

	connections *connQueue
	server      *http.Server
	h1Transport http.RoundTripper
	h2Transport http.RoundTripper
}

func newHTTPSServer(ctx context.Context, certs *certStore, currentState func() *httpsState, warningLimiter interface{ Allow() bool }) *httpsServer {
	dialer := &net.Dialer{Timeout: backendDialTimeout}
	s := &httpsServer{
		ctx:            ctx,
		certs:          certs,
		currentState:   currentState,
		warningLimiter: warningLimiter,
		connections:    newConnQueue(ctx),
		h1Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ResponseHeaderTimeout: backendResponseHeaderTimeout,
			IdleConnTimeout:       backendIdleConnTimeout,
			MaxIdleConnsPerHost:   32,
		},
		h2Transport: &h2BackendTransport{inner: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			ReadIdleTimeout: 30 * time.Second,
			PingTimeout:     10 * time.Second,
		}},
	}
	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: httpsReadHeaderTimeout,
		IdleTimeout:       httpsIdleTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	return s
}

func (s *httpsServer) run() {
	go func() {
		<-s.ctx.Done()
		_ = s.server.Close()
	}()
	err := s.server.Serve(s.connections)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && s.ctx.Err() == nil {
		slog.WarnContext(s.ctx, "HTTPS ingress server stopped", "err", err)
	}
}

func (s *httpsServer) terminate(client net.Conn, preface []byte) {
	tlsConn := tls.Server(&prefixConn{Conn: client, reader: io.MultiReader(bytes.NewReader(preface), client)}, &tls.Config{
		GetCertificate: s.certificateForHello,
		NextProtos:     []string{"h2", "http/1.1"},
	})
	if !s.connections.push(tlsConn) {
		_ = client.Close()
	}
}

func (s *httpsServer) certificateForHello(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	hostname, ok := ingressHostnameForProxy(hello.ServerName)
	if !ok {
		return nil, errors.New("invalid server name")
	}
	state := s.currentState()
	if state == nil {
		return nil, errors.New("no HTTPS ingress state")
	}
	host := state.hosts[hostname]
	if host == nil {
		return nil, errors.New("unknown HTTPS ingress hostname")
	}
	cert, ok := s.certs.Get(host.certID)
	if !ok {
		return nil, errors.New("certificate unavailable")
	}
	return cert, nil
}

func (s *httpsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	state := s.currentState()
	hostname, ok := ingressHostnameForProxy(requestHostname(r.Host))
	if state == nil || !ok {
		http.Error(w, "", http.StatusMisdirectedRequest)
		s.logResponse(r, http.StatusMisdirectedRequest, start)
		return
	}
	host := state.hosts[hostname]
	if host == nil {
		http.Error(w, "", http.StatusMisdirectedRequest)
		s.logResponse(r, http.StatusMisdirectedRequest, start)
		return
	}
	route := host.match(r.URL.Path)
	if route == nil {
		http.NotFound(w, r)
		s.logResponse(r, http.StatusNotFound, start)
		return
	}
	if len(route.backends.backends) == 0 {
		http.Error(w, "no ready backends", http.StatusServiceUnavailable)
		s.logResponse(r, http.StatusServiceUnavailable, start)
		return
	}
	if route.maxBody > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, route.maxBody)
	}
	sw := &statusWriter{ResponseWriter: w}
	route.proxy.ServeHTTP(sw, r)
	if sw.status >= http.StatusBadRequest {
		s.logResponse(r, sw.status, start)
	}
}

func (s *httpsServer) logResponse(r *http.Request, status int, start time.Time) {
	slog.InfoContext(s.ctx, fmt.Sprintf("HTTPS ingress non-ok response %s %s%s status=%d duration_ms=%d client=%s",
		r.Method, r.Host, r.URL.Path, status, time.Since(start).Milliseconds(), r.RemoteAddr))
}

func (s *httpsServer) buildRoute(route *apigen.HttpsNetIngress) *httpsRoute {
	pool := &backendPool{}
	for _, backend := range route.Backends {
		if backend == nil || backend.Port < 1 || backend.Port > 65535 {
			continue
		}
		pool.backends = append(pool.backends, ingressBackend{address: backend.Address, port: uint16(backend.Port)})
	}
	prefix := route.PathPrefix
	if prefix == "" {
		prefix = "/"
	}
	strip := route.StripPrefix && prefix != "/"
	transport := s.h1Transport
	if route.BackendProtocol == apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_H2C {
		transport = s.h2Transport
	}
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: flushIntervalFromMs(route.FlushIntervalMs),
		ErrorLog:      log.New(io.Discard, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			backend, ok := pool.pick()
			if !ok {
				return
			}
			pr.SetURL(&url.URL{Scheme: "http", Host: net.JoinHostPort(backend.address, strconv.Itoa(int(backend.port)))})
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
			if strip {
				pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
				stripped := strings.TrimPrefix(pr.In.URL.Path, prefix)
				if stripped == "" || stripped[0] != '/' {
					stripped = "/" + stripped
				}
				pr.Out.URL.Path = stripped
				pr.Out.URL.RawPath = ""
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "http: request body too large") {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			if allowOperationalWarning(nil) {
				slog.WarnContext(s.ctx, fmt.Sprintf("HTTPS ingress backend request for %s%s failed", r.Host, r.URL.Path), "err", err)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return &httpsRoute{
		prefix:   prefix,
		maxBody:  route.MaxRequestBodyBytes,
		proxy:    proxy,
		backends: pool,
	}
}

func (s *httpsServer) buildState(snapshot *apigen.NetState) *httpsState {
	state := &httpsState{
		hosts:      map[string]*httpsHost{},
		challenges: map[string]string{},
	}
	for _, challenge := range snapshot.AcmeChallenges {
		if challenge == nil || challenge.Token == "" {
			continue
		}
		state.challenges[challenge.Token] = challenge.KeyAuthorization
	}
	for _, ingress := range snapshot.Ingress {
		if ingress == nil || ingress.Kind != apigen.IngressKind_INGRESS_KIND_HTTPS || ingress.Https == nil {
			continue
		}
		hostname, ok := ingressHostnameForProxy(ingress.Hostname)
		if !ok {
			continue
		}
		host := state.hosts[hostname]
		if host == nil {
			host = &httpsHost{certID: ingress.Https.CertID}
			state.hosts[hostname] = host
		}
		route := s.buildRoute(ingress.Https)
		duplicate := false
		for _, existing := range host.routes {
			if existing.prefix == route.prefix {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		host.routes = append(host.routes, route)
	}
	if len(state.hosts) == 0 && len(state.challenges) == 0 {
		return nil
	}
	for _, host := range state.hosts {
		sort.Slice(host.routes, func(i, j int) bool { return len(host.routes[i].prefix) > len(host.routes[j].prefix) })
	}
	return state
}

// h2BackendTransport wraps the h2c backend transport to keep full-duplex
// streams live when the backend half-closes first. The raw http2 transport
// deadlocks in that case: closing the response body waits for the
// request-body write goroutine, which sits in an uninterruptible Read on the
// inbound request body the client is deliberately keeping open (bidi
// streaming). Pumping the inbound body through a pipe makes that Read
// cancelable, and the response body's Close tears the pipe down first.
type h2BackendTransport struct {
	inner http.RoundTripper
}

func (t *h2BackendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return t.inner.RoundTrip(req)
	}
	inbound := req.Body
	pr, pw := io.Pipe()
	go func() {
		_, err := io.Copy(pw, inbound)
		_ = pw.CloseWithError(err)
	}()
	req.Body = pr
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}
	resp.Body = &requestUnblockingBody{ReadCloser: resp.Body, unblock: func() {
		_ = pr.CloseWithError(errResponseFinished)
	}}
	return resp, nil
}

var errResponseFinished = errors.New("response finished before request stream ended")

type requestUnblockingBody struct {
	io.ReadCloser
	unblock func()
}

func (b *requestUnblockingBody) Close() error {
	b.unblock()
	return b.ReadCloser.Close()
}

func flushIntervalFromMs(ms int32) time.Duration {
	if ms < 0 {
		return -1
	}
	return time.Duration(ms) * time.Millisecond
}

func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

type prefixConn struct {
	net.Conn
	reader io.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

type connQueue struct {
	ctx    context.Context
	ch     chan net.Conn
	closed chan struct{}
	once   atomic.Bool
}

func newConnQueue(ctx context.Context) *connQueue {
	return &connQueue{ctx: ctx, ch: make(chan net.Conn), closed: make(chan struct{})}
}

func (q *connQueue) push(conn net.Conn) bool {
	select {
	case q.ch <- conn:
		return true
	case <-q.closed:
		return false
	case <-q.ctx.Done():
		return false
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case conn := <-q.ch:
		return conn, nil
	case <-q.closed:
		return nil, net.ErrClosed
	case <-q.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error {
	if q.once.CompareAndSwap(false, true) {
		close(q.closed)
	}
	return nil
}

func (q *connQueue) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv6loopback, Port: 443}
}

type httpRedirectServer struct {
	currentState func() *httpsState
}

func (s *httpRedirectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, acmeChallengePathPrefix) {
		token := strings.TrimPrefix(r.URL.Path, acmeChallengePathPrefix)
		state := s.currentState()
		if state != nil {
			if auth, ok := state.challenges[token]; ok {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte(auth))
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	host := requestHostname(r.Host)
	if host == "" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
}
