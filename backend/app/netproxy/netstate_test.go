package netproxy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
)

type netStateWriterStore struct {
	initial []apigen.ScheduledInstanceState
	mu      sync.Mutex
	current []apigen.ScheduledInstanceState
	updates chan apigen.ScheduledInstanceState
}

func (s *netStateWriterStore) FetchScheduledSnapshot(storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *netStateWriterStore) setCurrent(items []apigen.ScheduledInstanceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = items
}

func (s *netStateWriterStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	return s.initial, s.updates, func() {}
}

func netStateSeq(b []byte) (int64, error) {
	state, err := apigen.DecodeNetState(b)
	if err != nil {
		return 0, err
	}
	return state.Seq, nil
}

func TestInitialArtifactSequenceContinuesPersistedSequenceAhead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netstate.pb")
	future := time.Now().Add(time.Hour).UnixMilli()
	if err := WriteNetState(path, &apigen.NetState{Seq: future}); err != nil {
		t.Fatalf("writing netstate: %v", err)
	}
	if got := initialArtifactSequence(path, netStateSeq); got != future {
		t.Fatalf("initial sequence = %d, want persisted %d", got, future)
	}
}

func TestInitialArtifactSequenceFloorsAtWallClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netstate.pb")
	before := time.Now().UnixMilli()
	if got := initialArtifactSequence(path, netStateSeq); got < before {
		t.Fatalf("initial sequence without snapshot = %d, want >= %d", got, before)
	}
	// A persisted sequence behind the clock is floored too: netproxy's own seq
	// gates survive an agent state-dir wipe, so resuming a stale count would
	// have it silently dropping every update.
	if err := WriteNetState(path, &apigen.NetState{Seq: 41}); err != nil {
		t.Fatalf("writing netstate: %v", err)
	}
	if got := initialArtifactSequence(path, netStateSeq); got < before {
		t.Fatalf("initial sequence with stale snapshot = %d, want >= %d", got, before)
	}
}

func TestRunNetStateWriterProcessesUpdateQueuedWithInitialSnapshot(t *testing.T) {
	previousNetwork := network.Default
	network.SetDefault(network.New(network.GeneratePrefix(), 99))
	t.Cleanup(func() { network.SetDefault(previousNetwork) })

	route := apigen.ScheduledInstanceState{Config: apigen.DeploymentConfig{
		Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: "queued.example.com",
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					HostPort:      8443,
					ContainerPort: 443,
				},
			}},
		}},
	}}
	updates := make(chan apigen.ScheduledInstanceState, 1)
	updates <- route
	store := &netStateWriterStore{current: []apigen.ScheduledInstanceState{route}, updates: updates}
	path := filepath.Join(t.TempDir(), "netstate.pb")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunNetStateWriter(ctx, store, nil, "node-a", path, nil, nil, nil)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			state, decodeErr := apigen.DecodeNetState(data)
			if decodeErr == nil && state.Seq >= 2 && len(state.Ingress) == 1 && state.Ingress[0].Hostname == "queued.example.com" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("queued update was not rendered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("netstate writer did not stop")
	}
}

