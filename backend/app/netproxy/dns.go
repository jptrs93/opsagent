package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// RunDNS serves the netproxy DNS process. It consumes NetState snapshots and
// answers .internal AAAA queries locally, forwarding all unmatched queries to
// the configured upstream resolvers.
func RunDNS(ctx context.Context, states netStateSubscriber, listen string) error {
	if listen == "" {
		listen = ":53"
	}
	s := &dnsServer{}
	snapshot, updates, unsubscribe := states.SnapshotAndSubscribe()
	defer unsubscribe()
	s.setState(snapshot)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handle)
	udp := &dns.Server{Addr: listen, Net: "udp", Handler: mux}
	tcp := &dns.Server{Addr: listen, Net: "tcp", Handler: mux}
	errCh := make(chan error, 2)
	go func() { errCh <- udp.ListenAndServe() }()
	go func() { errCh <- tcp.ListenAndServe() }()

	for {
		select {
		case <-ctx.Done():
			_ = udp.Shutdown()
			_ = tcp.Shutdown()
			return nil
		case next := <-updates:
			s.setState(next)
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
	state atomic.Pointer[apigen.NetState]
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
	if q.Qtype == dns.TypeAAAA || q.Qtype == dns.TypeANY {
		if answers := s.lookupAAAA(q.Name); len(answers) > 0 {
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
	}
	if s.forward(w, r) {
		return
	}
	res.Rcode = dns.RcodeNameError
	_ = w.WriteMsg(res)
}

func (s *dnsServer) lookupAAAA(name string) []string {
	state := s.state.Load()
	if state == nil {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".")
	if len(parts) < 3 || parts[len(parts)-1] != "internal" {
		return nil
	}
	ordinal := int32(-1)
	servicePart := 0
	if len(parts) == 4 {
		var parsed int
		if _, err := fmt.Sscanf(parts[0], "%d", &parsed); err != nil {
			return nil
		}
		ordinal = int32(parsed)
		servicePart = 1
	} else if len(parts) != 3 {
		return nil
	}
	serviceName, env := parts[servicePart], parts[servicePart+1]
	var out []string
	for _, svc := range state.DnsServices {
		if svc == nil || svc.Name != serviceName || svc.Environment != env {
			continue
		}
		for _, ep := range svc.Endpoints {
			if ep == nil || ep.State != apigen.EndpointState_ENDPOINT_READY {
				continue
			}
			if ordinal >= 0 && ep.Ordinal != ordinal {
				continue
			}
			out = append(out, ep.Address)
		}
	}
	return out
}

func (s *dnsServer) forward(w dns.ResponseWriter, r *dns.Msg) bool {
	state := s.state.Load()
	if state == nil {
		return false
	}
	client := &dns.Client{Timeout: 2 * time.Second}
	for _, upstream := range state.UpstreamResolvers {
		if !strings.Contains(upstream, ":") {
			upstream = net.JoinHostPort(upstream, "53")
		}
		res, _, err := client.Exchange(r, upstream)
		if err != nil {
			continue
		}
		_ = w.WriteMsg(res)
		return true
	}
	return false
}
