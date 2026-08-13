package netproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/time/rate"
)

const (
	clientHelloTimeout = 5 * time.Second
	backendDialTimeout = 5 * time.Second
	maxClientHelloSize = 64 << 10
)

// netStateSubscriber atomically supplies the current state and future updates.
// It prevents a consumer from missing an update between retrieving a snapshot
// and beginning to wait for changes.
type netStateSubscriber interface {
	SnapshotAndSubscribe() (*apigen.NetState, <-chan *apigen.NetState, func())
}

// RunTLSIngress serves TLS passthrough and terminated HTTPS routes from full
// NetState snapshots. It reads the ClientHello to select a route by SNI, then
// either forwards the TLS stream unchanged or terminates it locally.
func RunTLSIngress(ctx context.Context, states netStateSubscriber, certs *certStore) error {
	s := &ingressServer{
		ctx:            ctx,
		listeners:      make(map[uint16]net.Listener),
		errs:           make(chan error, 1),
		warningLimiter: newOperationalWarningLimiter(),
	}
	s.https = newHTTPSServer(ctx, certs, func() *httpsState {
		state := s.state.Load()
		if state == nil {
			return nil
		}
		return state.https
	}, s.warningLimiter)
	go s.https.run()
	snapshot, updates, unsubscribe := states.SnapshotAndSubscribe()
	defer unsubscribe()
	if err := s.setState(snapshot); err != nil {
		return err
	}
	defer s.closeListeners()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-s.errs:
			return err
		case snapshot := <-updates:
			if err := s.setState(snapshot); err != nil {
				return err
			}
		}
	}
}

type ingressServer struct {
	ctx   context.Context
	https *httpsServer

	state atomic.Pointer[ingressState]

	mu           sync.Mutex
	listeners    map[uint16]net.Listener
	httpListener net.Listener
	errs         chan error

	warningLimiter    *rate.Limiter
	activeConnections atomic.Int64
}

type ingressState struct {
	seq    int64
	routes map[uint16]map[string]*ingressRoute
	https  *httpsState
}

func (s *ingressState) wantedTLSPorts() map[uint16]struct{} {
	ports := make(map[uint16]struct{}, len(s.routes)+1)
	for port := range s.routes {
		ports[port] = struct{}{}
	}
	if s.https != nil && len(s.https.hosts) > 0 {
		ports[443] = struct{}{}
	}
	return ports
}

type ingressRoute struct {
	backends []ingressBackend
	next     atomic.Uint64
}

type ingressBackend struct {
	address string
	port    uint16
}

func (s *ingressServer) setState(snapshot *apigen.NetState) error {
	if snapshot == nil {
		return nil
	}
	cur := s.state.Load()
	if cur != nil && snapshot.Seq <= cur.seq {
		return nil
	}
	next := ingressStateFromSnapshot(snapshot)
	next.https = s.https.buildState(snapshot)
	return s.install(next)
}

func ingressStateFromSnapshot(netState *apigen.NetState) *ingressState {
	state := &ingressState{
		seq:    netState.Seq,
		routes: make(map[uint16]map[string]*ingressRoute),
	}
	for _, ingress := range netState.Ingress {
		if ingress == nil || ingress.Kind != apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH || ingress.TlsPassthrough == nil {
			continue
		}
		port := ingress.TlsPassthrough.HostPort
		hostname, ok := ingressHostnameForProxy(ingress.Hostname)
		if port == netproxyDNSPort || port < 1 || port > 65535 || !ok {
			continue
		}
		byHostname := state.routes[uint16(port)]
		if byHostname == nil {
			byHostname = make(map[string]*ingressRoute)
			state.routes[uint16(port)] = byHostname
		}
		if _, duplicate := byHostname[hostname]; duplicate {
			continue
		}
		route := &ingressRoute{}
		for _, backend := range ingress.TlsPassthrough.Backends {
			if backend == nil || backend.Port < 1 || backend.Port > 65535 {
				continue
			}
			if _, err := netip.ParseAddr(backend.Address); err != nil {
				continue
			}
			route.backends = append(route.backends, ingressBackend{address: backend.Address, port: uint16(backend.Port)})
		}
		byHostname[hostname] = route
	}
	return state
}

