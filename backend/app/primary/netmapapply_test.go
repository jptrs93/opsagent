package primary

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func testNetMapApplier(t *testing.T, reconcile func(network.Topology) error) (*netMapApplier, chan *apigen.ClusterNetMap, chan int64) {
	t.Helper()
	updates := make(chan *apigen.ClusterNetMap, 4)
	applied := make(chan int64, 16)
	prefix := network.GeneratePrefix()
	return &netMapApplier{
		nodeID: 1,
		prefix: prefix,
		snapshotAndSubscribe: func(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
			if nodeID != 1 {
				t.Errorf("subscribed as node %d, want 1", nodeID)
			}
			return targetedNetMap(prefix, 1, 5), updates, func() {}
		},
		recordApplied: func(nodeID int32, appliedSeq int64) {
			if nodeID != 1 {
				t.Errorf("recorded applied for node %d, want 1", nodeID)
			}
			applied <- appliedSeq
		},
		reconcile:  reconcile,
		retryDelay: 5 * time.Millisecond,
	}, updates, applied
}

func targetedNetMap(prefix network.Prefix, nodeID int32, seq int64) *apigen.ClusterNetMap {
	return &apigen.ClusterNetMap{
		TargetNodeID:   nodeID,
		UlaPrefix:      prefix.Bytes(),
		Nodes:          []*apigen.ClusterNetMapNode{{NodeID: nodeID, UnderlayAddress: "192.0.2.1"}},
		DerivedFromSeq: seq,
	}
}

func waitApplied(t *testing.T, applied chan int64, want int64) {
	t.Helper()
	select {
	case got := <-applied:
		if got != want {
			t.Fatalf("applied seq %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for applied seq %d", want)
	}
}

func TestNetMapApplierAppliesSnapshotAndUpdates(t *testing.T) {
	var topologies []network.Topology
	applier, updates, applied := testNetMapApplier(t, func(topology network.Topology) error {
		topologies = append(topologies, topology)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		applier.run(ctx)
	}()

	waitApplied(t, applied, 5)
	updates <- targetedNetMap(applier.prefix, 1, 9)
	waitApplied(t, applied, 9)
	cancel()
	<-done

	if len(topologies) != 2 {
		t.Fatalf("reconciled %d topologies, want 2", len(topologies))
	}
	for _, topology := range topologies {
		if topology.LocalNodeID != 1 || topology.Prefix != applier.prefix {
			t.Fatalf("topology = %+v", topology)
		}
	}
}

func TestNetMapApplierRetriesFailedReconcile(t *testing.T) {
	fails := 2
	applier, _, applied := testNetMapApplier(t, func(network.Topology) error {
		if fails > 0 {
			fails--
			return errors.New("kernel says no")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		applier.run(ctx)
	}()

	waitApplied(t, applied, 5)
	cancel()
	<-done

	if fails != 0 {
		t.Fatalf("reconcile failures remaining = %d, want 0", fails)
	}
	select {
	case seq := <-applied:
		t.Fatalf("unexpected extra applied seq %d", seq)
	default:
	}
}

func TestNetMapApplierReplacesPendingMapWhileRetrying(t *testing.T) {
	var allow atomic.Bool
	failures := make(chan struct{}, 16)
	applier, updates, applied := testNetMapApplier(t, func(network.Topology) error {
		if !allow.Load() {
			failures <- struct{}{}
			return errors.New("kernel says no")
		}
		return nil
	})
	applier.retryDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		applier.run(ctx)
	}()

	select {
	case <-failures:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first reconcile failure")
	}
	allow.Store(true)
	updates <- targetedNetMap(applier.prefix, 1, 9)
	waitApplied(t, applied, 9)
	cancel()
	<-done
}
