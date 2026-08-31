package secondary

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

func TestAcceptClusterNetMapSessionSemantics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := state.Open(dbPath)
	defer store.Close()
	prefix := network.GeneratePrefix()

	first := testClusterNetMap(t, prefix, 5)
	status, err := acceptClusterNetMap(context.Background(), store, first, 1, prefix, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.PersistedSeq != 5 {
		t.Fatalf("status = %+v", status)
	}

	duplicate := testClusterNetMap(t, prefix, 5)
	duplicate.Nodes[0], duplicate.Nodes[1] = duplicate.Nodes[1], duplicate.Nodes[0]
	if _, err := acceptClusterNetMap(context.Background(), store, duplicate, 1, prefix, false, nil); err != nil {
		t.Fatalf("idempotent map rejected: %v", err)
	}
	stale := testClusterNetMap(t, prefix, 4)
	if _, err := acceptClusterNetMap(context.Background(), store, stale, 1, prefix, false, nil); !errors.Is(err, ErrStaleClusterNetMap) {
		t.Fatalf("stale error = %v", err)
	}
	higher := testClusterNetMap(t, prefix, 8)
	if _, err := acceptClusterNetMap(context.Background(), store, higher, 1, prefix, false, nil); err != nil {
		t.Fatal(err)
	}

	// A new session's snapshot wins unconditionally: a primary restored from a
	// backup republishes with a rolled-back counter, and its snapshot at connect
	// is authoritative even so.
	rolledBack := testClusterNetMap(t, prefix, 2)
	rolledBack.Nodes[1].UnderlayAddress = "192.0.2.9"
	if _, err := acceptClusterNetMap(context.Background(), store, rolledBack, 1, prefix, true, nil); err != nil {
		t.Fatalf("session snapshot with a lower stamp rejected: %v", err)
	}
	cached, _, ok, err := cachedClusterNetMap(context.Background(), store, 1, prefix)
	if err != nil || !ok {
		t.Fatalf("cached map: ok=%v err=%v", ok, err)
	}
	if cached.DerivedFromSeq != 2 || cached.Nodes[1].UnderlayAddress != "192.0.2.9" {
		t.Fatalf("cached map = %+v", cached)
	}
}

func TestRejectedInitialClusterNetMapReportsError(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()
	prefix := network.GeneratePrefix()
	oldDefault := network.Default
	network.SetDefault(network.New(prefix, 0))
	t.Cleanup(func() { network.SetDefault(oldDefault) })
	out := &outbox{ch: make(chan *apigen.MsgToMaster, 1), ctx: context.Background()}
	invalid := testClusterNetMap(t, prefix, 1)
	invalid.TargetNodeID = 2
	sess := &primarySessionState{netMapSnapshotPending: true}
	dispatchFromPrimary(context.Background(), out, store, newLogStreamTracker(), sess, &apigen.MsgToWorker{ClusterNetMap: invalid}, 1, nil, nil, nil)
	status := (<-out.ch).NetMapStatus
	if status == nil || status.ReconciliationError == "" || status.PersistedSeq != 0 {
		t.Fatalf("rejection status = %+v", status)
	}
	if !sess.netMapSnapshotPending {
		t.Fatal("a rejected map consumed the session's unconditional snapshot acceptance")
	}
}

