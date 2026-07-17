package netproxy

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

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

func TestRenderNetStateRendersTlsPassthroughIngress(t *testing.T) {
	prefix := network.GeneratePrefix()
	network.SetDefault(network.New(prefix, 99))
	state := RenderNetState(7, "node-a", []apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig{
			ID:       42,
			ConfigID: apigen.DeploymentIdentifier{SpaceID: 1, Name: "database"},
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
		Status: apigen.DeploymentStatus{Runner: apigen.RunnerStatus{Endpoints: []*apigen.Endpoint{{
			Address: "fd00::42",
			State:   apigen.EndpointState_ENDPOINT_READY,
		}}}},
	}})

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
	}})

	if got := len(state.Ingress); got != 1 {
		t.Fatalf("ingress count = %d, want 1", got)
	}
	if got := state.Ingress[0].TlsPassthrough.Backends; len(got) != 0 {
		t.Fatalf("backends = %+v, want none", got)
	}
}
