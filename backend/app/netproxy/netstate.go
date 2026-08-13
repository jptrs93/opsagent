package netproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
)

type scheduledInstanceStore interface {
	FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState
	MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func())
}

type CertSecretResolver interface {
	ResolveSecret(id int32) (string, bool)
}

type CertSecretResolverFunc func(id int32) (string, bool)

func (f CertSecretResolverFunc) ResolveSecret(id int32) (string, bool) { return f(id) }

// RunNetStateWriter writes full protobuf netstate snapshots for the local
// netproxy process. It is intentionally full-state and idempotent.
func RunNetStateWriter(ctx context.Context, store scheduledInstanceStore, predicate storage.ScheduledInstancePredicate, nodeIdentifier, path string, certs CertSecretResolver, acme *acmestate.Holder, ensureSecrets func(context.Context, []int32) error) {
	bundlePath := filepath.Join(filepath.Dir(path), CertBundleFileName)
	seq := initialNetStateSequence(path)
	var acmeUpdates <-chan *apigen.AcmeState
	if acme != nil {
		var unsubAcme func()
		_, acmeUpdates, unsubAcme = acme.SnapshotAndSubscribe()
		defer unsubAcme()
	}
	write := func(items []apigen.ScheduledInstanceState) {
		seq++
		var acmeState *apigen.AcmeState
		if acme != nil {
			acmeState = acme.Get()
		}
		state := RenderNetState(seq, nodeIdentifier, items, acmeState)
		if err := WriteCertBundle(bundlePath, RenderCertBundle(ctx, seq, items, certs, acmestate.Bindings(acmeState), ensureSecrets)); err != nil {
			slog.Warn("writing netproxy cert bundle failed", "path", bundlePath, "err", err)
		}
		if err := WriteNetState(path, state); err != nil {
			slog.Warn("writing netproxy netstate failed", "path", path, "err", err)
			return
		}
		if err := network.Default.SetNetproxyIngress(state.Ingress); err != nil {
			slog.Warn("reconciling netproxy ingress forwarding failed", "err", err)
		}
	}
	snapshot, updates, unsub := store.MustFetchScheduledSnapshotAndSubscribe(predicate)
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
			write(store.FetchScheduledSnapshot(predicate))
		case _, ok := <-acmeUpdates:
			if !ok {
				acmeUpdates = nil
				continue
			}
			write(store.FetchScheduledSnapshot(predicate))
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

// RenderNetState derives node-local DNS and ingress from the placements this
// node holds.
//
// Endpoints are derived, not reported: an instance's stable inbound address is
// a pure function of its space, deployment, and ordinal, so the only thing the
// runner contributes is whether it is up. Readiness still gates both DNS and
// ingress backends, which is what keeps a name from resolving to, or a proxy
// from dialling, a container that is not running.
func RenderNetState(seq int64, nodeIdentifier string, items []apigen.ScheduledInstanceState, acme *apigen.AcmeState) *apigen.NetState {
	prefix, _ := network.Default.PrefixValue()
	services := make([]*apigen.DnsService, 0, len(items))
	ingress := make([]*apigen.NetIngress, 0)
	for _, item := range items {
		if item.Config.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		ready := readyEndpoints(prefix, item)
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
	var challenges []*apigen.AcmeHttpChallenge
	if acme != nil {
		for _, challenge := range acme.Challenges {
			if challenge == nil || challenge.Token == "" {
				continue
			}
			challenges = append(challenges, challenge)
		}
	}
	return &apigen.NetState{
		Seq:               seq,
		UlaPrefix:         prefix.Bytes(),
		NodeIdentifier:    nodeIdentifier,
		DnsServices:       services,
		UpstreamResolvers: hostResolvers(),
		Ingress:           ingress,
		AcmeChallenges:    challenges,
	}
}

func renderIngress(item apigen.ScheduledInstanceState, endpoints []*apigen.Endpoint) []*apigen.NetIngress {
	var out []*apigen.NetIngress
	for _, route := range item.Config.Spec.Networking.Ingress {
		if route == nil {
			continue
		}
		hostname := ingressHostname(route.Hostname)
		if hostname == "" {
			continue
		}
		switch route.Kind {
		case apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH:
			if route.TlsPassthroughConfig == nil {
				continue
			}
			port := route.TlsPassthroughConfig.HostPort
			if port == 0 {
				port = 443
			}
			if port == netproxyDNSPort || port < 1 || port > 65535 || route.TlsPassthroughConfig.ContainerPort < 1 || route.TlsPassthroughConfig.ContainerPort > 65535 {
				continue
			}
			out = append(out, &apigen.NetIngress{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: hostname,
				TlsPassthrough: &apigen.TlsPassthroughNetIngress{
					HostPort: port,
					Backends: ingressBackends(endpoints, route.TlsPassthroughConfig.ContainerPort),
				},
			})
		case apigen.IngressKind_INGRESS_KIND_HTTPS:
			cfg := route.HttpsConfig
			if cfg == nil || cfg.ContainerPort < 1 || cfg.ContainerPort > 65535 {
				continue
			}
			prefix := cfg.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			out = append(out, &apigen.NetIngress{
				Kind:     apigen.IngressKind_INGRESS_KIND_HTTPS,
				Hostname: hostname,
				Https: &apigen.HttpsNetIngress{
					PathPrefix:          prefix,
					StripPrefix:         cfg.StripPrefix,
					BackendProtocol:     cfg.BackendProtocol,
					MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
					FlushIntervalMs:     cfg.FlushIntervalMs,
					CertID:              HTTPSCertID(cfg, hostname),
					Backends:            ingressBackends(endpoints, cfg.ContainerPort),
				},
			})
		}
	}
	return out
}

func ingressBackends(endpoints []*apigen.Endpoint, containerPort int32) []*apigen.IngressBackend {
	backends := make([]*apigen.IngressBackend, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Address == "" {
			continue
		}
		backends = append(backends, &apigen.IngressBackend{
			Address: endpoint.Address,
			Port:    containerPort,
		})
	}
	return backends
}

func HTTPSCertID(cfg *apigen.HttpsConfig, hostname string) string {
	if cfg != nil && cfg.CertSource != nil && cfg.CertSource.Secret != nil {
		return "secret:" + strconv.Itoa(int(cfg.CertSource.Secret.SecretVersionID))
	}
	return "acme:" + hostname
}

const CertBundleFileName = "certbundle.pb"

func RenderCertBundle(ctx context.Context, seq int64, items []apigen.ScheduledInstanceState, certs CertSecretResolver, acmeBindings map[string]int32, ensureSecrets func(context.Context, []int32) error) *apigen.CertBundle {
	wanted := map[string]int32{}
	for _, item := range items {
		if item.Config.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		for _, route := range item.Config.Spec.Networking.Ingress {
			if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_HTTPS || route.HttpsConfig == nil {
				continue
			}
			hostname := ingressHostname(route.Hostname)
			id := HTTPSCertID(route.HttpsConfig, hostname)
			if _, ok := wanted[id]; ok {
				continue
			}
			source := route.HttpsConfig.CertSource
			if source != nil && source.Secret != nil {
				if source.Secret.SecretVersionID > 0 {
					wanted[id] = source.Secret.SecretVersionID
				}
				continue
			}
			if secretVersionID, ok := acmeBindings[hostname]; ok {
				wanted[id] = secretVersionID
			}
		}
	}
	if len(wanted) > 0 && ensureSecrets != nil {
		ids := make([]int32, 0, len(wanted))
		for _, id := range wanted {
			ids = append(ids, id)
		}
		if err := ensureSecrets(ctx, ids); err != nil {
			slog.Warn("fetching netproxy cert secrets failed", "err", err)
		}
	}
	bundle := &apigen.CertBundle{Seq: seq}
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if certs == nil {
			continue
		}
		value, ok := certs.ResolveSecret(wanted[id])
		if !ok {
			slog.Warn("netproxy cert secret is not resolvable yet", "cert_id", id)
			continue
		}
		bundle.Certs = append(bundle.Certs, &apigen.CertBundleEntry{CertID: id, Pem: []byte(value)})
	}
	return bundle
}

func WriteCertBundle(path string, bundle *apigen.CertBundle) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, bundle.Encode(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// readyEndpoints derives the instance's endpoint set, which is empty unless the
// placement is both serving and running. A standby warming up for a takeover is
// deliberately absent: it holds no inbound address yet, so publishing it would
// send traffic to an address that does not route here.
func readyEndpoints(prefix network.Prefix, item apigen.ScheduledInstanceState) []*apigen.Endpoint {
	if prefix.IsZero() || item.Config.ID <= 0 ||
		item.Instance.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING ||
		item.Status.Runner.Status != apigen.RunningStatus_RUNNING {
		return nil
	}
	addr, err := prefix.InboundAddr(item.Config.Identity.SpaceID, item.Config.ID, item.Instance.InstanceOrdinal)
	if err != nil {
		return nil
	}
	return []*apigen.Endpoint{{
		Ordinal: item.Instance.InstanceOrdinal,
		Address: addr.String(),
		NodeID:  item.Instance.NodeID,
		State:   apigen.EndpointState_ENDPOINT_READY,
	}}
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
	return "space-" + strconv.Itoa(int(id))
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