func TestValidateClusterNetMapRejectsInvalidTopology(t *testing.T) {
	prefix := network.GeneratePrefix()
	tests := map[string]func(*apigen.ClusterNetMap){
		"wrong target":   func(m *apigen.ClusterNetMap) { m.TargetNodeID = 2 },
		"negative stamp": func(m *apigen.ClusterNetMap) { m.DerivedFromSeq = -1 },
		"missing target": func(m *apigen.ClusterNetMap) { m.Nodes = m.Nodes[1:] },
		"duplicate node": func(m *apigen.ClusterNetMap) { m.Nodes = append(m.Nodes, m.Nodes[0]) },
		"mixed family":   func(m *apigen.ClusterNetMap) { m.Nodes[1].UnderlayAddress = "2001:db8::2" },
		"unknown host":   func(m *apigen.ClusterNetMap) { m.Routes[0].HostingNodeID = 3 },
		"duplicate route": func(m *apigen.ClusterNetMap) {
			m.Routes = append(m.Routes, m.Routes[0])
		},
		// Only whole instances and whole placements route to a node. A host
		// route would pin one run, and a deployment-wide prefix would send every
		// ordinal to a single node.
		"host route": func(m *apigen.ClusterNetMap) {
			addr, err := prefix.InboundAddr(1, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			m.Routes[0].LogicalPrefix = netip.PrefixFrom(addr, 128).String()
		},
		"deployment-wide prefix": func(m *apigen.ClusterNetMap) {
			deployment, err := prefix.DeploymentCIDR(1, 10)
			if err != nil {
				t.Fatal(err)
			}
			m.Routes[0].LogicalPrefix = deployment.String()
		},
		"bare address": func(m *apigen.ClusterNetMap) {
			addr, err := prefix.InboundAddr(1, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			m.Routes[0].LogicalPrefix = addr.String()
		},
		"placement prefix with run bits set": func(m *apigen.ClusterNetMap) {
			addr, err := prefix.OutboundAddr(1, 10, 0, 1, 4)
			if err != nil {
				t.Fatal(err)
			}
			m.Routes[0].LogicalPrefix = netip.PrefixFrom(addr, network.PlacementPrefixBits).String()
		},
		"malformed wg key": func(m *apigen.ClusterNetMap) {
			m.Nodes[0].WgPublicKey = "not-base64"
		},
		"missing wg key": func(m *apigen.ClusterNetMap) {
			m.Nodes[0].WgPublicKey = ""
		},
		"missing wg port": func(m *apigen.ClusterNetMap) {
			m.Nodes[0].WgListenPort = 0
		},
		"wg port out of range": func(m *apigen.ClusterNetMap) {
			m.Nodes[0].WgListenPort = 70000
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := testClusterNetMap(t, prefix, 1)
			mutate(candidate)
			if _, _, err := validateClusterNetMap(candidate, 1, prefix); err == nil {
				t.Fatal("invalid map was accepted")
			}
		})
	}
}

// The cached map outlives the binary that wrote it, so a release that changes
// what a map may contain inherits whatever the previous release persisted. A
// cached blob this build cannot validate must be dropped rather than surfaced,
// because the two callers that see the error cannot survive it: startup
// panics, and acceptClusterNetMap would refuse the replacement map on the
// strength of the unreadable one it is replacing.
func TestUnreadableCachedClusterNetMapIsDiscarded(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()
	prefix := network.GeneratePrefix()

	// A bare address where a CIDR route prefix belongs fails validation.
	unreadable := testClusterNetMap(t, prefix, 7)
	addr, err := prefix.InboundAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	unreadable.Routes[0].LogicalPrefix = addr.String()
	store.MustSetLocalKV(storage.LocalKVWorkerClusterNetMap, unreadable.Encode())

	cached, _, ok, err := cachedClusterNetMap(context.Background(), store, 1, prefix)
	if err != nil {
		t.Fatalf("unreadable cached map surfaced an error: %v", err)
	}
	if ok || cached != nil {
		t.Fatalf("unreadable cached map was returned: ok=%v map=%+v", ok, cached)
	}
	if _, present := store.FetchLocalKV(storage.LocalKVWorkerClusterNetMap); present {
		t.Fatal("unreadable cached map was left in the store")
	}

	next := testClusterNetMap(t, prefix, 8)
	status, err := acceptClusterNetMap(context.Background(), store, next, 1, prefix, false, nil)
	if err != nil {
		t.Fatalf("republished map rejected after discarding the unreadable cache: %v", err)
	}
	if status.PersistedSeq != 8 {
		t.Fatalf("status = %+v", status)
	}
}

func testClusterNetMap(t *testing.T, prefix network.Prefix, seq int64) *apigen.ClusterNetMap {
	t.Helper()
	destination, err := prefix.InstanceCIDR(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &apigen.ClusterNetMap{
		DerivedFromSeq: seq,
		TargetNodeID:   1,
		UlaPrefix:      prefix.Bytes(),
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 1, UnderlayAddress: "192.0.2.1", WgPublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=", WgListenPort: 51833},
			{NodeID: 2, UnderlayAddress: "192.0.2.2", WgPublicKey: "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=", WgListenPort: 51833},
		},
		Routes: []*apigen.ClusterNetMapRoute{{LogicalPrefix: destination.String(), HostingNodeID: 1}},
		DnsServices: []*apigen.ClusterNetMapService{
			{Name: "opendeploy-net", SpaceID: 0, DeploymentID: 2, Ordinals: []*apigen.ClusterNetMapServiceOrdinal{{Ordinal: 0}}},
			{Name: "app", SpaceID: 1, DeploymentID: 10, Ordinals: []*apigen.ClusterNetMapServiceOrdinal{{Ordinal: 0}}},
		},
	}
}
