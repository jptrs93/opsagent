package netproxy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
)

type netStateWriterStore struct {
	initial []apigen.DeploymentWithStatus
	current []apigen.DeploymentWithStatus
	updates chan apigen.DeploymentWithStatus
}

func (s *netStateWriterStore) FetchDeploymentSnapshot(storage.DeploymentPredicate) []apigen.DeploymentWithStatus {
	return s.current
}

func (s *netStateWriterStore) MustFetchSnapshotAndSubscribe(storage.DeploymentPredicate) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func()) {
	return s.initial, s.updates, func() {}
}

func TestInitialNetStateSequenceContinuesPersistedSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netstate.pb")
	if err := WriteNetState(path, &apigen.NetState{Seq: 41}); err != nil {
		t.Fatalf("writing netstate: %v", err)
	}
	if got := initialNetStateSequence(path); got != 41 {
		t.Fatalf("initial sequence = %d, want 41", got)
	}
}

func TestInitialNetStateSequenceStartsAtZeroWithoutSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netstate.pb")
	if got := initialNetStateSequence(path); got != 0 {
		t.Fatalf("initial sequence = %d, want 0", got)
	}
}

func TestRunNetStateWriterProcessesUpdateQueuedWithInitialSnapshot(t *testing.T) {
	previousNetwork := network.Default
	network.SetDefault(network.New(network.GeneratePrefix(), 99))
	t.Cleanup(func() { network.SetDefault(previousNetwork) })

	route := apigen.DeploymentWithStatus{Config: apigen.DeploymentConfig2{
		Spec: apigen.DeploymentSpec2{Networking: apigen.NetworkingConfig{
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
	updates := make(chan apigen.DeploymentWithStatus, 1)
	updates <- route
	store := &netStateWriterStore{current: []apigen.DeploymentWithStatus{route}, updates: updates}
	path := filepath.Join(t.TempDir(), "netstate.pb")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunNetStateWriter(ctx, store, nil, "node-a", path)
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

func TestRenderNetStateRendersTlsPassthroughIngress(t *testing.T) {
	prefix := network.GeneratePrefix()
	network.SetDefault(network.New(prefix, 99))
	state := RenderNetState(7, "node-a", []apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig2{
			ID:       42,
			Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "database"},
			Spec: apigen.DeploymentSpec2{Networking: apigen.NetworkingConfig{
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
		Status: apigen.DeploymentStatus{Runner: apigen.RunnerStatus{Endpoints: []*apigen.Endpoint{{
			Address: "fd00::42",
			State:   apigen.EndpointState_ENDPOINT_READY,
		}}}},
	}})

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
	if got := route.TlsPassthrough.Backends; len(got) != 1 || got[0].Address != "fd00::42" || got[0].Port != 5432 {
		t.Fatalf("backends = %+v, want fd00::42:5432", got)
	}
}

func TestRenderNetStateKeepsIngressWithoutReadyBackend(t *testing.T) {
	state := RenderNetState(1, "node-a", []apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig2{
			Spec: apigen.DeploymentSpec2{Networking: apigen.NetworkingConfig{
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
	}})

	if got := len(state.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
	if got := state.Ingress[0].TlsPassthrough.Backends; len(got) != 0 {
		t.Fatalf("backends = %+v, want none", got)
	}
}

func TestRenderNetStateOmitsIngressOnDNSPort(t *testing.T) {
	state := RenderNetState(1, "node-a", []apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig2{Spec: apigen.DeploymentSpec2{Networking: apigen.NetworkingConfig{
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
	}})

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
