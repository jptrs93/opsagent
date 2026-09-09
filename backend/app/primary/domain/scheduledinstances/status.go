package scheduledinstances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type Store struct {
	*state.Service
}

func (s Store) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	WriteStatus(s.Service, instanceID, f)
}

func (s Store) FetchScheduledInstance(instanceID int32) *apigen.ScheduledInstance {
	inst, err := Live(context.Background(), s.Queries(), instanceID)
	if err != nil {
		return nil
	}
	return inst
}

func Live(ctx context.Context, q *pq.Queries, instanceID int32) (*apigen.ScheduledInstance, error) {
	event, err := q.GetScheduledInstance(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if event.Value.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
		return nil, nil
	}
	inst := event.Value
	return &inst, nil
}

func WriteStatus(store *state.Service, instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	ctx := context.Background()
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		inst, err := Live(ctx, q, instanceID)
		if err != nil || inst == nil {
			return nil, err
		}
		current, err := q.GetLatestScheduledInstanceStatus(ctx, instanceID)
		if errors.Is(err, sql.ErrNoRows) {
			current = &apigen.ScheduledInstanceStatus{}
		} else if err != nil {
			return nil, err
		}
		current.ScheduledInstanceID = instanceID
		current.DeploymentID = inst.DeploymentID
		if !f(current) {
			return nil, nil
		}
		current.ScheduledInstanceID = instanceID
		return writeStatus(ctx, q, seq, current)
	}); err != nil {
		panic(fmt.Sprintf("write scheduled instance status: %v", err))
	}
}

func WriteReplicatedStatus(store *state.Service, st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 || st.UpdatedAt.IsZero() {
		return
	}
	ctx := context.Background()
	current := *st
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		return writeStatus(ctx, q, seq, &current)
	}); err != nil && !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("write replicated instance status: %v", err))
	}
}

func writeStatus(ctx context.Context, q *pq.Queries, seq int64, st *apigen.ScheduledInstanceStatus) (*state.Update, error) {
	event, err := q.GetScheduledInstance(ctx, st.ScheduledInstanceID)
	if err != nil {
		return nil, err
	}
	inst := event.Value
	previous, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	st.DeploymentID = inst.DeploymentID
	publish := previous == nil || !st.UpdatedAt.Before(previous.UpdatedAt)
	rowSeq := int64(0)
	if publish {
		rowSeq = seq
	}
	if err := q.InsertScheduledInstanceStatus(ctx, rowSeq, st); err != nil {
		return nil, err
	}
	if !publish {
		return nil, nil
	}
	cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(inst.DeploymentID), Version: int64(inst.DeploymentVersion)})
	if err != nil {
		return nil, err
	}
	stored, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	observed := apigen.WithRunningVersion(cfg, *stored)
	return &state.Update{InstanceStatuses: []*apigen.ScheduledInstanceStatus{&observed}}, nil
}
