package clusterhandler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type sessionNetMapProvider struct {
	current *apigen.ClusterNetMap
}

func (p *sessionNetMapProvider) SnapshotAndSubscribe(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	next := *p.current
	next.TargetNodeID = nodeID
	return &next, make(chan *apigen.ClusterNetMap), func() {}
}

func TestSessionReconnectSendsLatestNetworkMap(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
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

func initialSessionNetMap(t *testing.T, store *sqlite.PrimaryStorage, provider networkMapProvider) *apigen.ClusterNetMap {
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
		return msg.DeploymentsSnapshot == nil
	})
	if got == nil {
		t.Fatal("session did not send a network map")
	}
	return got
}
