package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type schedulingInstance struct {
	Event  pq.ScheduledInstanceEvent
	Config apigen.DeploymentRecord
	Status apigen.ScheduledInstanceStatus
}

func readSchedulingState(ctx context.Context, q *pq.Queries, deploymentID uint64) (*apigen.DeploymentRecord, []schedulingInstance, error) {
	cfg, err := q.GetLatestDeployment(ctx, deploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		cfg = nil
	} else if err != nil {
		return nil, nil, err
	}
	events, err := q.ListNonFinalScheduledInstancesForDeployment(ctx, deploymentID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]schedulingInstance, 0, len(events))
	for _, event := range events {
		pinned, err := q.GetDeploymentVersion(ctx, event.Value.Deployment.DeploymentID, event.Value.Deployment.Version)
		if err != nil {
			return nil, nil, err
		}
		entry := schedulingInstance{Event: *event, Config: *pinned}
		status, err := q.GetLatestScheduledInstanceStatus(ctx, event.ScheduledInstanceID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		if status != nil {
			entry.Status = *status
		}
		out = append(out, entry)
	}
	return cfg, out, nil
}

func newInstance(ctx context.Context, q *pq.Queries, seq int64, cfg *apigen.DeploymentRecord, ordinal uint32, target apigen.ScheduledInstanceTarget, at time.Time) (*pq.ScheduledInstanceEvent, error) {
	id, err := q.NextScheduledInstanceID(ctx)
	if err != nil {
		return nil, err
	}
	inst := &apigen.ScheduledInstance{
		ID:              id,
		Deployment:      apigen.DeploymentRef{DeploymentID: cfg.Deployment.ID, Version: cfg.Meta.Version},
		NodeID:          cfg.Deployment.PlacementNodeID(),
		InstanceOrdinal: ordinal,
		SpaceID:         cfg.Deployment.SpaceID,
	}
	return pq.NewScheduledInstanceEvent(seq, inst, target, at), nil
}

func EnsureRunInstance(store *state.Service, deploymentID uint64, deploymentVersion uint32, nodeID uint64, instanceOrdinal uint32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
	ctx := context.Background()
	if !initial.WantsRunning() {
		panic(fmt.Sprintf("EnsureRunInstance: initial state %v is not runnable", initial))
	}
	var existing *apigen.ScheduledInstance
	now := time.Now()
	inst := &apigen.ScheduledInstance{
		Deployment:      apigen.DeploymentRef{DeploymentID: deploymentID, Version: deploymentVersion},
		NodeID:          nodeID,
		InstanceOrdinal: instanceOrdinal,
		State:           initial,
	}
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		events, err := q.ListNonFinalScheduledInstancesForDeployment(ctx, deploymentID)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			current := event.Value
			if current.Deployment.Version == deploymentVersion && current.NodeID == nodeID && current.InstanceOrdinal == instanceOrdinal && current.State.WantsRunning() {
				existing = &current
				return nil, nil
			}
		}
		cfg, err := q.GetDeploymentVersion(ctx, deploymentID, deploymentVersion)
		if err != nil {
			return nil, err
		}
		inst.SpaceID = cfg.Deployment.SpaceID
		inst.ID = erru.Must(q.NextScheduledInstanceID(ctx))
		event := pq.NewScheduledInstanceEvent(seq, inst, initial, now)
		return pq.NewUpdate(pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, event)), nil
	}); err != nil {
		panic(fmt.Sprintf("EnsureRunInstance: %v", err))
	}
	if existing != nil {
		return existing, false
	}
	return inst, true
}
