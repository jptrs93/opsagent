package netproxy

import (
	"bytes"
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

	"github.com/jptrs93/goutil/logu"
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

type ClusterNetMapSource interface {
	SnapshotAndSubscribe() (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func())
}

type ClusterNetMapSourceFunc func() (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func())

func (f ClusterNetMapSourceFunc) SnapshotAndSubscribe() (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	return f()
}

// RunNetStateWriter writes full protobuf netstate snapshots for the local
// netproxy process. It is intentionally full-state and idempotent. Each output
// file carries its own sequence and is rewritten only when its rendered
// content changed. Host-port forwarding to netproxy is not derived here: the
// primary evaluates each route's listen selectors and distributes the publish
// set in the cluster network map.
func RunNetStateWriter(ctx context.Context, store scheduledInstanceStore, predicate storage.ScheduledInstancePredicate, nodeIdentifier, path string, certs CertSecretResolver, acme *acmestate.Holder, netMaps ClusterNetMapSource, ensureSecrets func(context.Context, []int32) error) {
	ctx = logu.AddTag(ctx, "NetStateWriter")
	bundlePath := filepath.Join(filepath.Dir(path), CertBundleFileName)
	netSeq := initialArtifactSequence(ctx, path, func(b []byte) (int64, error) {
		state, err := apigen.DecodeNetState(b)
		if err != nil {
			return 0, err
		}
		return state.Seq, nil
	})
	bundleSeq := initialArtifactSequence(ctx, bundlePath, func(b []byte) (int64, error) {
		bundle, err := apigen.DecodeCertBundle(b)
		if err != nil {
			return 0, err
		}
		return bundle.Seq, nil
	})
	var lastState, lastBundle []byte
	var acmeUpdates <-chan *apigen.AcmeState
	if acme != nil {
		var unsubAcme func()
		_, acmeUpdates, unsubAcme = acme.SnapshotAndSubscribe()
		defer unsubAcme()
	}
	var clusterMap *apigen.ClusterNetMap
	var netMapUpdates <-chan *apigen.ClusterNetMap
	if netMaps != nil {
		var unsubNetMaps func()
		clusterMap, netMapUpdates, unsubNetMaps = netMaps.SnapshotAndSubscribe()
		defer unsubNetMaps()
	}
	write := func(items []apigen.ScheduledInstanceState) {
		var acmeState *apigen.AcmeState
		if acme != nil {
			acmeState = acme.Get()
		}
		bundle := RenderCertBundle(ctx, 0, items, certs, acmestate.Bindings(acmeState), ensureSecrets)
		if b := bundle.Encode(); !bytes.Equal(b, lastBundle) {
			bundleSeq++
			bundle.Seq = bundleSeq
			if err := WriteCertBundle(bundlePath, bundle); err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("writing netproxy cert bundle %s failed", bundlePath), "err", err)
			} else {
				lastBundle = b
			}
		}
		state := RenderNetState(0, nodeIdentifier, items, acmeState, clusterMap)
		if b := state.Encode(); !bytes.Equal(b, lastState) {
			netSeq++
			state.Seq = netSeq
			if err := WriteNetState(path, state); err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("writing netproxy netstate %s failed", path), "err", err)
				return
			}
			lastState = b
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
		case next, ok := <-netMapUpdates:
			if !ok {
				netMapUpdates = nil
				continue
			}
			if next != nil {
				clusterMap = next
			}
			write(store.FetchScheduledSnapshot(predicate))
		}
	}
}

// initialArtifactSequence floors the sequence at the current unix
// milliseconds: netproxy runs in a separate process whose monotonic seq gates
// survive an agent state-dir wipe, so restarting the count from the file (or
// zero) would leave a running netproxy silently dropping every update until
// its own restart.
func initialArtifactSequence(ctx context.Context, path string, seqOf func([]byte) (int64, error)) int64 {
	now := time.Now().UnixMilli()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return now
	}
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("reading existing netproxy state file %s failed", path), "err", err)
		return now
	}
	seq, err := seqOf(b)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("decoding existing netproxy state file %s failed", path), "err", err)
		return now
	}
	return max(seq, now)
}