func (s *ingressServer) install(next *ingressState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := next.wantedTLSPorts()
	opened := make(map[uint16]net.Listener)
	for port := range wanted {
		if _, exists := s.listeners[port]; exists {
			continue
		}
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(int(port)))
		if err != nil {
			for _, openedListener := range opened {
				_ = openedListener.Close()
			}
			return fmt.Errorf("listening for TLS ingress on port %d: %w", port, err)
		}
		opened[port] = listener
	}
	wantHTTP := next.https != nil
	if wantHTTP && s.httpListener == nil {
		listener, err := net.Listen("tcp", ":80")
		if err != nil {
			for _, openedListener := range opened {
				_ = openedListener.Close()
			}
			return fmt.Errorf("listening for HTTP ingress redirects on port 80: %w", err)
		}
		s.httpListener = listener
		go s.serveHTTPRedirect(listener)
		slog.Info("HTTP ingress redirect listener started")
	}

	s.state.Store(next)
	for port, listener := range opened {
		s.listeners[port] = listener
		go s.serve(port, listener)
		slog.Info("TLS ingress listener started", "port", port)
	}
	for port, listener := range s.listeners {
		if _, ok := wanted[port]; ok {
			continue
		}
		delete(s.listeners, port)
		_ = listener.Close()
		slog.Info("TLS ingress listener stopped", "port", port)
	}
	if !wantHTTP && s.httpListener != nil {
		_ = s.httpListener.Close()
		s.httpListener = nil
		slog.Info("HTTP ingress redirect listener stopped")
	}
	return nil
}

func (s *ingressServer) serveHTTPRedirect(listener net.Listener) {
	server := &http.Server{
		Handler: &httpRedirectServer{currentState: func() *httpsState {
			state := s.state.Load()
			if state == nil {
				return nil
			}
			return state.https
		}},
		ReadHeaderTimeout: httpsReadHeaderTimeout,
		IdleTimeout:       httpsIdleTimeout,
	}
	go func() {
		<-s.ctx.Done()
		_ = server.Close()
	}()
	err := server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) && s.ctx.Err() == nil {
		slog.Warn("HTTP ingress redirect server stopped", "err", err)
	}
}

func (s *ingressServer) serve(port uint16, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || s.ctx.Err() != nil {
				return
			}
			if temporary, ok := err.(interface{ Temporary() bool }); ok && temporary.Temporary() {
				slog.Warn("accepting TLS ingress connection failed", "port", port, "err", err)
				time.Sleep(50 * time.Millisecond)
				continue
			}
			select {
			case s.errs <- fmt.Errorf("accepting TLS ingress on port %d: %w", port, err):
			case <-s.ctx.Done():
			}
			return
		}
		go s.handle(port, conn)
	}
}

func (s *ingressServer) handle(port uint16, client net.Conn) {
	handedOff := false
	defer func() {
		if !handedOff {
			_ = client.Close()
		}
	}()
	_ = client.SetReadDeadline(time.Now().Add(clientHelloTimeout))
	preface, hostname, err := readTLSClientHello(client)
	if err != nil {
		slog.Debug("reading TLS ingress ClientHello failed", "port", port, "err", err)
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	state := s.state.Load()
	if state == nil {
		return
	}
	route := state.routes[port][hostname]
	if route == nil {
		if port == 443 && state.https != nil && state.https.hosts[hostname] != nil {
			handedOff = true
			s.https.terminate(client, preface)
			return
		}
		slog.Debug("TLS ingress route has no ready backends", "port", port, "hostname", hostname)
		return
	}
	if len(route.backends) == 0 {
		slog.Debug("TLS ingress route has no ready backends", "port", port, "hostname", hostname)
		return
	}

	backend, err := route.dial(s.ctx)
	if err != nil {
		if allowOperationalWarning(s.warningLimiter) {
			slog.Warn("dialing all TLS ingress backends failed", "port", port, "hostname", hostname, "backend_count", len(route.backends), "err", err)
		}
		return
	}
	defer backend.Close()
	if _, err := backend.Write(preface); err != nil {
		slog.Debug("writing TLS ClientHello to ingress backend failed", "port", port, "hostname", hostname, "err", err)
		return
	}
	activeConnections := s.activeConnections.Add(1)
	defer s.activeConnections.Add(-1)
	slog.Info("TLS ingress connection routed",
		"port", port,
		"hostname", hostname,
		"client_address", client.RemoteAddr(),
		"backend_address", backend.RemoteAddr(),
		"active_connections", activeConnections,
	)
	relayTCP(s.ctx, client, backend)
}

func (r *ingressRoute) dial(ctx context.Context) (net.Conn, error) {
	start := r.next.Add(1) - 1
	dialer := net.Dialer{Timeout: backendDialTimeout}
	var lastErr error
	for offset := uint64(0); offset < uint64(len(r.backends)); offset++ {
		backend := r.backends[(start+offset)%uint64(len(r.backends))]
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(backend.address, strconv.Itoa(int(backend.port))))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func relayTCP(ctx context.Context, client, backend net.Conn) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
			_ = backend.Close()
		case <-done:
		}
	}()
	defer close(done)

	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(backend, client)
		closeWrite(backend)
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(client, backend)
		closeWrite(client)
	}()
	copies.Wait()
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func (s *ingressServer) closeListeners() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for port, listener := range s.listeners {
		delete(s.listeners, port)
		_ = listener.Close()
	}
	if s.httpListener != nil {
		_ = s.httpListener.Close()
		s.httpListener = nil
	}
}

