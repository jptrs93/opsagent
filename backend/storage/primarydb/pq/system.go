package pq

import (
	"context"
)

// InsertSystemConfigRevision appends a settings revision and returns its row
// id.
func (q *Queries) InsertSystemConfigRevision(ctx context.Context, updatedAt int64, configBlob []byte) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
INSERT INTO system_config_revisions (updated_at, config_blob) VALUES (?, ?)
`, updatedAt, configBlob)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// DeleteScheduledInstanceStatusesForNode clears the recorded runtime statuses
// of every scheduled instance on a node, except instances of the deployments
// in exceptDeploymentIDs (resolved by the caller from the deployment cache).
// Returns the number of status rows removed.
func (q *Queries) DeleteScheduledInstanceStatusesForNode(ctx context.Context, nodeID int64, exceptDeploymentIDs []int64) (int64, error) {
	query := `
		DELETE FROM scheduled_instance_status
		WHERE scheduled_instance_id IN (
			SELECT DISTINCT e.scheduled_instance_id FROM scheduled_instance_event_log e
			WHERE e.node_id = ?`
	args := []any{nodeID}
	if len(exceptDeploymentIDs) > 0 {
		query += ` AND e.deployment_id NOT IN (` + placeholders(len(exceptDeploymentIDs)) + `)`
		for _, id := range exceptDeploymentIDs {
			args = append(args, id)
		}
	}
	query += `
		)`
	result, err := q.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func placeholders(n int) string {
	out := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
