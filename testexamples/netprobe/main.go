// netprobe is the network-policy end-to-end workload: one binary that acts as
// a server, a client, or both.
//
// The point of the client half is that every failure is classified. A policy
// drop is silent, so it can only ever look like a connect timeout; anything
// else (NXDOMAIN, refused, unreachable) means something other than the policy
// boundary produced the failure, and a test asserting "the traffic was denied"
// must be able to tell those apart. Each attempt therefore resolves the name,
// dials the resolved address, and issues the request as three separately
// reported stages:
//
//	netprobe probe=<label> stage=dns result=ok addr=<addr>
//	netprobe probe=<label> stage=connect result=error err=timeout detail=...
//	netprobe probe=<label> result=ok status=200 bytes=123
//
// Connections are never reused, so a policy change is observable on the next
// interval without restarting the workload.
//
// Environment:
//
//	NETPROBE_NAME             identity echoed by the server role
//	NETPROBE_LISTEN           server role: tcp/8080,tcp/8081,udp/9000
//	NETPROBE_TARGETS          probe role: label=url[,label=url...]
//	NETPROBE_UDP_TARGETS      probe role: label=host:port[,...]
//	NETPROBE_STREAM_TARGET    probe role: label=url of an SSE endpoint held open
//	NETPROBE_INTERVAL_MS      probe interval, default 5000
//	NETPROBE_TIMEOUT_MS       per-stage timeout, default 3000
//
// Server endpoints on every TCP port:
//
//	/                     identity echo
//	/bulk?bytes=N         N bytes of body (PMTU / large-response coverage)
//	/sse                  unbounded 1s tick stream
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jptrs93/goutil/logu"
)

func main() {
	slog.SetDefault(logu.NewJSONLogger(os.Stdout, slog.LevelInfo))
	name := envOr("NETPROBE_NAME", "netprobe")
	interval := envDuration("NETPROBE_INTERVAL_MS", 5*time.Second)
	timeout := envDuration("NETPROBE_TIMEOUT_MS", 3*time.Second)
	logf("netprobe start name=%s listen=%s targets=%s udp_targets=%s stream=%s",
		name, os.Getenv("NETPROBE_LISTEN"), os.Getenv("NETPROBE_TARGETS"),
		os.Getenv("NETPROBE_UDP_TARGETS"), os.Getenv("NETPROBE_STREAM_TARGET"))

	logAddresses(name)

	listeners := splitList(os.Getenv("NETPROBE_LISTEN"))
	for _, spec := range listeners {
		proto, port, ok := strings.Cut(spec, "/")
		if !ok {
			fatalf("netprobe listen spec %q must be <tcp|udp>/<port>", spec)
		}
		switch proto {
		case "tcp":
			go serveTCP(name, port)
		case "udp":
			go serveUDP(name, port)
		default:
			fatalf("netprobe listen spec %q has unknown protocol %q", spec, proto)
		}
	}
	if len(listeners) > 0 {
		go signalReadyWhenListening(name, listeners)
	}

	for _, target := range parseTargets(os.Getenv("NETPROBE_TARGETS")) {
		go probeHTTPLoop(target, interval, timeout)
	}
	for _, target := range parseTargets(os.Getenv("NETPROBE_UDP_TARGETS")) {
		go probeUDPLoop(target, interval, timeout)
	}
	for _, target := range parseTargets(os.Getenv("NETPROBE_STREAM_TARGET")) {
		go streamLoop(target, timeout)
	}

	// Heartbeat, so a workload with neither role still proves it is running.
	// The address line repeats with it: a test reading this workload's address
	// out of its output must find it in a recent log window, not only in the
	// startup lines.
	for count := 1; ; count++ {
		logf("netprobe alive name=%s count=%d", name, count)
		time.Sleep(30 * time.Second)
		logAddresses(name)
	}
}

// logAddresses reports this run's own workload addresses, which is how a test
// on another node learns where to send traffic: cross-node DNS does not exist,
// and an `address()` env ref cannot cross a space boundary (references are
// own-space-or-global). The stable inbound address `I` is the one whose
// placement and run slots — the last 28 bits of the address ABI
// <ULA:48><space:16><deployment:24><ordinal:12><placementSlot:20><runSlot:8>
// — are zero; the other global address is this run's outbound `O`.
func logAddresses(name string) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logf("netprobe address lookup failed name=%s err=%v", name, err)
		return
	}
	var inbound, outbound string
	for _, entry := range addrs {
		prefix, ok := entry.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(prefix.IP)
		if !ok || !addr.Is6() || addr.Is4In6() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
			continue
		}
		if isInboundAddr(addr) {
			inbound = addr.String()
		} else {
			outbound = addr.String()
		}
	}
	logf("netprobe address name=%s inbound=%s outbound=%s", name, inbound, outbound)
}

