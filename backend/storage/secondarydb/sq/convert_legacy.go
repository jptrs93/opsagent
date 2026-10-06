package sq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/legacyconv"
)

// DataModelFormatVersion is the blob format of the secondary's local caches:
// 1 the v0.0.615 shapes under api-contract-old, 2 the contract rewritten from
// the data model. It lives in local_kv under formatVersionKey.
const DataModelFormatVersion = 2

const formatVersionKey = "format_version"

func legacyDataModel(db *sql.DB) bool {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'local_kv'`).Scan(&n); err != nil {
		panic(fmt.Errorf("format check: %w", err))
	}
	if n == 0 {
		return false
	}
	var raw []byte
	switch err := db.QueryRow(`SELECT value FROM local_kv WHERE key = ?`, formatVersionKey).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		return true
	case err != nil:
		panic(fmt.Errorf("format check: %w", err))
	}
	version, err := strconv.Atoi(string(raw))
	if err != nil {
		panic(fmt.Errorf("format check: local_kv %s = %q", formatVersionKey, raw))
	}
	if version > DataModelFormatVersion {
		panic(fmt.Sprintf("this database was written by a newer release (format %d, this binary reads %d)", version, DataModelFormatVersion))
	}
	return version < DataModelFormatVersion
}

func markFormatVersion(db *sql.DB) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO local_kv (key, value) VALUES (?, ?)`, formatVersionKey, []byte(strconv.Itoa(DataModelFormatVersion))); err != nil {
		panic(fmt.Errorf("format version: %w", err))
	}
}

// convertDataModel rewrites the version 1 caches in place, in one
// transaction. Every cache here is refetched from the primary at the next
// session, so a blob the rules refuse is dropped with an error logged rather
// than stopping the node: the instance cache row means that workload waits
// for the primary before it cold-starts, the network blobs mean the dataplane
// is programmed once the primary answers.
func convertDataModel(ctx context.Context, db *sql.DB) {
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	defer tx.Rollback()
	converted, dropped, err := convertInstanceCache(ctx, tx)
	if err != nil {
		panic(fmt.Errorf("data model conversion: instance cache: %w", err))
	}
	if err := convertLocalKV(ctx, tx); err != nil {
		panic(fmt.Errorf("data model conversion: local_kv: %w", err))
	}
	statuses, err := convertStatusRows(ctx, tx)
	if err != nil {
		panic(fmt.Errorf("data model conversion: status rows: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO local_kv (key, value) VALUES (?, ?)`, formatVersionKey, []byte(strconv.Itoa(DataModelFormatVersion))); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Errorf("data model conversion: %w", err))
	}
	slog.InfoContext(ctx, fmt.Sprintf("local caches converted to the data model contract in %s", time.Since(start).Round(time.Millisecond)),
		"instances", converted, "instances_dropped", dropped, "status_rows", statuses)
}

func convertInstanceCache(ctx context.Context, tx *sql.Tx) (converted, dropped int, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT instance_id, blob FROM local_scheduled_instance_cache`)
	if err != nil {
		return 0, 0, err
	}
	type cached struct {
		id   int64
		blob []byte
	}
	var all []cached
	for rows.Next() {
		var c cached
		if err := rows.Scan(&c.id, &c.blob); err != nil {
			rows.Close()
			return 0, 0, err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, c := range all {
		st, err := legacyconv.ScheduledInstanceState(c.blob)
		if err != nil {
			slog.ErrorContext(ctx, "cached instance state has no representation in the new model; the primary resends it", "err", err, "instance_id", c.id)
			if _, err := tx.ExecContext(ctx, `DELETE FROM local_scheduled_instance_cache WHERE instance_id = ?`, c.id); err != nil {
				return 0, 0, err
			}
			dropped++
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE local_scheduled_instance_cache SET instance_id = ?, blob = ? WHERE instance_id = ?`, st.Instance.ID, st.Encode(), c.id); err != nil {
			return 0, 0, err
		}
		converted++
	}
	return converted, dropped, nil
}

func convertLocalKV(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM local_kv WHERE key = ?`, storage.LocalKVAcmeState); err != nil {
		return err
	}
	for _, kv := range []struct {
		key     string
		convert func([]byte) ([]byte, error)
	}{
		{storage.LocalKVClusterNetwork, func(old []byte) ([]byte, error) {
			v, err := legacyconv.ClusterNetworkInfo(old)
			if err != nil {
				return nil, err
			}
			return v.Encode(), nil
		}},
		{storage.LocalKVClusterNetMap, func(old []byte) ([]byte, error) {
			v, err := legacyconv.ClusterNetMap(old)
			if err != nil {
				return nil, err
			}
			return v.Encode(), nil
		}},
	} {
		var old []byte
		switch err := tx.QueryRowContext(ctx, `SELECT value FROM local_kv WHERE key = ?`, kv.key).Scan(&old); {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return err
		}
		value, err := kv.convert(old)
		if err != nil {
			slog.ErrorContext(ctx, "cached network state has no representation in the new model; the primary resends it", "err", err, "key", kv.key)
			if _, err := tx.ExecContext(ctx, `DELETE FROM local_kv WHERE key = ?`, kv.key); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE local_kv SET value = ? WHERE key = ?`, value, kv.key); err != nil {
			return err
		}
	}
	return nil
}

func convertStatusRows(ctx context.Context, tx *sql.Tx) (int, error) {
	var hasDeploymentID int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('scheduled_instance_status') WHERE name = 'deployment_id'`).Scan(&hasDeploymentID); err != nil {
		return 0, err
	}
	if hasDeploymentID > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE scheduled_instance_status DROP COLUMN deployment_id`); err != nil {
			return 0, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT scheduled_instance_id, updated_at, runner_extra_blob FROM scheduled_instance_status WHERE length(runner_extra_blob) > 0`)
	if err != nil {
		return 0, err
	}
	type extra struct {
		id, updatedAt int64
		blob          []byte
	}
	var all []extra
	for rows.Next() {
		var e extra
		if err := rows.Scan(&e.id, &e.updatedAt, &e.blob); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, e := range all {
		converted, err := legacyconv.RunnerStatusExtra(e.blob)
		if err != nil {
			return 0, fmt.Errorf("instance %d at %d: %w", e.id, e.updatedAt, err)
		}
		if converted == nil {
			converted = []byte{}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_instance_status SET runner_extra_blob = ? WHERE scheduled_instance_id = ? AND updated_at = ?`, converted, e.id, e.updatedAt); err != nil {
			return 0, err
		}
	}
	return len(all), nil
}
