package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) AppendScheduledInstanceEvent(ctx context.Context, seq int64, inst *apigen.ScheduledInstance, state apigen.ScheduledInstanceTarget, at time.Time) (*apigen.ScheduledInstanceEvent, error) {
	eventTime := at.UnixMilli()
	return scanScheduledInstanceEvent(q.db.QueryRowContext(ctx, `INSERT INTO scheduled_instance_event_log (
 scheduled_instance_id, version, global_seq, event_time, created_time,
 deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state)
 SELECT ?, COALESCE(MAX(version), 0) + 1, ?, ?, COALESCE(MIN(created_time), ?), ?, ?, ?, ?, ?, ?, ?
 FROM scheduled_instance_event_log WHERE scheduled_instance_id = ?
 RETURNING `+scheduledInstanceEventColumns,
		inst.ID, seq, eventTime, eventTime,
		inst.DeploymentID, inst.DeploymentVersion, inst.DeploymentSpecVersion, inst.NodeID, inst.InstanceOrdinal, inst.SpaceID, int64(state),
		inst.ID))
}
