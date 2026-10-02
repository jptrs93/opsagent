package pq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const scheduledInstanceColumns = `e.id, e.deployment_id, e.deployment_version, e.deployment_spec_version, e.node_id, e.instance_ordinal, e.space_id, e.state, e.created_time, e.seq, e.event_time, e.author`

const latestScheduledInstanceStatusJoin = ` LEFT JOIN scheduled_instance_status s ON s.scheduled_instance_id = e.id`

func scanScheduledInstanceEventInto(event *ScheduledInstanceEvent, extra []any, row scanner) error {
	var state, author int64
	dest := []any{&event.ScheduledInstanceID, &event.Value.DeploymentID, &event.Value.DeploymentVersion, &event.Value.DeploymentSpecVersion,
		&event.Value.NodeID, &event.Value.InstanceOrdinal, &event.Value.SpaceID, &state, &event.CreatedTime, &event.Seq, &event.EventTime, &author}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	event.Author = int32(author)
	event.Value.ID = event.ScheduledInstanceID
	event.Value.State = apigen.ScheduledInstanceTarget(state)
	return nil
}

func scanScheduledInstanceEvent(row scanner) (*ScheduledInstanceEvent, error) {
	var event ScheduledInstanceEvent
	if err := scanScheduledInstanceEventInto(&event, nil, row); err != nil {
		return nil, err
	}
	return &event, nil
}

func (q *Queries) queryScheduledInstanceEvents(ctx context.Context, where string, args ...any) ([]*ScheduledInstanceEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+scheduledInstanceColumns+` FROM scheduled_instances e `+where+` ORDER BY e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*ScheduledInstanceEvent
	for rows.Next() {
		event, err := scanScheduledInstanceEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (q *Queries) NextScheduledInstanceID(ctx context.Context) (int32, error) {
	id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE)
	return int32(id), err
}

// GetScheduledInstance returns a retained instance, or sql.ErrNoRows once
// retention pruned it.
func (q *Queries) GetScheduledInstance(ctx context.Context, id int32) (*ScheduledInstanceEvent, error) {
	return scanScheduledInstanceEvent(q.db.QueryRowContext(ctx, `SELECT `+scheduledInstanceColumns+` FROM scheduled_instances e WHERE e.id = ?`, id))
}

// ListRetainedScheduledInstances returns every retained instance: the
// non-final ones plus the newest final per ordinal of a live deployment.
func (q *Queries) ListRetainedScheduledInstances(ctx context.Context) ([]*ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, ``)
}

func (q *Queries) ListNonFinalScheduledInstances(ctx context.Context) ([]*ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `WHERE e.state != ?`, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}

func (q *Queries) ListNonFinalScheduledInstancesForDeployment(ctx context.Context, deploymentID int32) ([]*ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `WHERE e.deployment_id = ? AND e.state != ?`, deploymentID, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}

func (q *Queries) ListRetainedScheduledInstancesForNode(ctx context.Context, nodeID int32) ([]*ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `WHERE e.node_id = ?`, nodeID)
}

func (q *Queries) ListDrainingDeploymentIDs(ctx context.Context) ([]int32, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT deployment_id FROM scheduled_instances WHERE state = ? ORDER BY deployment_id`,
		int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (q *Queries) scanScheduledInstanceState(ctx context.Context, row scanner) (*apigen.ScheduledInstanceState, error) {
	var event ScheduledInstanceEvent
	var status scheduledInstanceStatusRow
	if err := scanScheduledInstanceEventInto(&event, status.fields(), row); err != nil {
		return nil, err
	}
	cfg, err := q.GetDeploymentEventByVersion(ctx, GetDeploymentEventByVersionParams{DeploymentID: int64(event.Value.DeploymentID), Version: int64(event.Value.DeploymentVersion)})
	if err != nil {
		return nil, err
	}
	state := &apigen.ScheduledInstanceState{Instance: event.Value, Config: *cfg}
	if st := status.toStatus(); st != nil {
		state.Status = apigen.WithRunningVersion(cfg, *st)
	}
	return state, nil
}

func (q *Queries) queryScheduledInstanceStates(ctx context.Context, where string, args ...any) ([]apigen.ScheduledInstanceState, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+scheduledInstanceColumns+`, `+scheduledInstanceStatusColumnsS+` FROM scheduled_instances e`+
		latestScheduledInstanceStatusJoin+` `+where+` ORDER BY e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []apigen.ScheduledInstanceState
	for rows.Next() {
		state, err := q.scanScheduledInstanceState(ctx, rows)
		if err != nil {
			return nil, err
		}
		states = append(states, *state)
	}
	return states, rows.Err()
}

func (q *Queries) GetScheduledInstanceState(ctx context.Context, id int32) (*apigen.ScheduledInstanceState, error) {
	states, err := q.queryScheduledInstanceStates(ctx, `WHERE e.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, sql.ErrNoRows
	}
	return &states[0], nil
}