func waitForNetState(t *testing.T, path string, ready func(*apigen.NetState) bool) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if state, err := apigen.DecodeNetState(b); err == nil && ready(state) {
				return state.Seq
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("netstate did not reach the expected content")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunNetStateWriterSkipsRewriteWhenContentUnchanged(t *testing.T) {
	previousNetwork := network.Default
	network.SetDefault(network.New(network.GeneratePrefix(), 99))
	t.Cleanup(func() { network.SetDefault(previousNetwork) })

	route := func(hostname string) apigen.ScheduledInstanceState {
		return apigen.ScheduledInstanceState{Config: apigen.DeploymentConfig{
			Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: hostname,
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
						HostPort:      8443,
						ContainerPort: 443,
					},
				}},
			}},
		}}
	}
	first := route("first.example.com")
	updates := make(chan apigen.ScheduledInstanceState, 2)
	store := &netStateWriterStore{initial: []apigen.ScheduledInstanceState{first}, updates: updates}
	store.setCurrent([]apigen.ScheduledInstanceState{first})
	path := filepath.Join(t.TempDir(), "netstate.pb")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunNetStateWriter(ctx, store, nil, "node-a", path, nil, nil, nil)
		close(done)
	}()

	initialSeq := waitForNetState(t, path, func(s *apigen.NetState) bool {
		return len(s.Ingress) == 1 && s.Ingress[0].Hostname == "first.example.com"
	})

	// Whichever order the writer interleaves these with setCurrent, exactly one
	// of the two updates changes the rendered content, so the sequence must
	// advance by exactly one.
	updates <- first
	store.setCurrent([]apigen.ScheduledInstanceState{first, route("second.example.com")})
	updates <- first

	finalSeq := waitForNetState(t, path, func(s *apigen.NetState) bool { return len(s.Ingress) == 2 })
	if finalSeq != initialSeq+1 {
		t.Fatalf("seq went %d -> %d; unchanged renders must not rewrite the file", initialSeq, finalSeq)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("netstate writer did not stop")
	}
}

func TestRenderNetStateRendersTlsPassthroughIngress(t *testing.T) {
	prefix := network.GeneratePrefix()
	network.SetDefault(network.New(prefix, 99))
	state := RenderNetState(7, "node-a", []apigen.ScheduledInstanceState{{
		Config: apigen.DeploymentConfig{
			ID:      42,
			SpaceID: 1, Name: "database",
			Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: "DB.Example.COM.",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
						ContainerPort: 5432,
					},
				}},
			}},
		},
		Instance: apigen.ScheduledInstance{
			ID: 5, DeploymentID: 42, NodeID: 1,
			State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Status: apigen.ScheduledInstanceStatus{Runner: apigen.RunnerStatus{
			Status: apigen.RunningStatus_RUNNING,
		}},
	}}, nil)

	backendAddr, err := prefix.InboundAddr(1, 42, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.NodeIdentifier != "node-a" {
		t.Fatalf("node identifier = %q, want node-a", state.NodeIdentifier)
	}
	if got := len(state.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
	route := state.Ingress[0]
	if route.Kind != apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH || route.Hostname != "db.example.com" {
		t.Fatalf("route = %+v, want normalized TLS passthrough route", route)
	}
	if route.TlsPassthrough == nil || route.TlsPassthrough.HostPort != 443 {
		t.Fatalf("TLS passthrough config = %+v, want default host port 443", route.TlsPassthrough)
	}
	if got := route.TlsPassthrough.Backends; len(got) != 1 || got[0].Address != backendAddr.String() || got[0].Port != 5432 {
		t.Fatalf("backends = %+v, want %s:5432", got, backendAddr)
	}
}

// TestRenderNetStateDerivesEndpointsFromPlacement covers the removal of the
// reported endpoint set. A placement's address is a pure function of its space,
// deployment, and ordinal, so the only thing status still decides is whether the
// endpoint is published at all — and only the serving placement that is actually
// running may be.
func TestRenderNetStateDerivesEndpointsFromPlacement(t *testing.T) {
	prefix := network.GeneratePrefix()
	previousNetwork := network.Default
	network.SetDefault(network.New(prefix, 99))
	t.Cleanup(func() { network.SetDefault(previousNetwork) })

	item := func(state apigen.ScheduledInstanceTarget, running apigen.RunningStatus) apigen.ScheduledInstanceState {
		return apigen.ScheduledInstanceState{
			Instance: apigen.ScheduledInstance{ID: 5, DeploymentID: 42, NodeID: 1, State: state},
			Config: apigen.DeploymentConfig{
				ID:      42,
				SpaceID: 1, Name: "database",
				Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
					Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				}},
			},
			Status: apigen.ScheduledInstanceStatus{Runner: apigen.RunnerStatus{Status: running}},
		}
	}
	want, err := prefix.InboundAddr(1, 42, 0)
	if err != nil {
		t.Fatal(err)
	}

	serving := RenderNetState(1, "node-a", []apigen.ScheduledInstanceState{
		item(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, apigen.RunningStatus_RUNNING),
	}, nil)
	if len(serving.DnsServices) != 1 || len(serving.DnsServices[0].Endpoints) != 1 {
		t.Fatalf("dns services = %+v, want one endpoint", serving.DnsServices)
	}
	if got := serving.DnsServices[0].Endpoints[0].Address; got != want.String() {
		t.Fatalf("endpoint address = %s, want derived %s", got, want)
	}

	// A placement that is not serving-and-running keeps its DNS name — the DNS
	// server answers authoritatively for a service that exists but is down
	// instead of leaking the lookup upstream — but publishes no endpoint, since
	// its address does not route to this node yet.
	for name, down := range map[string]apigen.ScheduledInstanceState{
		"standby":  item(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY, apigen.RunningStatus_RUNNING),
		"draining": item(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING, apigen.RunningStatus_RUNNING),
		"crashed":  item(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, apigen.RunningStatus_CRASHED),
		"starting": item(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, apigen.RunningStatus_STARTING),
	} {
		got := RenderNetState(1, "node-a", []apigen.ScheduledInstanceState{down}, nil)
		if len(got.DnsServices) != 1 || len(got.DnsServices[0].Endpoints) != 0 {
			t.Errorf("%s: dns services = %+v, want the service with no endpoints", name, got.DnsServices)
		}
	}
}

