package pq

import (
	"context"
)

// Hand-written scheduled instance reads. An instance's state history lives
// entirely in scheduled_instance_versions: creation time is the oldest row's
// created_at and current state the newest row's state, both identified by id.
// The v1 row is written in the same tx as the instance itself, so the join and
// subquery always find a row.

// ScheduledInstanceRow is an incarnation joined with its version log endpoints.
type ScheduledInstanceRow struct {
	ID                       int64
	CreatedAt                int64 // first version's created_at
	DeploymentID             int64
	DeploymentVersion        int64
	NodeID                   int64
	InstanceOrdinal          int64
	DeploymentSpaceVersionID int64
	SpaceID                  int64 // the pinned space version's space_id
	State                    int64 // latest version's state
}

const scheduledInstanceRowSelect = `
	SELECT si.id,
	       (SELECT f.created_at FROM scheduled_instance_versions f
	        WHERE f.scheduled_instance_id = si.id ORDER BY f.id LIMIT 1),
	       si.deployment_id, si.deployment_version, si.node_id, si.instance_ordinal,
	       si.deployment_space_version_id, COALESCE(sp.space_id, 0), v.state
	FROM scheduled_instances si
	JOIN scheduled_instance_versions v ON v.id =
	    (SELECT MAX(m.id) FROM scheduled_instance_versions m
	     WHERE m.scheduled_instance_id = si.id)
	LEFT JOIN deployment_space_versions sp ON sp.id = si.deployment_space_version_id`

type scheduledInstanceScanner interface {
	Scan(dest ...any) error
}

func scanScheduledInstanceRow(scanner scheduledInstanceScanner) (ScheduledInstanceRow, error) {
	var r ScheduledInstanceRow
	err := scanner.Scan(&r.ID, &r.CreatedAt, &r.DeploymentID, &r.DeploymentVersion,
		&r.NodeID, &r.InstanceOrdinal, &r.DeploymentSpaceVersionID, &r.SpaceID, &r.State)
	return r, err
}

func (q *Queries) GetScheduledInstance(ctx context.Context, id int64) (ScheduledInstanceRow, error) {
	return scanScheduledInstanceRow(q.db.QueryRowContext(ctx,
		scheduledInstanceRowSelect+` WHERE si.id = ?`, id))
}

func (q *Queries) ListNonFinalScheduledInstances(ctx context.Context) ([]ScheduledInstanceRow, error) {
	return q.listScheduledInstanceRows(ctx,
		scheduledInstanceRowSelect+` WHERE v.state != 2 ORDER BY si.id ASC`)
}

// ListLatestScheduledInstancePerOrdinal returns the newest incarnation of every
// (deployment, ordinal), whatever its state. Rebuilds the retained view of
// ordinals whose last instance has been finalized, which is all a stopped
// deployment has left to show.
func (q *Queries) ListLatestScheduledInstancePerOrdinal(ctx context.Context) ([]ScheduledInstanceRow, error) {
	return q.listScheduledInstanceRows(ctx, scheduledInstanceRowSelect+`
	JOIN (
	    SELECT deployment_id, instance_ordinal, MAX(id) AS id
	    FROM scheduled_instances
	    GROUP BY deployment_id, instance_ordinal
	) latest ON latest.id = si.id
	ORDER BY si.id ASC`)
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
