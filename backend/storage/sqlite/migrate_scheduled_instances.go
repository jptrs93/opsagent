package sqlite

// One-shot migration from the schema where a deployment had a single mutable
// deployment_status row to scheduled instances, where every runtime incarnation
// of a deployment is its own row with its own status history. It exists only
// for clusters upgrading across that boundary, and is meant to be deleted along
// with the legacy tables it reads once every cluster has rolled over.
//
// The primary and the secondary migrate independently, with no coordination, so
// the two must land on identical scheduled instance ids. The primary's snapshot
// is authoritative and applySnapshot finalizes every locally held instance
// missing from it, so disagreeing here would tear down and recreate every
// workload in the cluster. deployment_id is the only identifier both sides
// already hold for a running workload, so it is reused verbatim as the
// scheduled instance id. scheduled_instances.id is AUTOINCREMENT and an
// explicit insert carries sqlite_sequence up to the highest rowid written, so
// ids minted after the migration never collide with the ones it chose.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// localKVScheduledInstanceMigration records that the migration has run. It is a
// marker rather than a test for an empty scheduled_instances table because an
// empty instance set is a legitimate steady state — everything finalized — and
// re-running against the retained legacy tables would resurrect assignments the
// cluster has already retired.
const localKVScheduledInstanceMigration = "scheduled_instance_migration_v1"

// migratedInstanceOrdinal is the only ordinal the pre-migration schema could
// express: one deployment, one placement.
const migratedInstanceOrdinal = 0

// mustMigrateScheduledInstancesPrimary writes the scheduled_instances rows. The
// primary owns that table: it is where assignments are minted, and the worker
// only ever receives them over the cluster stream.
func mustMigrateScheduledInstancesPrimary(db *sql.DB) {
	mustMigrateScheduledInstances(db, false)
}

// mustMigrateScheduledInstancesSecondary writes local_scheduled_instance_cache
// instead. That table is the durable assignment source a worker's operator runs
// from, and MustLoadRuntimeConfig panics when it holds no netproxy instance, so
// an unmigrated secondary cannot boot far enough to be handed assignments by the
// primary in the first place.
func mustMigrateScheduledInstancesSecondary(db *sql.DB) {
	mustMigrateScheduledInstances(db, true)
}

func mustMigrateScheduledInstances(db *sql.DB, secondary bool) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin scheduled instance migration: %v", err))
	}
	defer tx.Rollback()
	q := New(tx)

	if _, err := q.GetLocalKV(ctx, localKVScheduledInstanceMigration); err == nil {
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("read scheduled instance migration marker: %v", err))
	}

	configs := legacyRunningConfigs(ctx, tx)
	for _, cfg := range configs {
		status := legacyDeploymentStatus(ctx, tx, cfg.ID)
		insertMigratedInstance(ctx, tx, q, cfg, status, secondary)
	}

	if err := q.UpsertLocalKV(ctx, UpsertLocalKVParams{Key: localKVScheduledInstanceMigration, Value: []byte{}}); err != nil {
		panic(fmt.Sprintf("record scheduled instance migration marker: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit scheduled instance migration: %v", err))
	}
	if len(configs) > 0 {
		slog.Info("migrated legacy deployments to scheduled instances", "count", len(configs))
	}
}

// legacyRunningConfigs returns the deployments that must become scheduled
// instances: live configs whose workload is meant to be running. A stopped or
// deleted deployment gets none. It has no container to adopt, and the scheduler
// creates an instance for it the moment it is started again.
func legacyRunningConfigs(ctx context.Context, tx *sql.Tx) []*apigen.DeploymentConfig {
	rows, err := tx.QueryContext(ctx, `
SELECT deployment_id, node_id, space_id, name, created_at, version, updated_at, updated_by, spec_blob, deleted
FROM deployment_configs
ORDER BY deployment_id`)
	if err != nil {
		panic(fmt.Sprintf("list deployments for scheduled instance migration: %v", err))
	}
	defer rows.Close()

	out := make([]*apigen.DeploymentConfig, 0)
	for rows.Next() {
		var r ListAllDeploymentConfigsRow
		if err := rows.Scan(&r.DeploymentID, &r.NodeID, &r.SpaceID, &r.Name, &r.CreatedAt,
			&r.Version, &r.UpdatedAt, &r.UpdatedBy, &r.SpecBlob, &r.Deleted); err != nil {
			panic(fmt.Sprintf("scan deployment for scheduled instance migration: %v", err))
		}
		if r.Deleted != 0 || r.NodeID <= 0 {
			continue
		}
		cfg := configRowToProto(r)
		if !cfg.WorkloadRunning() {
			continue
		}
		out = append(out, cfg)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("read deployments for scheduled instance migration: %v", err))
	}
	return out
}