func TestRenderNetStateKeepsIngressWithoutReadyBackend(t *testing.T) {
	state := RenderNetState(1, "node-a", []apigen.ScheduledInstanceState{{
		Config: apigen.DeploymentConfig{
			Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigen.Ingress{{
					Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
					Hostname: "db.example.com",
					TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
						HostPort:      8443,
						ContainerPort: 5432,
					},
				}},
			}},
		},
	}}, nil)

	if got := len(state.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
	if got := state.Ingress[0].TlsPassthrough.Backends; len(got) != 0 {
		t.Fatalf("backends = %+v, want none", got)
	}
}

func TestRenderNetStateOmitsIngressOnDNSPort(t *testing.T) {
	state := RenderNetState(1, "node-a", []apigen.ScheduledInstanceState{{
		Config: apigen.DeploymentConfig{Spec: apigen.DeploymentSpec{Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname: "dns.example.com",
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{
					HostPort:      netproxyDNSPort,
					ContainerPort: 443,
				},
			}},
		}}},
	}}, nil)

	if len(state.Ingress) != 0 {
		t.Fatalf("ingress = %+v, want DNS port route omitted", state.Ingress)
	}
}

func TestHostResolversFallsBackFromLoopbackStub(t *testing.T) {
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "stub.conf")
	upstreamPath := filepath.Join(dir, "upstream.conf")
	if err := os.WriteFile(stubPath, []byte("nameserver 127.0.0.53\nnameserver ::1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upstreamPath, []byte("nameserver 192.0.2.53\nnameserver 2001:db8::53\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := []string{"192.0.2.53", "2001:db8::53"}
	if got := hostResolversFromFiles(stubPath, upstreamPath); !slices.Equal(got, want) {
		t.Fatalf("resolvers = %v, want %v", got, want)
	}
}

func TestHostResolversPrefersUsablePrimaryConfig(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.conf")
	fallbackPath := filepath.Join(dir, "fallback.conf")
	if err := os.WriteFile(primaryPath, []byte("nameserver 198.51.100.53\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallbackPath, []byte("nameserver 192.0.2.53\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := []string{"198.51.100.53"}
	if got := hostResolversFromFiles(primaryPath, fallbackPath); !slices.Equal(got, want) {
		t.Fatalf("resolvers = %v, want %v", got, want)
	}
}
