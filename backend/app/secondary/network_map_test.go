package secondary

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestAcceptClusterNetMapSequencing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := sqlite.NewSecondaryStorage(dbPath)
	defer store.Close()
	prefix := network.GeneratePrefix()
	first := testClusterNetMap(t, prefix, "generation-a", 1)

	status, err := acceptClusterNetMap(store, first, 1, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if status.AcceptedGeneration != first.Generation || status.PersistedSequence != 1 {
		t.Fatalf("status = %+v", status)
	}

	duplicate := testClusterNetMap(t, prefix, "generation-a", 1)
	duplicate.Nodes[0], duplicate.Nodes[1] = duplicate.Nodes[1], duplicate.Nodes[0]
	if _, err := acceptClusterNetMap(store, duplicate, 1, prefix); err != nil {
		t.Fatalf("idempotent map rejected: %v", err)
	}
	stale := testClusterNetMap(t, prefix, "generation-a", -1)
	if _, err := acceptClusterNetMap(store, stale, 1, prefix); err == nil {
		t.Fatal("non-positive sequence was accepted")
	}
	conflict := testClusterNetMap(t, prefix, "generation-a", 1)
	conflict.Routes[0].HostingNodeID = 2
	if _, err := acceptClusterNetMap(store, conflict, 1, prefix); !errors.Is(err, ErrConflictingClusterNetMap) {
		t.Fatalf("conflict error = %v", err)
	}

	higher := testClusterNetMap(t, prefix, "generation-a", 2)
	if _, err := acceptClusterNetMap(store, higher, 1, prefix); err != nil {
		t.Fatal(err)
	}
	stale = testClusterNetMap(t, prefix, "generation-a", 1)
	if _, err := acceptClusterNetMap(store, stale, 1, prefix); !errors.Is(err, ErrStaleClusterNetMap) {
		t.Fatalf("stale error = %v", err)
	}
	reset := testClusterNetMap(t, prefix, "generation-b", 1)
	if _, err := acceptClusterNetMap(store, reset, 1, prefix); err != nil {
		t.Fatalf("new generation rejected: %v", err)
	}
	cached, _, ok, err := cachedClusterNetMap(store, 1, prefix)
	if err != nil || !ok {
		t.Fatalf("cached map: ok=%v err=%v", ok, err)
	}
	if cached.Generation != "generation-b" || cached.Sequence != 1 {
		t.Fatalf("cached map = %+v", cached)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = sqlite.NewSecondaryStorage(dbPath)
	defer store.Close()
	retired := testClusterNetMap(t, prefix, "generation-a", 3)
	if _, err := acceptClusterNetMap(store, retired, 1, prefix); !errors.Is(err, ErrStaleClusterNetMap) {
		t.Fatalf("retired generation error = %v", err)
	}
}

func TestRejectedInitialClusterNetMapReportsError(t *testing.T) {
	store := sqlite.NewSecondaryStorage(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()
	prefix := network.GeneratePrefix()
	oldDefault := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(oldDefault) })
	out := &outbox{ch: make(chan *apigen.MsgToMaster, 1), ctx: context.Background()}
	invalid := testClusterNetMap(t, prefix, "generation-a", 1)
	invalid.TargetNodeID = 2
	dispatchFromPrimary(context.Background(), out, store, newLogStreamTracker(), &apigen.MsgToWorker{ClusterNetMap: invalid}, 1)
	status := (<-out.ch).NetMapStatus
	if status == nil || status.ReconciliationError == "" || status.PersistedSequence != 0 {
		t.Fatalf("rejection status = %+v", status)
	}
}

func TestValidateClusterNetMapRejectsInvalidTopology(t *testing.T) {
	prefix := network.GeneratePrefix()
	tests := map[string]func(*apigen.ClusterNetMap){
		"wrong target": func(m *apigen.ClusterNetMap) { m.TargetNodeID = 2 },
		"padded generation": func(m *apigen.ClusterNetMap) {
			m.Generation = " generation-a "
		},
		"missing target": func(m *apigen.ClusterNetMap) { m.Nodes = m.Nodes[1:] },
		"duplicate node": func(m *apigen.ClusterNetMap) { m.Nodes = append(m.Nodes, m.Nodes[0]) },
		"mixed family":   func(m *apigen.ClusterNetMap) { m.Nodes[1].UnderlayAddress = "2001:db8::2" },
		"unknown host":   func(m *apigen.ClusterNetMap) { m.Routes[0].HostingNodeID = 3 },
		"duplicate route": func(m *apigen.ClusterNetMap) {
			m.Routes = append(m.Routes, m.Routes[0])
		},
		"service route": func(m *apigen.ClusterNetMap) {
			addr, err := prefix.ServiceAddr(1, 10)
			if err != nil {
				t.Fatal(err)
			}
			m.Routes[0].LogicalAddress = addr.String()
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := testClusterNetMap(t, prefix, "generation-a", 1)
			mutate(candidate)
			if _, _, err := validateClusterNetMap(candidate, 1, prefix); err == nil {
				t.Fatal("invalid map was accepted")
			}
		})
	}
}

func testClusterNetMap(t *testing.T, prefix network.Prefix, generation string, sequence int64) *apigen.ClusterNetMap {
	t.Helper()
	addr, err := prefix.InstanceAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &apigen.ClusterNetMap{
		Generation:   generation,
		Sequence:     sequence,
		TargetNodeID: 1,
		UlaPrefix:    prefix.Bytes(),
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1"},
			{NodeID: 2, UnderlayAddress: "192.0.2.2"},
		},
		Routes: []*apigen.ClusterNetMapRoute{{LogicalAddress: addr.String(), HostingNodeID: 1}},
	}
}
