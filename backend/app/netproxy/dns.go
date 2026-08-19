package netproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/time/rate"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const maxConcurrentDNSForwards = 256

// RunDNS answers .internal AAAA queries from NetState snapshots locally and
// forwards all unmatched queries to the configured upstream resolvers.
func RunDNS(ctx context.Context, states netStateSubscriber, listen string) error {
	if listen == "" {
		listen = ":53"
	}
	s := &dnsServer{
		warningLimiter: newOperationalWarningLimiter(),
		forwardSlots:   make(chan struct{}, maxConcurrentDNSForwards),
	}
	snapshot, updates, unsubscribe := states.SnapshotAndSubscribe()
	defer unsubscribe()
	s.setState(snapshot)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handle)
	started := make(chan struct{}, 2)
	udp := &dns.Server{Addr: listen, Net: "udp", Handler: mux, NotifyStartedFunc: func() { started <- struct{}{} }}
	tcp := &dns.Server{Addr: listen, Net: "tcp", Handler: mux, NotifyStartedFunc: func() { started <- struct{}{} }}
	errCh := make(chan error, 2)
	go func() { errCh <- udp.ListenAndServe() }()
	go func() { errCh <- tcp.ListenAndServe() }()

	startedCount := 0
	for {
		select {
		case <-ctx.Done():
			_ = udp.Shutdown()
			_ = tcp.Shutdown()
			return nil
		case next := <-updates:
			s.setState(next)
		case <-started:
			startedCount++
			if startedCount == 2 {
				slog.Info("DNS listeners started", "address", listen, "networks", "udp,tcp")
			}
		case err := <-errCh:
			_ = udp.Shutdown()
			_ = tcp.Shutdown()
			if err == nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

type dnsServer struct {
	state          atomic.Pointer[apigen.NetState]
	warningLimiter *rate.Limiter
	forwardSlots   chan struct{}
}

func (s *dnsServer) setState(next *apigen.NetState) {
	if next == nil {
		return
	}
	cur := s.state.Load()
	if cur != nil && next.Seq <= cur.Seq {
		return
	}
	s.state.Store(next)
}

func (s *dnsServer) handle(w dns.ResponseWriter, r *dns.Msg) {
	res := new(dns.Msg)
	res.SetReply(r)
	if len(r.Question) == 0 {
		_ = w.WriteMsg(res)
		return
	}
	q := r.Question[0]
	answers, known := s.lookupAAAA(q.Name)
	if (q.Qtype == dns.TypeAAAA || q.Qtype == dns.TypeANY) && len(answers) > 0 {
		res.Authoritative = true
		for _, addr := range answers {
			res.Answer = append(res.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 5},
				AAAA: net.ParseIP(addr),
			})
		}
		_ = w.WriteMsg(res)
		return
	}
	if known {
		res.Authoritative = true
		_ = w.WriteMsg(res)
		return
	}
	if s.forward(w, r) {
		return
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(q.Name, ".")), ".internal") {
		res.Rcode = dns.RcodeNameError
	} else {
		res.Rcode = dns.RcodeServerFailure
	}
	_ = w.WriteMsg(res)
}

// lookupAAAA resolves name against the local catalog. known reports whether
// the name belongs to a catalogued service on this node — for those the server
// is authoritative even with no live endpoints, answering empty NOERROR
// instead of leaking the query upstream.
func (s *dnsServer) lookupAAAA(name string) (answers []string, known bool) {
	state := s.state.Load()
	if state == nil {
		return nil, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".")
	if len(parts) < 3 || parts[len(parts)-1] != "internal" {
		return nil, false
	}
	ordinal := int32(-1)
	servicePart := 0
	if len(parts) == 4 {
		var parsed int
		if _, err := fmt.Sscanf(parts[0], "%d", &parsed); err != nil {
			return nil, false
		}
		ordinal = int32(parsed)
		servicePart = 1
	} else if len(parts) != 3 {
		return nil, false
	}
	serviceName, env := parts[servicePart], parts[servicePart+1]
	for _, svc := range state.DnsServices {
		if svc == nil || svc.Name != serviceName || svc.Environment != env {
			continue
		}
		known = true
		for _, ep := range svc.Endpoints {
			if ep == nil || ep.State != apigen.EndpointState_ENDPOINT_READY {
				continue
			}
			if ordinal >= 0 && ep.Ordinal != ordinal {
				continue
			}
			answers = append(answers, ep.Address)
		}
	}
	return answers, known
}

func (s *dnsServer) forward(w dns.ResponseWriter, r *dns.Msg) bool {
	state := s.state.Load()
	if state == nil {
		return false
	}
	if s.forwardSlots != nil {
		select {
		case s.forwardSlots <- struct{}{}:
			defer func() { <-s.forwardSlots }()
		default:
			if allowOperationalWarning(s.warningLimiter) {
				slog.Warn("DNS forwarding concurrency limit reached", "limit", cap(s.forwardSlots))
			}
			return false
		}
	}
	client := &dns.Client{Timeout: 2 * time.Second}
	var lastErr error
	for _, upstream := range state.UpstreamResolvers {
		upstream = dnsResolverAddress(upstream)
		res, _, err := client.Exchange(r, upstream)
		if err != nil {
			lastErr = err
			continue
		}
		if err := w.WriteMsg(res); err != nil {
			slog.Debug("writing forwarded DNS response failed", "err", err)
		}
		return true
	}
	if lastErr != nil && allowOperationalWarning(s.warningLimiter) {
		attrs := []any{"resolver_count", len(state.UpstreamResolvers), "err", lastErr}
		if len(r.Question) > 0 {
			attrs = append(attrs, "name", r.Question[0].Name, "type", r.Question[0].Qtype)
		}
		slog.Warn("forwarding DNS query to all upstream resolvers failed", attrs...)
	}
	return false
}

func dnsResolverAddress(upstream string) string {
	if _, _, err := net.SplitHostPort(upstream); err == nil {
		return upstream
	}
	if addr, err := netip.ParseAddr(upstream); err == nil {
		return net.JoinHostPort(addr.String(), "53")
	}
	return net.JoinHostPort(upstream, "53")
}