func isInboundAddr(addr netip.Addr) bool {
	b := addr.As16()
	return b[12]&0x0f == 0 && b[13] == 0 && b[14] == 0 && b[15] == 0
}

type target struct {
	Label string
	Value string
}

// parseTargets expands `${VAR}` inside target values. Cross-node targets must
// be literal addresses (`.internal` names resolve only on the node holding the
// deployment), and the only supported way to obtain another deployment's
// stable inbound address is an `address()` env ref, which delivers a bare
// address — so the URL around it is composed here.
func parseTargets(raw string) []target {
	var targets []target
	for _, entry := range splitList(raw) {
		label, value, ok := strings.Cut(entry, "=")
		if !ok || label == "" || value == "" {
			fatalf("netprobe target %q must be <label>=<value>", entry)
		}
		expanded := os.ExpandEnv(value)
		if expanded == "" {
			fatalf("netprobe target %q expanded to an empty value", entry)
		}
		targets = append(targets, target{Label: label, Value: expanded})
	}
	return targets
}

// ---------------------------------------------------------------- server role

// signalReadyWhenListening reports readiness once every configured TCP port
// accepts a connection, so the server role can be rolled over: a candidate
// that has not yet bound its ports must not be promoted, because promotion
// moves the stable inbound address to it.
func signalReadyWhenListening(name string, listeners []string) {
	path := os.Getenv("OPENDEPLOY_READINESS_SOCK_PATH")
	if path == "" {
		return
	}
	for _, spec := range listeners {
		proto, port, _ := strings.Cut(spec, "/")
		if proto != "tcp" {
			continue
		}
		for {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("::1", port), time.Second)
			if err == nil {
				conn.Close()
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		fatalf("netprobe readiness signal failed name=%s err=%v", name, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ready\n")); err != nil {
		fatalf("netprobe readiness write failed name=%s err=%v", name, err)
	}
	logf("netprobe readiness sent name=%s", name)
}

func serveTCP(name, port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bulk"):
			serveBulk(w, r)
		case strings.HasSuffix(r.URL.Path, "/sse"):
			serveSSE(w, r, name, port)
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "netprobe=%s\nport=%s\npath=%s\nremote=%s\n", name, port, r.URL.Path, r.RemoteAddr)
		}
	})
	server := &http.Server{Addr: "[::]:" + port, Handler: mux}
	logf("netprobe listening name=%s protocol=tcp port=%s", name, port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatalf("netprobe listen failed protocol=tcp port=%s err=%v", port, err)
	}
}

// serveBulk writes an exact byte count so a truncated or stalled large
// response is distinguishable from a working one. Cross-node responses cross
// a tunnel with a reduced MTU, where a lost ICMPv6 packet-too-big shows up as
// a stall rather than an error.
func serveBulk(w http.ResponseWriter, r *http.Request) {
	size, err := strconv.Atoi(r.URL.Query().Get("bytes"))
	if err != nil || size < 0 {
		http.Error(w, "bytes must be a non-negative integer", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	chunk := make([]byte, 32*1024)
	for i := range chunk {
		chunk[i] = byte('a' + i%26)
	}
	for written := 0; written < size; {
		n := min(len(chunk), size-written)
		if _, err := w.Write(chunk[:n]); err != nil {
			return
		}
		written += n
	}
}

func serveSSE(w http.ResponseWriter, r *http.Request, name, port string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for tick := 1; ; tick++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
		fmt.Fprintf(w, "data: %s %s %d\n\n", name, port, tick)
		flusher.Flush()
	}
}

func serveUDP(name, port string) {
	addr, err := net.ResolveUDPAddr("udp", "[::]:"+port)
	if err != nil {
		fatalf("netprobe resolve failed protocol=udp port=%s err=%v", port, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fatalf("netprobe listen failed protocol=udp port=%s err=%v", port, err)
	}
	logf("netprobe listening name=%s protocol=udp port=%s", name, port)
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			logf("netprobe udp read failed port=%s err=%v", port, err)
			continue
		}
		reply := fmt.Sprintf("netprobe=%s port=%s echo=%s", name, port, string(buf[:n]))
		if _, err := conn.WriteToUDP([]byte(reply), from); err != nil {
			logf("netprobe udp write failed port=%s err=%v", port, err)
		}
	}
}

// ----------------------------------------------------------------- probe role

func probeHTTPLoop(t target, interval, timeout time.Duration) {
	for {
		probeHTTP(t, timeout)
		time.Sleep(interval)
	}
}

