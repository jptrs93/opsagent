package netproxy

import (
	"bytes"
	"log/slog"
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