// PrunedScheduledInstanceState is the state of an instance that the commit
// u finalized and retention pruned in the same commit, so its rows are gone
// by the time subscribers read it: the instance comes from u's own payload,
// the pinned version from the tables while another instance still pins it
// and from the write log otherwise, and the status from u or the log. It
// returns nil when u carries no instance payload for id, which is a late
// status report for an instance pruned earlier.
func (q *Queries) PrunedScheduledInstanceState(ctx context.Context, u *apigen.CoreWriteUpdate, id int32) (*apigen.ScheduledInstanceState, error) {
	var inst *apigen.ScheduledInstance
	var status *apigen.ScheduledInstanceStatus
	for _, m := range u.Mutations {
		if m.EntityID() != int64(id) || m.Entity() == nil {
			continue
		}
		switch m.Type() {
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE:
			inst = m.Entity().ScheduledInstance
		case apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS:
			status = m.Entity().ScheduledInstanceStatus
		}
	}
	if inst == nil {
		return nil, nil
	}
	cfg, err := q.GetDeploymentEventByVersion(ctx, GetDeploymentEventByVersionParams{DeploymentID: int64(inst.DeploymentID), Version: int64(inst.DeploymentVersion)})
	if errors.Is(err, sql.ErrNoRows) {
		cfg, err = q.deploymentVersionFromLog(ctx, int64(inst.DeploymentID), int64(inst.DeploymentVersion))
	}
	if err != nil {
		return nil, err
	}
	if status == nil {
		latest, err := q.LatestMutation(ctx, apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, int64(id))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if latest != nil && latest.Entity() != nil {
			status = latest.Entity().ScheduledInstanceStatus
		}
	}
	value := *inst
	value.ID = id
	state := &apigen.ScheduledInstanceState{Instance: value, Config: *cfg}
	if status != nil {
		state.Status = apigen.WithRunningVersion(cfg, *status)
	}
	return state, nil
}

func (q *Queries) ListLiveScheduledInstanceStates(ctx context.Context) ([]apigen.ScheduledInstanceState, error) {
	return q.queryScheduledInstanceStates(ctx, `WHERE e.state != ?`, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}

// NewScheduledInstanceEvent is the create of an instance whose id the caller
// allocated; its created time is the commit's. Nothing is written.
func NewScheduledInstanceEvent(seq int64, inst *apigen.ScheduledInstance, state apigen.ScheduledInstanceTarget, at time.Time) *ScheduledInstanceEvent {
	eventTime := at.UnixMilli()
	value := *inst
	value.State = state
	return &ScheduledInstanceEvent{ScheduledInstanceID: inst.ID, Seq: seq, CreatedTime: eventTime, EventTime: eventTime, Value: value}
}

// ScheduledInstanceTransition is the instance after a state change, under
// the commit's seq and time. Nothing is written.
func ScheduledInstanceTransition(seq int64, current *ScheduledInstanceEvent, state apigen.ScheduledInstanceTarget, at time.Time) *ScheduledInstanceEvent {
	next := *current
	next.Seq, next.EventTime, next.Author = seq, at.UnixMilli(), 0
	next.Value.State = state
	return &next
}

func (q *Queries) reduceScheduledInstance(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, inst *apigen.ScheduledInstance) error {
	if inst == nil {
		return fmt.Errorf("payload has no instance")
	}
	return q.upsert(ctx, meta, `INSERT INTO scheduled_instances (id, deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state, created_time, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET deployment_id = excluded.deployment_id, deployment_version = excluded.deployment_version, deployment_spec_version = excluded.deployment_spec_version,
  node_id = excluded.node_id, instance_ordinal = excluded.instance_ordinal, space_id = excluded.space_id, state = excluded.state,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(inst.DeploymentID), int64(inst.DeploymentVersion), int64(inst.DeploymentSpecVersion), int64(inst.NodeID), int64(inst.InstanceOrdinal), int64(inst.SpaceID), int64(inst.State),
		env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteScheduledInstanceRows(ctx context.Context, id int64) error {
	if err := q.deleteScheduledInstanceStatusRow(ctx, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM scheduled_instances WHERE id = ?`, id)
	return err
}

func (q *Queries) scheduledInstanceDeploymentID(ctx context.Context, id int64) (int64, bool, error) {
	var deployment int64
	err := q.db.QueryRowContext(ctx, `SELECT deployment_id FROM scheduled_instances WHERE id = ?`, id).Scan(&deployment)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return deployment, err == nil, err
}
