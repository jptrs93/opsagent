package pq

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// One-time v0.0.611 shape migration: Deployment gained a scheduling message
// holding the placement that used to be node_id and the desired running state
// that used to be ContainerSpec.running (or, for the opendeploy system
// deployment, the presence of opendeploy_spec). Rows whose value has no
// dedicated_nodes are lifted in place; the legacy running flag is cleared so
// the next spec comparison does not see a phantom change. Remove after every
// active cluster has rolled forward, per the migrations.sql history-note
// convention.
func migrateDeploymentScheduling(db *sql.DB) {
	ctx := context.Background()
	if err := migrateDeploymentSchedulingRows(ctx, db); err != nil {
		panic(fmt.Errorf("deployment scheduling migration: %w", err))
	}
}

func migrateDeploymentSchedulingRows(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, value FROM deployment_event_log ORDER BY id`)
	if err != nil {
		return err
	}
	type pending struct {
		id    int64
		value []byte
	}
	var updates []pending
	for rows.Next() {
		var id int64
		var value []byte
		if err := rows.Scan(&id, &value); err != nil {
			rows.Close()
			return err
		}
		def, err := apigen.DecodeDeployment(value)
		if err != nil {
			rows.Close()
			return fmt.Errorf("row %d: %w", id, err)
		}
		if def.Scheduling.DedicatedNodes != nil {
			continue
		}
		running := def.Spec.OpendeploySpec != nil
		if container := def.Spec.Container(); container != nil {
			running = container.Running
			container.Running = false
		}
		def.Scheduling = apigen.DedicatedScheduling(running, def.NodeID)
		updates = append(updates, pending{id: id, value: def.Encode()})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE deployment_event_log SET value = ? WHERE id = ?`, u.value, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
