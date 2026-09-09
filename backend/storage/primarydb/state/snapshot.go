package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"sort"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
)

func (s *Service) BuildSnapshot(ctx context.Context) *apigen.Snapshot {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return BuildSnapshot(ctx, s.q)
}

func BuildSnapshot(ctx context.Context, q *pq.Queries) *apigen.Snapshot {
	out := &apigen.Snapshot{Seq: erru.Must(q.GetGlobalSeq(ctx))}
	latest := erru.Must(q.ListLatestDeploymentEvents(ctx))
	deployments := map[int32]map[int32]*apigen.DeploymentEvent{}
	insertDeployment := func(event *apigen.DeploymentEvent) {
		if deployments[event.DeploymentID] == nil {
			deployments[event.DeploymentID] = map[int32]*apigen.DeploymentEvent{}
		}
		deployments[event.DeploymentID][event.Version] = event
	}
	for _, row := range latest {
		if !row.Deleted() {
			insertDeployment(row)
		}
	}
	// Deployment retention follows the reducer: latest desired plus pins,
	// with a deleted parent's tombstone held only while a live instance pins it.
	deletedDeployments := map[int64]bool{}
	for _, row := range latest {
		deletedDeployments[int64(row.DeploymentID)] = row.Deleted()
	}
	liveOrdinals := map[instanceOrdinalKey]bool{}
	instances := erru.Must(q.ListNonFinalScheduledInstances(ctx))
	for _, event := range instances {
		liveOrdinals[ordinalKeyOf(&event.Value)] = true
	}
	for _, event := range erru.Must(q.ListLatestScheduledInstancePerOrdinal(ctx)) {
		if !deletedDeployments[int64(event.Value.DeploymentID)] && event.Value.State.IsFinal() && !liveOrdinals[ordinalKeyOf(&event.Value)] {
			instances = append(instances, event)
		}
	}
	included := map[int32]*apigen.DeploymentEvent{}
	for _, event := range instances {
		out.ScheduledInstanceEvents = append(out.ScheduledInstanceEvents, event)
		pinned := pinnedConfig(ctx, q, event.Value.DeploymentID, event.Value.DeploymentVersion)
		insertDeployment(pinned)
		included[event.ScheduledInstanceID] = pinned
	}
	for _, row := range latest {
		if row.Deleted() && deployments[int32(row.DeploymentID)] != nil {
			insertDeployment(row)
		}
	}
	for _, versions := range deployments {
		for _, event := range versions {
			out.DeploymentEvents = append(out.DeploymentEvents, event)
		}
	}
	sort.Slice(out.DeploymentEvents, func(i, j int) bool {
		a, b := out.DeploymentEvents[i], out.DeploymentEvents[j]
		if a.DeploymentID == b.DeploymentID {
			return a.Version < b.Version
		}
		return a.DeploymentID < b.DeploymentID
	})
	for _, st := range erru.Must(q.ListLatestScheduledInstanceStatuses(ctx)) {
		if pinned := included[st.ScheduledInstanceID]; pinned != nil {
			status := apigen.WithRunningVersion(pinned, *st)
			out.InstanceStatuses = append(out.InstanceStatuses, &status)
		}
	}
	for _, row := range erru.Must(q.ListNodeRows(ctx, pq.AllNodeStatuses)) {
		out.NodeEvents = append(out.NodeEvents, &row.Event)
	}
	out.NodeStatuses = erru.Must(q.ListLatestNodeStatuses(ctx))
	out.SecretEvents = erru.Must(q.ListAllSecretEvents(ctx))
	out.ConfigEvents = erru.Must(q.ListAllConfigEvents(ctx))
	for _, row := range erru.Must(q.ListAllAssetEvents(ctx)) {
		out.AssetEvents = append(out.AssetEvents, ptru.To(row))
	}
	out.Spaces = pointers(erru.Must(q.ListSpaces(ctx)))
	out.Users = pointers(erru.Must(q.ListUsers(ctx)))
	out.ValueDirectories = erru.Must(q.ListValueDirectories(ctx))
	out.AssetDirectories = pointers(erru.Must(q.ListAssetDirectories(ctx)))
	for _, row := range erru.Must(q.ListLatestLiveNetworkPolicyEvents(ctx)) {
		out.NetworkPolicyEvents = append(out.NetworkPolicyEvents, row)
	}
	out.AuthzRuleTemplates = erru.Must(q.ListAuthzRuleTemplates(ctx))
	for _, row := range erru.Must(q.ListLatestLiveAuthzGrantEvents(ctx)) {
		out.AuthzGrantEvents = append(out.AuthzGrantEvents, ptru.To(row))
	}
	out.AuthzGlobalRules = erru.Must(q.ListAuthzGlobalRules(ctx))
	row, err := q.GetLatestSystemConfig(ctx)
	if err == nil {
		out.SystemConfig = row
	} else if !errors.Is(err, sql.ErrNoRows) {
		panic(err)
	}
	return out
}

func pointers[T any](items []T) []*T {
	out := make([]*T, 0, len(items))
	for i := range items {
		out = append(out, &items[i])
	}
	return out
}

type instanceOrdinalKey struct {
	deploymentID int32
	ordinal      int32
}

func ordinalKeyOf(inst *apigen.ScheduledInstance) instanceOrdinalKey {
	return instanceOrdinalKey{deploymentID: inst.DeploymentID, ordinal: inst.InstanceOrdinal}
}

func pinnedConfig(ctx context.Context, q *pq.Queries, deploymentID, deploymentVersion int32) *apigen.DeploymentEvent {
	event, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{
		DeploymentID: int64(deploymentID),
		Version:      int64(deploymentVersion),
	})
	if err != nil {
		panic(fmt.Sprintf("resolve deployment %d version %d: %v", deploymentID, deploymentVersion, err))
	}
	return event
}