func readTLSClientHello(conn net.Conn) ([]byte, string, error) {
	var raw, handshake []byte
	for {
		var recordHeader [5]byte
		if err := readClientHelloBytes(conn, &raw, recordHeader[:]); err != nil {
			return nil, "", err
		}
		if recordHeader[0] != 22 || recordHeader[1] != 3 {
			return nil, "", errors.New("expected TLS handshake record")
		}
		recordLength := int(binary.BigEndian.Uint16(recordHeader[3:]))
		if recordLength == 0 || recordLength > 18432 || len(raw)+recordLength > maxClientHelloSize {
			return nil, "", errors.New("TLS ClientHello exceeds limit")
		}
		record := make([]byte, recordLength)
		if err := readClientHelloBytes(conn, &raw, record); err != nil {
			return nil, "", err
		}
		handshake = append(handshake, record...)
		if len(handshake) < 4 {
			continue
		}
		if handshake[0] != 1 {
			return nil, "", errors.New("expected TLS ClientHello")
		}
		helloLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
		if helloLength == 0 || helloLength+4 > maxClientHelloSize {
			return nil, "", errors.New("TLS ClientHello exceeds limit")
		}
		if len(handshake) < helloLength+4 {
			continue
		}
		hostname, err := tlsServerName(handshake[4 : helloLength+4])
		if err != nil {
			return nil, "", err
		}
		return raw, hostname, nil
	}
}

func readClientHelloBytes(r io.Reader, raw *[]byte, target []byte) error {
	if len(*raw)+len(target) > maxClientHelloSize {
		return errors.New("TLS ClientHello exceeds limit")
	}
	if _, err := io.ReadFull(r, target); err != nil {
		return err
	}
	*raw = append(*raw, target...)
	return nil
}

func tlsServerName(hello []byte) (string, error) {
	if len(hello) < 35 { // legacy_version, random, and session ID length
		return "", errors.New("short TLS ClientHello")
	}
	offset := 34
	sessionIDLength := int(hello[offset])
	offset++
	if offset+sessionIDLength+2 > len(hello) {
		return "", errors.New("invalid TLS session ID")
	}
	offset += sessionIDLength
	cipherSuitesLength := int(binary.BigEndian.Uint16(hello[offset:]))
	offset += 2
	if cipherSuitesLength == 0 || cipherSuitesLength%2 != 0 || offset+cipherSuitesLength+1 > len(hello) {
		return "", errors.New("invalid TLS cipher suites")
	}
	offset += cipherSuitesLength
	compressionLength := int(hello[offset])
	offset++
	if compressionLength == 0 || offset+compressionLength+2 > len(hello) {
		return "", errors.New("invalid TLS compression methods")
	}
	offset += compressionLength
	extensionsLength := int(binary.BigEndian.Uint16(hello[offset:]))
	offset += 2
	if offset+extensionsLength != len(hello) {
		return "", errors.New("invalid TLS extensions")
	}
	for end := offset + extensionsLength; offset < end; {
		if offset+4 > end {
			return "", errors.New("invalid TLS extension")
		}
		extensionType := binary.BigEndian.Uint16(hello[offset:])
		extensionLength := int(binary.BigEndian.Uint16(hello[offset+2:]))
		offset += 4
		if offset+extensionLength > end {
			return "", errors.New("invalid TLS extension length")
		}
		if extensionType == 0 {
			return serverNameFromExtension(hello[offset : offset+extensionLength])
		}
		offset += extensionLength
	}
	return "", errors.New("TLS ClientHello has no SNI")
}

func serverNameFromExtension(extension []byte) (string, error) {
	if len(extension) < 5 {
		return "", errors.New("invalid TLS SNI extension")
	}
	listLength := int(binary.BigEndian.Uint16(extension))
	if listLength+2 != len(extension) {
		return "", errors.New("invalid TLS SNI list")
	}
	for offset := 2; offset < len(extension); {
		if offset+3 > len(extension) {
			return "", errors.New("invalid TLS server name")
		}
		nameType := extension[offset]
		nameLength := int(binary.BigEndian.Uint16(extension[offset+1:]))
		offset += 3
		if nameLength == 0 || offset+nameLength > len(extension) {
			return "", errors.New("invalid TLS server name length")
		}
		if nameType == 0 {
			if hostname, ok := ingressHostnameForProxy(string(extension[offset : offset+nameLength])); ok {
				return hostname, nil
			}
			return "", errors.New("invalid TLS server name")
		}
		offset += nameLength
	}
	return "", errors.New("TLS ClientHello has no host_name SNI")
}

func ingressHostnameForProxy(value string) (string, bool) {
	hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if hostname == "" || len(hostname) > 253 {
		return "", false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", false
			}
		}
	}
	return hostname, true
}