func probeHTTP(t target, timeout time.Duration) {
	parsed, err := url.Parse(t.Value)
	if err != nil {
		logf("netprobe probe=%s stage=parse result=error err=other detail=%v", t.Label, err)
		return
	}
	host, port := splitHostPort(parsed)
	addr, ok := resolve(t.Label, host, timeout)
	if !ok {
		return
	}

	dialed := atomic.Bool{}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			// Dial the address this probe resolved, so a connect failure is
			// attributable to that address and cannot silently fall back to a
			// different one from a second DNS answer.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
				if err == nil {
					dialed.Store(true)
				}
				return conn, err
			},
		},
	}
	response, err := client.Get(t.Value)
	if err != nil {
		stage := "connect"
		if dialed.Load() {
			stage = "request"
		}
		logf("netprobe probe=%s stage=%s result=error err=%s detail=%v", t.Label, stage, classify(err), err)
		return
	}
	defer response.Body.Close()
	bytes, err := io.Copy(io.Discard, response.Body)
	if err != nil {
		logf("netprobe probe=%s stage=body result=error err=%s detail=%v", t.Label, classify(err), err)
		return
	}
	logf("netprobe probe=%s result=ok status=%d bytes=%d", t.Label, response.StatusCode, bytes)
}

// resolve reports the DNS stage separately from the connect stage. A literal
// address is reported as resolved so both forms produce the same line shape:
// cross-node targets are literal, because `.internal` names resolve only on
// the node holding the deployment.
func resolve(label, host string, timeout time.Duration) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(host); err == nil {
		logf("netprobe probe=%s stage=dns result=ok addr=%s literal=true", label, addr)
		return addr, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		logf("netprobe probe=%s stage=dns result=error err=%s detail=%v", label, classify(err), err)
		return netip.Addr{}, false
	}
	logf("netprobe probe=%s stage=dns result=ok addr=%s literal=false", label, addrs[0])
	return addrs[0], true
}

func probeUDPLoop(t target, interval, timeout time.Duration) {
	for {
		probeUDP(t, timeout)
		time.Sleep(interval)
	}
}

func probeUDP(t target, timeout time.Duration) {
	conn, err := net.DialTimeout("udp", t.Value, timeout)
	if err != nil {
		logf("netprobe probe=%s stage=connect result=error err=%s detail=%v", t.Label, classify(err), err)
		return
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		logf("netprobe probe=%s stage=request result=error err=other detail=%v", t.Label, err)
		return
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		logf("netprobe probe=%s stage=request result=error err=%s detail=%v", t.Label, classify(err), err)
		return
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		logf("netprobe probe=%s stage=response result=error err=%s detail=%v", t.Label, classify(err), err)
		return
	}
	logf("netprobe probe=%s result=ok bytes=%d body=%s", t.Label, n, string(buf[:n]))
}

// streamLoop holds one SSE connection open and reports every tick. It is the
// oracle for flow-scoped policy changes: removing an allow must not tear down
// an established connection, so the tick counter has to keep advancing while
// fresh connections are failing.
func streamLoop(t target, timeout time.Duration) {
	for {
		streamOnce(t, timeout)
		time.Sleep(time.Second)
	}
}

func streamOnce(t target, timeout time.Duration) {
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
	}}
	response, err := client.Get(t.Value)
	if err != nil {
		logf("netprobe stream=%s result=error err=%s detail=%v", t.Label, classify(err), err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		logf("netprobe stream=%s result=error err=status detail=%d", t.Label, response.StatusCode)
		return
	}
	logf("netprobe stream=%s opened=true", t.Label)
	buf := make([]byte, 4096)
	ticks := 0
	for {
		n, err := response.Body.Read(buf)
		ticks += strings.Count(string(buf[:n]), "data: ")
		if ticks > 0 {
			logf("netprobe stream=%s tick=%d", t.Label, ticks)
		}
		if err != nil {
			logf("netprobe stream=%s closed=true ticks=%d err=%s detail=%v", t.Label, ticks, classify(err), err)
			return
		}
	}
}

// classify reduces an error to the vocabulary tests assert on. A policy drop
// is silent, so it always surfaces as `timeout`; every other class means the
// failure came from somewhere other than the boundary.
func classify(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "reset"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "unreachable"
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return "timeout"
		}
		if dnsErr.IsNotFound {
			return "notfound"
		}
		return "dnsfail"
	}
	return "other"
}

func splitHostPort(parsed *url.URL) (string, string) {
	host, port := parsed.Hostname(), parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return host, port
}

func splitList(raw string) []string {
	var out []string
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	ms, err := strconv.Atoi(value)
	if err != nil || ms <= 0 {
		fatalf("netprobe %s=%q must be a positive integer", name, value)
	}
	return time.Duration(ms) * time.Millisecond
}

func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
