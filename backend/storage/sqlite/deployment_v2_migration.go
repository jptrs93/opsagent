package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const deploymentV2MigrationKey = "migration_deployment_config_v2"

type legacyDeploymentSpecRow struct {
	deploymentID   int64
	version        int64
	specBlob       []byte
	desiredVersion string
	desiredRunning int64
}

// migrateDeploymentConfigsV2 rewrites every current and historical V1
// DeploymentSpec before any deployment state is loaded. This migration is
// intentionally temporary and can be removed after every installation has
// completed one startup with the V2 release.
func migrateDeploymentConfigsV2(db *sql.DB) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin deployment V2 migration: %v", err))
	}
	defer tx.Rollback()

	var marker []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM local_kv WHERE key = ?`, deploymentV2MigrationKey).Scan(&marker)
	if err == nil {
		if !bytes.Equal(marker, []byte{1}) {
			panic(fmt.Sprintf("invalid deployment V2 migration marker: %x", marker))
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("read deployment V2 migration marker: %v", err))
	}

	configs := loadLegacyDeploymentSpecRows(tx, `
		SELECT deployment_id, version, spec_blob, desired_version, desired_running
		FROM deployment_configs
		ORDER BY deployment_id`, "deployment configs")
	history := loadLegacyDeploymentSpecRows(tx, `
		SELECT deployment_id, version, spec_blob, desired_version, desired_running
		FROM deployment_config_history
		ORDER BY deployment_id, version`, "deployment config history")

	rewriteLegacyDeploymentSpecRows(tx, "deployment_configs", configs)
	rewriteLegacyDeploymentSpecRows(tx, "deployment_config_history", history)

	if _, err := tx.ExecContext(ctx, `INSERT INTO local_kv (key, value) VALUES (?, ?)`, deploymentV2MigrationKey, []byte{1}); err != nil {
		panic(fmt.Sprintf("write deployment V2 migration marker: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit deployment V2 migration: %v", err))
	}
	slog.Info("deployment configs migrated to V2", "configs", len(configs), "history", len(history))
}

func loadLegacyDeploymentSpecRows(tx *sql.Tx, query, label string) []legacyDeploymentSpecRow {
	rows, err := tx.QueryContext(context.Background(), query)
	if err != nil {
		panic(fmt.Sprintf("load %s for V2 migration: %v", label, err))
	}
	defer rows.Close()

	var out []legacyDeploymentSpecRow
	for rows.Next() {
		var row legacyDeploymentSpecRow
		if err := rows.Scan(&row.deploymentID, &row.version, &row.specBlob, &row.desiredVersion, &row.desiredRunning); err != nil {
			panic(fmt.Sprintf("scan %s for V2 migration: %v", label, err))
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("iterate %s for V2 migration: %v", label, err))
	}
	return out
}

func rewriteLegacyDeploymentSpecRows(tx *sql.Tx, table string, rows []legacyDeploymentSpecRow) {
	query := fmt.Sprintf(`UPDATE %s SET spec_blob = ? WHERE deployment_id = ? AND version = ?`, table)
	for _, row := range rows {
		legacy, err := apigen.DecodeDeploymentSpec(row.specBlob)
		if err != nil {
			panic(fmt.Sprintf("decode V1 %s deployment %d version %d: %v", table, row.deploymentID, row.version, err))
		}
		v2, err := apigen.DeploymentSpecToV2(legacy, apigen.DesiredState{
			Version: row.desiredVersion,
			Running: row.desiredRunning != 0,
		})
		if err != nil {
			panic(fmt.Sprintf("convert V1 %s deployment %d version %d: %v", table, row.deploymentID, row.version, err))
		}
		if _, err := tx.ExecContext(context.Background(), query, v2.Encode(), row.deploymentID, row.version); err != nil {
			panic(fmt.Sprintf("rewrite V2 %s deployment %d version %d: %v", table, row.deploymentID, row.version, err))
		}
	}
}
