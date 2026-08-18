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
// of every scheduled instance on a node, except instances of the deployment
// identified by (exceptSpaceID, exceptName). Returns the number of status rows
// removed.
func (q *Queries) DeleteScheduledInstanceStatusesForNode(ctx context.Context, nodeID int64, exceptSpaceID int64, exceptName string) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		DELETE FROM scheduled_instance_status
		WHERE scheduled_instance_id IN (
			SELECT si.id FROM scheduled_instances si
			WHERE si.node_id = ?
				AND si.deployment_id IN (
					SELECT d.deployment_id FROM deployments d
					WHERE NOT ((SELECT sp.space_id FROM deployment_space_versions sp
					            WHERE sp.deployment_id = d.deployment_id
					            ORDER BY sp.version DESC LIMIT 1) = ? AND d.name = ?)
				)
		)`, nodeID, exceptSpaceID, exceptName)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteLocalKV removes one machine-local key. Missing keys are a no-op.
func (q *Queries) DeleteLocalKV(ctx context.Context, key string) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM local_kv WHERE key = ?`, key)
	return err
}
