package pq

import (
	"context"
	"database/sql"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const scheduledInstanceEventColumns = `id, global_seq, event_time, created_time, scheduled_instance_id, version,
 deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state`

const scheduledInstanceEventColumnsE = `e.id, e.global_seq, e.event_time, e.created_time, e.scheduled_instance_id, e.version,
 e.deployment_id, e.deployment_version, e.deployment_spec_version, e.node_id, e.instance_ordinal, e.space_id, e.state`

const latestScheduledInstanceEventsFrom = `FROM scheduled_instance_event_log e
 JOIN (SELECT scheduled_instance_id, MAX(version) AS version FROM scheduled_instance_event_log GROUP BY scheduled_instance_id) latest
 ON latest.scheduled_instance_id = e.scheduled_instance_id AND latest.version = e.version`

const latestScheduledInstanceStatusJoin = ` LEFT JOIN scheduled_instance_status s ON s.scheduled_instance_id = e.scheduled_instance_id
 AND s.updated_at = (SELECT MAX(updated_at) FROM scheduled_instance_status WHERE scheduled_instance_id = e.scheduled_instance_id)`

func scanScheduledInstanceEventInto(event *apigen.ScheduledInstanceEvent, extra []any, row scanner) error {
	var state int64
	dest := []any{&event.EventID, &event.Seq, &event.EventTime, &event.CreatedTime, &event.ScheduledInstanceID, &event.Version,
		&event.Value.DeploymentID, &event.Value.DeploymentVersion, &event.Value.DeploymentSpecVersion,
		&event.Value.NodeID, &event.Value.InstanceOrdinal, &event.Value.SpaceID, &state}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	event.Value.ID = event.ScheduledInstanceID
	event.Value.CreatedAt = millisToTime(event.CreatedTime)
	event.Value.State = apigen.ScheduledInstanceTarget(state)
	event.EventType = apigen.EventType_EVENT_TYPE_UPDATE
	if event.Version == 1 {
		event.EventType = apigen.EventType_EVENT_TYPE_CREATE
	}
	return nil
}

func scanScheduledInstanceEvent(row scanner) (*apigen.ScheduledInstanceEvent, error) {
	var event apigen.ScheduledInstanceEvent
	if err := scanScheduledInstanceEventInto(&event, nil, row); err != nil {
		return nil, err
	}
	return &event, nil
}

func (q *Queries) queryScheduledInstanceEvents(ctx context.Context, query string, args ...any) ([]*apigen.ScheduledInstanceEvent, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*apigen.ScheduledInstanceEvent
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
	var id int32
	err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(scheduled_instance_id), 0) + 1 FROM scheduled_instance_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetScheduledInstance(ctx context.Context, id int32) (*apigen.ScheduledInstanceEvent, error) {
	return scanScheduledInstanceEvent(q.db.QueryRowContext(ctx, `SELECT `+scheduledInstanceEventColumns+` FROM scheduled_instance_event_log
 WHERE scheduled_instance_id = ? ORDER BY version DESC LIMIT 1`, id))
}

func (q *Queries) ListLatestScheduledInstanceEvents(ctx context.Context) ([]*apigen.ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `SELECT `+scheduledInstanceEventColumnsE+` `+latestScheduledInstanceEventsFrom+` ORDER BY e.scheduled_instance_id`)
}

func (q *Queries) ListNonFinalScheduledInstances(ctx context.Context) ([]*apigen.ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `SELECT `+scheduledInstanceEventColumnsE+` `+latestScheduledInstanceEventsFrom+`
 WHERE e.state != ? ORDER BY e.scheduled_instance_id`, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}

func (q *Queries) ListNonFinalScheduledInstancesForDeployment(ctx context.Context, deploymentID int32) ([]*apigen.ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `SELECT `+scheduledInstanceEventColumnsE+` `+latestScheduledInstanceEventsFrom+`
 WHERE e.deployment_id = ? AND e.state != ? ORDER BY e.scheduled_instance_id`, deploymentID, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}

func (q *Queries) ListLatestScheduledInstancePerOrdinal(ctx context.Context) ([]*apigen.ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `SELECT `+scheduledInstanceEventColumnsE+` `+latestScheduledInstanceEventsFrom+`
 JOIN (SELECT deployment_id, instance_ordinal, MAX(scheduled_instance_id) AS scheduled_instance_id
       FROM scheduled_instance_event_log GROUP BY deployment_id, instance_ordinal) newest
 ON newest.scheduled_instance_id = e.scheduled_instance_id
 ORDER BY e.scheduled_instance_id`)
}

func (q *Queries) ListScheduledInstanceEventsAtSeq(ctx context.Context, seq int64) ([]*apigen.ScheduledInstanceEvent, error) {
	return q.queryScheduledInstanceEvents(ctx, `SELECT `+scheduledInstanceEventColumns+` FROM scheduled_instance_event_log WHERE global_seq = ? ORDER BY id`, seq)
}

func (q *Queries) ListDrainingDeploymentIDs(ctx context.Context) ([]int32, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT e.deployment_id `+latestScheduledInstanceEventsFrom+`
 WHERE e.state = ? ORDER BY e.deployment_id`, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING))
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
	var event apigen.ScheduledInstanceEvent
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
	rows, err := q.db.QueryContext(ctx, `SELECT `+scheduledInstanceEventColumnsE+`, `+scheduledInstanceStatusColumnsS+` `+
		latestScheduledInstanceEventsFrom+latestScheduledInstanceStatusJoin+` `+where+` ORDER BY e.scheduled_instance_id`, args...)
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
	states, err := q.queryScheduledInstanceStates(ctx, `WHERE e.scheduled_instance_id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, sql.ErrNoRows
	}
	return &states[0], nil
}

func (q *Queries) ListLiveScheduledInstanceStates(ctx context.Context) ([]apigen.ScheduledInstanceState, error) {
	return q.queryScheduledInstanceStates(ctx, `WHERE e.state != ?`, int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED))
}
