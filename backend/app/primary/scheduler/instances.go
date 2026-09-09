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
	Event  apigen.ScheduledInstanceEvent
	Config apigen.DeploymentEvent
	Status apigen.ScheduledInstanceStatus
}

func readSchedulingState(ctx context.Context, q *pq.Queries, deploymentID int32) (*apigen.DeploymentEvent, []schedulingInstance, error) {
	cfg, err := q.GetLatestDeploymentEvent(ctx, int64(deploymentID))
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
		pinned, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(event.Value.DeploymentID), Version: int64(event.Value.DeploymentVersion)})
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

func newInstance(ctx context.Context, q *pq.Queries, seq int64, cfg *apigen.DeploymentEvent, ordinal int32, target apigen.ScheduledInstanceTarget, at time.Time) (*apigen.ScheduledInstanceEvent, error) {
	id, err := q.NextScheduledInstanceID(ctx)
	if err != nil {
		return nil, err
	}
	inst := &apigen.ScheduledInstance{ID: id, DeploymentID: cfg.DeploymentID, DeploymentVersion: cfg.Version, DeploymentSpecVersion: cfg.SpecVersion, NodeID: cfg.Value.NodeID, InstanceOrdinal: ordinal, SpaceID: cfg.Value.SpaceID}
	return q.AppendScheduledInstanceEvent(ctx, seq, inst, target, at)
}

func EnsureRunInstance(store *state.Service, deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, initial apigen.ScheduledInstanceTarget) (*apigen.ScheduledInstance, bool) {
	ctx := context.Background()
	if !initial.WantsRunning() {
		panic(fmt.Sprintf("EnsureRunInstance: initial state %v is not runnable", initial))
	}
	var existing *apigen.ScheduledInstance
	now := time.Now()
	inst := &apigen.ScheduledInstance{
		CreatedAt:         time.UnixMilli(now.UnixMilli()),
		DeploymentID:      deploymentID,
		DeploymentVersion: deploymentVersion,
		NodeID:            nodeID,
		InstanceOrdinal:   instanceOrdinal,
		State:             initial,
	}
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		events, err := q.ListNonFinalScheduledInstancesForDeployment(ctx, deploymentID)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			current := event.Value
			if current.DeploymentVersion == deploymentVersion && current.NodeID == nodeID && current.InstanceOrdinal == instanceOrdinal && current.State.WantsRunning() {
				existing = &current
				return nil, nil
			}
		}
		cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(deploymentID), Version: int64(deploymentVersion)})
		if err != nil {
			return nil, err
		}
		inst.DeploymentSpecVersion = cfg.SpecVersion
		inst.SpaceID = cfg.Value.SpaceID
		inst.ID = erru.Must(q.NextScheduledInstanceID(ctx))
		event, err := q.AppendScheduledInstanceEvent(ctx, seq, inst, initial, now)
		if err != nil {
			return nil, err
		}
		return &state.Update{ScheduledInstanceEvents: []*apigen.ScheduledInstanceEvent{event}}, nil
	}); err != nil {
		panic(fmt.Sprintf("EnsureRunInstance: %v", err))
	}
	if existing != nil {
		return existing, false
	}
	return inst, true
}