// RenderNetState derives DNS and ingress as a pure function of target state:
// an endpoint is published for every ordinal that is supposed to be serving,
// whether or not its workload is currently up. The cluster map's catalog
// covers all nodes; without a catalog the render falls back to the placements
// this node holds. Every catalogued service contributes its DNS name, so the
// DNS server can answer authoritatively for a service that exists but has no
// established ordinal instead of leaking the lookup upstream.
func RenderNetState(seq int64, nodeIdentifier string, items []apigen.ScheduledInstanceState, acme *apigen.AcmeState, clusterMap *apigen.ClusterNetMap) *apigen.NetState {
	prefix, _ := network.Default.PrefixValue()
	virtual := make([]apigen.ScheduledInstanceState, 0, len(items))
	for _, item := range items {
		if item.Config.Def.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		virtual = append(virtual, item)
	}
	sort.SliceStable(virtual, func(i, j int) bool { return virtual[i].Instance.ID < virtual[j].Instance.ID })

	services := make([]*apigen.DnsService, 0)
	serviceByName := make(map[string]*apigen.DnsService)
	addService := func(name, env string, endpoints []*apigen.Endpoint) {
		svc := serviceByName[env+"|"+name]
		if svc == nil {
			svc = &apigen.DnsService{Name: name, Environment: env}
			serviceByName[env+"|"+name] = svc
			services = append(services, svc)
		}
		svc.Endpoints = appendNewEndpoints(svc.Endpoints, endpoints)
	}
	endpointsByDeployment := make(map[int32][]*apigen.Endpoint)
	if clusterMap != nil && len(clusterMap.DnsServices) > 0 {
		for _, service := range clusterMap.DnsServices {
			if service == nil || service.Name == "" {
				continue
			}
			endpoints := make([]*apigen.Endpoint, 0, len(service.Ordinals))
			for _, ordinal := range service.Ordinals {
				if ordinal == nil {
					continue
				}
				addr, err := prefix.InboundAddr(service.SpaceID, service.DeploymentID, ordinal.Ordinal)
				if err != nil {
					continue
				}
				endpoints = append(endpoints, &apigen.Endpoint{
					Ordinal: ordinal.Ordinal,
					Address: addr.String(),
					State:   apigen.EndpointState_ENDPOINT_READY,
				})
			}
			endpointsByDeployment[service.DeploymentID] = appendNewEndpoints(endpointsByDeployment[service.DeploymentID], endpoints)
			addService(service.Name, network.SpaceDNSName(service.SpaceID), endpoints)
		}
	} else {
		type ordinalKey struct{ deploymentID, ordinal int32 }
		type ordinalStates struct{ serving, standby, draining bool }
		statesByOrdinal := make(map[ordinalKey]*ordinalStates)
		for _, item := range virtual {
			if item.Config.ID <= 0 {
				continue
			}
			key := ordinalKey{item.Config.ID, item.Instance.InstanceOrdinal}
			states := statesByOrdinal[key]
			if states == nil {
				states = &ordinalStates{}
				statesByOrdinal[key] = states
			}
			switch item.Instance.State {
			case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING:
				states.serving = true
			case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY:
				states.standby = true
			case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING:
				states.draining = true
			}
		}
		for _, item := range virtual {
			if item.Config.ID <= 0 {
				continue
			}
			states := statesByOrdinal[ordinalKey{item.Config.ID, item.Instance.InstanceOrdinal}]
			if !states.serving && !(states.standby && states.draining) {
				continue
			}
			addr, err := prefix.InboundAddr(item.Config.Def.SpaceID, item.Config.ID, item.Instance.InstanceOrdinal)
			if err != nil {
				continue
			}
			endpoint := &apigen.Endpoint{
				Ordinal: item.Instance.InstanceOrdinal,
				Address: addr.String(),
				State:   apigen.EndpointState_ENDPOINT_READY,
			}
			endpointsByDeployment[item.Config.ID] = appendNewEndpoints(endpointsByDeployment[item.Config.ID], []*apigen.Endpoint{endpoint})
		}
		for _, item := range virtual {
			name := network.DNSLabel(item.Config.Def.Name)
			if name == "" {
				continue
			}
			addService(name, network.SpaceDNSName(item.Config.Def.SpaceID), endpointsByDeployment[item.Config.ID])
		}
	}

	ingress := make([]*apigen.NetIngress, 0)
	ingressByRoute := make(map[string]*apigen.NetIngress)
	for _, item := range virtual {
		for _, route := range renderIngress(item, endpointsByDeployment[item.Config.ID]) {
			key := ingressRouteKey(route)
			existing := ingressByRoute[key]
			if existing == nil {
				ingressByRoute[key] = route
				ingress = append(ingress, route)
				continue
			}
			mergeIngressBackends(existing, route)
		}
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Environment != services[j].Environment {
			return services[i].Environment < services[j].Environment
		}
		return services[i].Name < services[j].Name
	})
	sort.Slice(ingress, func(i, j int) bool { return ingressRouteKey(ingress[i]) < ingressRouteKey(ingress[j]) })
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
	for _, route := range item.Config.Def.Spec.Networking.Ingress {
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

func ingressRouteKey(route *apigen.NetIngress) string {
	switch route.Kind {
	case apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH:
		return "tls|" + route.Hostname + "|" + strconv.Itoa(int(route.TlsPassthrough.HostPort))
	case apigen.IngressKind_INGRESS_KIND_HTTPS:
		return "https|" + route.Hostname + "|" + route.Https.PathPrefix
	}
	return ""
}

func mergeIngressBackends(dst, src *apigen.NetIngress) {
	switch dst.Kind {
	case apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH:
		dst.TlsPassthrough.Backends = appendNewBackends(dst.TlsPassthrough.Backends, src.TlsPassthrough.Backends)
	case apigen.IngressKind_INGRESS_KIND_HTTPS:
		dst.Https.Backends = appendNewBackends(dst.Https.Backends, src.Https.Backends)
	}
}

func appendNewBackends(dst, src []*apigen.IngressBackend) []*apigen.IngressBackend {
	for _, backend := range src {
		duplicate := false
		for _, have := range dst {
			if have.Address == backend.Address && have.Port == backend.Port {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, backend)
		}
	}
	return dst
}

func appendNewEndpoints(dst, src []*apigen.Endpoint) []*apigen.Endpoint {
	for _, endpoint := range src {
		duplicate := false
		for _, have := range dst {
			if have.Ordinal == endpoint.Ordinal && have.Address == endpoint.Address {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, endpoint)
		}
	}
	return dst
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
		if item.Config.Def.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		for _, route := range item.Config.Def.Spec.Networking.Ingress {
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
			slog.WarnContext(ctx, "fetching netproxy cert secrets failed", "err", err)
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
			slog.WarnContext(ctx, fmt.Sprintf("netproxy cert secret %s is not resolvable yet", id))
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
