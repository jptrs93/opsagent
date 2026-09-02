package pq

import (
	"context"
)

// Hand-written scheduled instance reads. An instance's history lives entirely
// in scheduled_instance_event_log: every row carries the full identity, the
// highest-version row carries the current state, and created_time is
// denormalised onto every row, so an instance read is a single row.

// ScheduledInstanceRow is an instance's latest event row.
type ScheduledInstanceRow struct {
	ID                    int64 // scheduled_instance_id
	CreatedTime           int64
	DeploymentID          int64
	DeploymentVersion     int64 // pinned deployment_event_log version
	DeploymentSpecVersion int64 // denormalised from the pinned version's row
	NodeID                int64
	InstanceOrdinal       int64
	SpaceID               int64
	State                 int64 // latest event's state
}

const scheduledInstanceRowSelect = `
	SELECT e.scheduled_instance_id, e.created_time,
	       e.deployment_id, e.deployment_version, e.deployment_spec_version,
	       e.node_id, e.instance_ordinal, e.space_id, e.state
	FROM scheduled_instance_event_log e
	JOIN (SELECT scheduled_instance_id, MAX(version) AS version
	      FROM scheduled_instance_event_log
	      GROUP BY scheduled_instance_id) latest
	  ON latest.scheduled_instance_id = e.scheduled_instance_id
	 AND latest.version = e.version`

type scheduledInstanceScanner interface {
	Scan(dest ...any) error
}

func scanScheduledInstanceRow(scanner scheduledInstanceScanner) (ScheduledInstanceRow, error) {
	var r ScheduledInstanceRow
	err := scanner.Scan(&r.ID, &r.CreatedTime, &r.DeploymentID, &r.DeploymentVersion,
		&r.DeploymentSpecVersion, &r.NodeID, &r.InstanceOrdinal, &r.SpaceID, &r.State)
	return r, err
}

func (q *Queries) GetScheduledInstance(ctx context.Context, id int64) (ScheduledInstanceRow, error) {
	return scanScheduledInstanceRow(q.db.QueryRowContext(ctx,
		scheduledInstanceRowSelect+` WHERE e.scheduled_instance_id = ?`, id))
}

func (q *Queries) ListNonFinalScheduledInstances(ctx context.Context) ([]ScheduledInstanceRow, error) {
	return q.listScheduledInstanceRows(ctx,
		scheduledInstanceRowSelect+` WHERE e.state != 2 ORDER BY e.scheduled_instance_id ASC`)
}

// ListLatestScheduledInstancePerOrdinal returns the newest incarnation of every
// (deployment, ordinal), whatever its state. Rebuilds the retained view of
// ordinals whose last instance has been finalized, which is all a stopped
// deployment has left to show.
func (q *Queries) ListLatestScheduledInstancePerOrdinal(ctx context.Context) ([]ScheduledInstanceRow, error) {
	return q.listScheduledInstanceRows(ctx, scheduledInstanceRowSelect+`
	JOIN (
	    SELECT deployment_id, instance_ordinal, MAX(scheduled_instance_id) AS scheduled_instance_id
	    FROM scheduled_instance_event_log
	    GROUP BY deployment_id, instance_ordinal
	) newest ON newest.scheduled_instance_id = e.scheduled_instance_id
	ORDER BY e.scheduled_instance_id ASC`)
}

func (q *Queries) listScheduledInstanceRows(ctx context.Context, query string) ([]ScheduledInstanceRow, error) {
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledInstanceRow
	for rows.Next() {
		r, err := scanScheduledInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