// legacyDeploymentStatus reads the single mutable status row the old schema kept
// per deployment, or nil when the deployment has never reported one.
//
// Carrying the status over is what keeps the migration from restarting every
// workload in the cluster: ReAttachRunning hands back a stopped runner when the
// previous status is empty, and the operator then creates a container over the
// deterministic id of the one already running. runner_config_version in
// particular has to survive verbatim, because it is half of that id.
//
// updated_at is an HLC and is copied unchanged. Both sides deriving the same
// value is what leaves the reconnect backlog replay in applySnapshot empty.
func legacyDeploymentStatus(ctx context.Context, tx *sql.Tx, deploymentID int32) *apigen.ScheduledInstanceStatus {
	row := ScheduledInstanceStatus{
		ScheduledInstanceID: int64(deploymentID),
		DeploymentID:        int64(deploymentID),
	}
	err := tx.QueryRowContext(ctx, `
SELECT updated_at, preparer_config_version, preparer_artifact, preparer_status,
       runner_config_version, runner_pid, runner_artifact, runner_status,
       runner_num_restarts, runner_last_restart_at, runner_extra_blob
FROM deployment_status
WHERE deployment_id = ?`, deploymentID).Scan(
		&row.UpdatedAt, &row.PreparerConfigVersion, &row.PreparerArtifact, &row.PreparerStatus,
		&row.RunnerConfigVersion, &row.RunnerPid, &row.RunnerArtifact, &row.RunnerStatus,
		&row.RunnerNumRestarts, &row.RunnerLastRestartAt, &row.RunnerExtraBlob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(fmt.Sprintf("read legacy status for deployment %d: %v", deploymentID, err))
	}
	// The old schema inserted a placeholder row for every deployment and used a
	// zero clock to mean "nothing observed yet".
	if row.UpdatedAt == 0 {
		return nil
	}
	return scheduledInstanceStatusRowToProto(row)
}

// insertMigratedInstance writes the one placement a legacy deployment implies.
// It is created RUN_SERVING because it is the only placement of its instance and
// therefore already owns the instance's stable inbound address; that address is
// a pure function of space, deployment, and ordinal, so it does not move here.
func insertMigratedInstance(
	ctx context.Context,
	tx *sql.Tx,
	q *Queries,
	cfg *apigen.DeploymentConfig,
	status *apigen.ScheduledInstanceStatus,
	secondary bool,
) {
	inst := apigen.ScheduledInstance{
		ID:                cfg.ID,
		CreatedAt:         cfg.UpdatedAt,
		DeploymentID:      cfg.ID,
		DeploymentVersion: cfg.Version,
		NodeID:            cfg.NodeID,
		InstanceOrdinal:   migratedInstanceOrdinal,
		State:             apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
	}

	if status != nil {
		if err := q.InsertScheduledInstanceStatus(ctx, scheduledInstanceStatusProtoToInsertParams(status)); err != nil {
			panic(fmt.Sprintf("insert scheduled instance status for deployment %d: %v", cfg.ID, err))
		}
	}

	if secondary {
		state := &apigen.ScheduledInstanceState{Instance: inst, Config: *cfg}
		if status != nil {
			state.Status = *status
		}
		if err := q.UpsertLocalScheduledInstanceCache(ctx, UpsertLocalScheduledInstanceCacheParams{
			InstanceID: int64(inst.ID),
			Blob:       state.Encode(),
		}); err != nil {
			panic(fmt.Sprintf("seed local scheduled instance cache for deployment %d: %v", cfg.ID, err))
		}
		return
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO scheduled_instances (id, created_at, deployment_id, deployment_version, node_id, instance_ordinal, state)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inst.ID, timeToMillis(inst.CreatedAt), inst.DeploymentID, inst.DeploymentVersion,
		inst.NodeID, inst.InstanceOrdinal, int64(inst.State)); err != nil {
		panic(fmt.Sprintf("insert scheduled instance for deployment %d: %v", cfg.ID, err))
	}
}
