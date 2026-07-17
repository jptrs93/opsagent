package netproxy

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"golang.org/x/time/rate"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDNSForwardLogsRateLimitedUpstreamFailure(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	server := &dnsServer{warningLimiter: rate.NewLimiter(0, 1)}
	server.state.Store(&apigen.NetState{UpstreamResolvers: []string{"127.0.0.1:notaport"}})
	request := new(dns.Msg)
	request.SetQuestion("example.com.", dns.TypeAAAA)

	server.forward(nil, request)
	server.forward(nil, request)

	got := output.String()
	if count := strings.Count(got, "forwarding DNS query to all upstream resolvers failed"); count != 1 {
		t.Fatalf("warning count = %d, want 1; output: %q", count, got)
	}
	for _, want := range []string{"resolver_count=1", "name=example.com.", "type=28"} {
		if !strings.Contains(got, want) {
			t.Errorf("log output %q does not contain %q", got, want)
		}
	}
}

func TestDNSForwardRejectsWorkAtConcurrencyLimit(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	server := &dnsServer{
		warningLimiter: rate.NewLimiter(rate.Inf, 1),
		forwardSlots:   make(chan struct{}, 1),
	}
	server.forwardSlots <- struct{}{}
	server.state.Store(&apigen.NetState{UpstreamResolvers: []string{"192.0.2.53"}})
	request := new(dns.Msg)
	request.SetQuestion("example.com.", dns.TypeAAAA)

	if server.forward(nil, request) {
		t.Fatal("forward succeeded while all concurrency slots were occupied")
	}
	if got := output.String(); !strings.Contains(got, "DNS forwarding concurrency limit reached") || !strings.Contains(got, "limit=1") {
		t.Fatalf("unexpected log output: %q", got)
	}
}

func TestDNSResolverAddressAddsDefaultPort(t *testing.T) {
	for _, test := range []struct {
		upstream string
		want     string
	}{
		{upstream: "192.0.2.53", want: "192.0.2.53:53"},
		{upstream: "2001:db8::53", want: "[2001:db8::53]:53"},
		{upstream: "[2001:db8::53]:5353", want: "[2001:db8::53]:5353"},
		{upstream: "resolver.example.com", want: "resolver.example.com:53"},
	} {
		if got := dnsResolverAddress(test.upstream); got != test.want {
			t.Errorf("dnsResolverAddress(%q) = %q, want %q", test.upstream, got, test.want)
		}
	}
}

func TestDNSReturnsServerFailureWithoutExternalResolver(t *testing.T) {
	server := &dnsServer{}
	server.state.Store(&apigen.NetState{})
	request := new(dns.Msg)
	request.SetQuestion("example.com.", dns.TypeAAAA)
	w := &testDNSResponseWriter{}

	server.handle(w, request)

	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("response = %+v, want SERVFAIL", w.msg)
	}
}

type testDNSResponseWriter struct {
	msg *dns.Msg
}

func (w *testDNSResponseWriter) LocalAddr() net.Addr         { return nil }
func (w *testDNSResponseWriter) RemoteAddr() net.Addr        { return nil }
func (w *testDNSResponseWriter) Close() error                { return nil }
func (w *testDNSResponseWriter) TsigStatus() error           { return nil }
func (w *testDNSResponseWriter) TsigTimersOnly(bool)         {}
func (w *testDNSResponseWriter) Hijack()                     {}
func (w *testDNSResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *testDNSResponseWriter) WriteMsg(msg *dns.Msg) error {
	w.msg = msg
	return nil
}
