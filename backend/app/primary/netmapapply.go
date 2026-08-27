package primary

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/netmappublisher"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

type netMapApplier struct {
	nodeID               int32
	prefix               network.Prefix
	snapshotAndSubscribe func(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func())
	recordApplied        func(nodeID int32, appliedSeq int64)
	reconcile            func(network.Topology) error
	setPolicyRules       func([]network.PolicyRule) error
	retryDelay           time.Duration
}

func newNetMapApplier(nodeID int32, prefix network.Prefix, maps *netmappublisher.Publisher) *netMapApplier {
	return &netMapApplier{
		nodeID:               nodeID,
		prefix:               prefix,
		snapshotAndSubscribe: maps.SnapshotAndSubscribe,
		recordApplied:        maps.RecordApplied,
		reconcile:            network.Default.ReconcileTopology,
		setPolicyRules:       network.Default.SetPolicyRules,
		retryDelay:           15 * time.Second,
	}
}

func (a *netMapApplier) run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "NetmapApplier")
	pending, updates, unsubscribe := a.snapshotAndSubscribe(a.nodeID)
	defer unsubscribe()
	var retry <-chan time.Time
	for {
		if pending != nil {
			if err := a.apply(pending); err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("applying cluster network map on primary node failed seq=%d", pending.DerivedFromSeq), "err", err)
				retry = time.After(a.retryDelay)
			} else {
				slog.InfoContext(ctx, fmt.Sprintf("applied cluster network map on primary node seq=%d", pending.DerivedFromSeq))
				a.recordApplied(a.nodeID, pending.DerivedFromSeq)
				pending = nil
				retry = nil
			}
		}
		select {
		case <-ctx.Done():
			return
		case next, ok := <-updates:
			if !ok {
				return
			}
			pending = next
		case <-retry:
			retry = nil
		}
	}
}

func (a *netMapApplier) apply(clusterMap *apigen.ClusterNetMap) error {
	topology, err := network.TopologyFromClusterNetMap(clusterMap, a.nodeID, a.prefix)
	if err != nil {
		return err
	}
	if err := a.reconcile(topology); err != nil {
		return err
	}
	return a.setPolicyRules(network.PolicyRulesFromNetMap(clusterMap.PolicyRules))
}
