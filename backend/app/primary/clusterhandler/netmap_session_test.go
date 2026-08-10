package clusterhandler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb"
)

type sessionNetMapProvider struct {
	current *apigen.ClusterNetMap

	mu        sync.Mutex
	applied   map[int32]int64
	forgotten []int32
}

func (p *sessionNetMapProvider) SnapshotAndSubscribe(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	next := *p.current
	next.TargetNodeID = nodeID
	return &next, make(chan *apigen.ClusterNetMap), func() {}
}

func (p *sessionNetMapProvider) RecordApplied(nodeID int32, appliedSequence int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.applied == nil {
		p.applied = make(map[int32]int64)
	}
	p.applied[nodeID] = appliedSequence
}

func (p *sessionNetMapProvider) ForgetNode(nodeID int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgotten = append(p.forgotten, nodeID)
}

func (p *sessionNetMapProvider) appliedFor(nodeID int32) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq, ok := p.applied[nodeID]
	return seq, ok
}

// TestSessionRecordsOnlyCleanNetMapApplies covers the barrier's input: a worker
// that reports a reconciliation error still holds whatever its kernel had
// before, so counting it as caught up would retire a placement it can still be
// routing to.
func TestSessionRecordsOnlyCleanNetMapApplies(t *testing.T) {
	provider := &sessionNetMapProvider{current: &apigen.ClusterNetMap{Generation: "g", Sequence: 1}}
	session := &Session{NodeID: 7, networkMaps: provider}

	session.handleIncoming(&apigen.MsgToMaster{NetMapStatus: &apigen.NetMapStatus{AppliedSequence: 4}})
	if seq, ok := provider.appliedFor(7); !ok || seq != 4 {
		t.Fatalf("clean apply recorded %d (present=%v), want 4", seq, ok)
	}

	session.handleIncoming(&apigen.MsgToMaster{NetMapStatus: &apigen.NetMapStatus{
		AppliedSequence: 9, ReconciliationError: "installing remote route: no such device",
	}})
	if seq, _ := provider.appliedFor(7); seq != 4 {
		t.Fatalf("failed apply recorded %d, want the previous clean value 4", seq)
	}
}

func TestSessionReconnectSendsLatestNetworkMap(t *testing.T) {
	store := primarydb.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	provider := &sessionNetMapProvider{current: &apigen.ClusterNetMap{Generation: "generation-a", Sequence: 1}}

	first := initialSessionNetMap(t, store, provider)
	if first.Sequence != 1 || first.TargetNodeID != 7 {
		t.Fatalf("first session map = %+v", first)
	}
	provider.current = &apigen.ClusterNetMap{Generation: "generation-a", Sequence: 3}
	second := initialSessionNetMap(t, store, provider)
	if second.Sequence != 3 || second.TargetNodeID != 7 {
		t.Fatalf("reconnected session map = %+v", second)
	}
}

func initialSessionNetMap(t *testing.T, store *primarydb.Storage, provider networkMapProvider) *apigen.ClusterNetMap {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := newSession(ctx, cancel, 7, "worker", nil, store, provider)
	reqs := func(yield func(*apigen.MsgToMaster, error) bool) {
		<-ctx.Done()
	}
	var got *apigen.ClusterNetMap
	session.run(reqs, func(msg *apigen.MsgToWorker, err error) bool {
		if err != nil {
			t.Fatal(err)
		}
		if msg.ClusterNetMap != nil {
			got = msg.ClusterNetMap
		}
		return msg.ScheduledInstancesSnapshot == nil
	})
	if got == nil {
		t.Fatal("session did not send a network map")
	}
	return got
}
