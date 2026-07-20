package netproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
)

type deploymentStore interface {
	FetchDeploymentSnapshot(predicate storage.DeploymentPredicate) []apigen.DeploymentWithStatus
	MustFetchSnapshotAndSubscribe(predicate storage.DeploymentPredicate) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func())
}

// RunNetStateWriter writes full protobuf netstate snapshots for the local
// netproxy process. It is intentionally full-state and idempotent.
func RunNetStateWriter(ctx context.Context, store deploymentStore, predicate storage.DeploymentPredicate, nodeIdentifier, path string) {
	seq := initialNetStateSequence(path)
	write := func(items []apigen.DeploymentWithStatus) {
		seq++
		state := RenderNetState(seq, nodeIdentifier, items)
		if err := WriteNetState(path, state); err != nil {
			slog.Warn("writing netproxy netstate failed", "path", path, "err", err)
			return
		}
		if err := network.Default.SetNetproxyIngress(state.Ingress); err != nil {
			slog.Warn("reconciling netproxy ingress forwarding failed", "err", err)
		}
	}
	snapshot, updates, unsub := store.MustFetchSnapshotAndSubscribe(predicate)
	defer unsub()
	write(snapshot)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			write(store.FetchDeploymentSnapshot(predicate))
		}
	}
}

func initialNetStateSequence(path string) int64 {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		slog.Warn("reading existing netproxy netstate failed", "path", path, "err", err)
		return 0
	}
	state, err := apigen.DecodeNetState(b)
	if err != nil {
		slog.Warn("decoding existing netproxy netstate failed", "path", path, "err", err)
		return 0
	}
	return max(state.Seq, 0)
}

func RenderNetState(seq int64, nodeIdentifier string, items []apigen.DeploymentWithStatus) *apigen.NetState {
	prefix, _ := network.Default.PrefixValue()
	services := make([]*apigen.DnsService, 0, len(items))
	ingress := make([]*apigen.NetIngress, 0)
	for _, item := range items {
		if item.Config.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		ready := readyEndpoints(item.Status.Runner.Endpoints)
		ingress = append(ingress, renderIngress(item, ready)...)
		if len(ready) == 0 {
			continue
		}
		services = append(services, &apigen.DnsService{
			Name:        dnsLabel(item.Config.Identity.Name),
			Environment: spaceDNSName(item.Config.Identity.SpaceID),
			Endpoints:   ready,
		})
	}
	return &apigen.NetState{
		Seq:               seq,
		UlaPrefix:         prefix.Bytes(),
		NodeIdentifier:    nodeIdentifier,
		DnsServices:       services,
		UpstreamResolvers: hostResolvers(),
		Ingress:           ingress,
	}
}

func renderIngress(item apigen.DeploymentWithStatus, endpoints []*apigen.Endpoint) []*apigen.NetIngress {
	var out []*apigen.NetIngress
	for _, route := range item.Config.Spec.Networking.Ingress {
		if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH || route.TlsPassthroughConfig == nil {
			continue
		}
		hostname := ingressHostname(route.Hostname)
		if hostname == "" {
			continue
		}
		port := route.TlsPassthroughConfig.HostPort
		if port == 0 {
			port = 443
		}
		if port == netproxyDNSPort || port < 1 || port > 65535 || route.TlsPassthroughConfig.ContainerPort < 1 || route.TlsPassthroughConfig.ContainerPort > 65535 {
			continue
		}
		backends := make([]*apigen.IngressBackend, 0, len(endpoints))
		for _, endpoint := range endpoints {
			if endpoint.Address == "" {
				continue
			}
			backends = append(backends, &apigen.IngressBackend{
				Address: endpoint.Address,
				Port:    route.TlsPassthroughConfig.ContainerPort,
			})
		}
		out = append(out, &apigen.NetIngress{
			Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
			Hostname: hostname,
			TlsPassthrough: &apigen.TlsPassthroughNetIngress{
				HostPort: port,
				Backends: backends,
			},
		})
	}
	return out
}

func ingressHostname(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func WriteNetState(path string, state *apigen.NetState) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, state.Encode(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readyEndpoints(in []*apigen.Endpoint) []*apigen.Endpoint {
	out := make([]*apigen.Endpoint, 0, len(in))
	for _, ep := range in {
		if ep != nil && ep.State == apigen.EndpointState_ENDPOINT_READY {
			readyEndpoint := *ep
			out = append(out, &readyEndpoint)
		}
	}
	return out
}

func dnsLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' && !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "deployment"
	}
	return out
}

func spaceDNSName(id int32) string {
	switch id {
	case 0:
		return "opendeploy"
	case 1:
		return "default"
	default:
		return "space-" + strconv.Itoa(int(id))
	}
}

func hostResolvers() []string {
	return hostResolversFromFiles(
		"/etc/resolv.conf",
		"/run/systemd/resolve/resolv.conf",
		"/run/NetworkManager/resolv.conf",
	)
}

func hostResolversFromFiles(paths ...string) []string {
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var resolvers []string
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[0] != "nameserver" {
				continue
			}
			addr, err := netip.ParseAddr(fields[1])
			if err != nil || addr.IsLoopback() || addr.IsUnspecified() {
				continue
			}
			resolvers = append(resolvers, addr.String())
		}
		if len(resolvers) > 0 {
			return resolvers
		}
	}
	return nil
}
