package pq

import (
	"database/sql"
	"fmt"
)

// One-time v0.0.567 shape migration: the deployment/asset/secret/config event
// logs gained *_changed flag columns (1 iff the event bumped the matching
// sub-version), and the payload columns that used NULL as the "this event
// wrote a value" indicator (asset size_bytes/sha256, secret
// smk_version/ciphertext/nonce, config value) became NOT NULL snapshots
// carried forward on every row. SQLite cannot ALTER a column to NOT NULL, so
// old-shape tables are renamed aside before ApplySchema recreates them
// (CREATE TABLE IF NOT EXISTS would silently no-op against the old shape) and
// copied back afterwards. Remove after every active cluster has rolled
// forward, per the migrations.sql history-note convention.

// eventFlagMigrations lists, per table, the flag column whose absence marks
// the old shape, the named indexes that must be dropped with the old table
// (they keep their names through a rename and would block ApplySchema's
// CREATE INDEX IF NOT EXISTS), and the copy statement. Copies preserve row
// ids — they are the pinned version ids — and recompute the flags from the
// sub-version deltas, so the backfill matches what the write path now stamps.
var eventFlagMigrations = []struct {
	table   string
	flagCol string
	indexes []string
	copySQL string
}{
	{
		table:   "deployment_event_log",
		flagCol: "spec_changed",
		indexes: []string{"idx_deployment_event_log_spec_version"},
		copySQL: `
			INSERT INTO deployment_event_log (
				id, global_seq, event_time, created_time, author, deployment_id, version,
				spec_version, space_assignment_version, name_version,
				spec_changed, space_assignment_changed, name_changed, value, event_type)
			SELECT id, global_seq, event_time, created_time, author, deployment_id, version,
				spec_version, space_assignment_version, name_version,
				spec_version != COALESCE(LAG(spec_version) OVER w, 0),
				space_assignment_version != COALESCE(LAG(space_assignment_version) OVER w, 0),
				name_version != COALESCE(LAG(name_version) OVER w, 0),
				value, event_type
			FROM deployment_event_log_legacy
			WINDOW w AS (PARTITION BY deployment_id ORDER BY version)
			ORDER BY id`,
	},
	{
		table:   "asset_event_log",
		flagCol: "value_changed",
		indexes: []string{"idx_asset_event_log_sha256"},
		copySQL: `
			INSERT INTO asset_event_log (
				id, global_seq, event_time, created_time, author, asset_id, version,
				value_version, space_version, value_changed, space_changed,
				key, asset_directory_id, space_id, size_bytes, sha256, event_type)
			SELECT id, global_seq, event_time, created_time, author, asset_id, version,
				value_version, space_version,
				value_version != COALESCE(LAG(value_version) OVER w, 0),
				space_version != COALESCE(LAG(space_version) OVER w, 0),
				key, asset_directory_id, space_id,
				COALESCE(size_bytes, (SELECT p.size_bytes FROM asset_event_log_legacy p
					WHERE p.asset_id = l.asset_id AND p.version < l.version AND p.size_bytes IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), 0),
				COALESCE(sha256, (SELECT p.sha256 FROM asset_event_log_legacy p
					WHERE p.asset_id = l.asset_id AND p.version < l.version AND p.sha256 IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), ''),
				event_type
			FROM asset_event_log_legacy l
			WINDOW w AS (PARTITION BY asset_id ORDER BY version)
			ORDER BY id`,
	},
	{
		table:   "secret_event_log",
		flagCol: "value_changed",
		copySQL: `
			INSERT INTO secret_event_log (
				id, global_seq, event_time, created_time, author, secret_id, version,
				value_version, space_version, value_changed, space_changed,
				name, value_directory_id, space_id, smk_version, ciphertext, nonce, event_type)
			SELECT id, global_seq, event_time, created_time, author, secret_id, version,
				value_version, space_version,
				value_version != COALESCE(LAG(value_version) OVER w, 0),
				space_version != COALESCE(LAG(space_version) OVER w, 0),
				name, value_directory_id, space_id,
				COALESCE(smk_version, (SELECT p.smk_version FROM secret_event_log_legacy p
					WHERE p.secret_id = l.secret_id AND p.version < l.version AND p.smk_version IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), 0),
				COALESCE(ciphertext, (SELECT p.ciphertext FROM secret_event_log_legacy p
					WHERE p.secret_id = l.secret_id AND p.version < l.version AND p.ciphertext IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), x''),
				COALESCE(nonce, (SELECT p.nonce FROM secret_event_log_legacy p
					WHERE p.secret_id = l.secret_id AND p.version < l.version AND p.nonce IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), x''),
				event_type
			FROM secret_event_log_legacy l
			WINDOW w AS (PARTITION BY secret_id ORDER BY version)
			ORDER BY id`,
	},
	{
		table:   "config_event_log",
		flagCol: "value_changed",
		copySQL: `
			INSERT INTO config_event_log (
				id, global_seq, event_time, created_time, author, config_id, version,
				value_version, space_version, value_changed, space_changed,
				name, value_directory_id, space_id, value, event_type)
			SELECT id, global_seq, event_time, created_time, author, config_id, version,
				value_version, space_version,
				value_version != COALESCE(LAG(value_version) OVER w, 0),
				space_version != COALESCE(LAG(space_version) OVER w, 0),
				name, value_directory_id, space_id,
				COALESCE(value, (SELECT p.value FROM config_event_log_legacy p
					WHERE p.config_id = l.config_id AND p.version < l.version AND p.value IS NOT NULL
					ORDER BY p.version DESC LIMIT 1), ''),
				event_type
			FROM config_event_log_legacy l
			WINDOW w AS (PARTITION BY config_id ORDER BY version)
			ORDER BY id`,
	},
}

// renameOldShapeEventLogs runs before ApplySchema: any event log table that
// still lacks its flag column is moved aside so the schema files can create
// the new shape.
func renameOldShapeEventLogs(db *sql.DB) {
	for _, m := range eventFlagMigrations {
		var cols, flags int
		err := db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM pragma_table_info(?)),
			(SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?)`,
			m.table, m.table, m.flagCol).Scan(&cols, &flags)
		if err != nil {
			panic(fmt.Sprintf("inspect %s shape: %v", m.table, err))
		}
		if cols == 0 || flags > 0 {
			continue // table absent (fresh db or mid-migration crash) or already new-shape
		}
		for _, idx := range m.indexes {
			if _, err := db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
				panic(fmt.Sprintf("drop index %s: %v", idx, err))
			}
		}
		if _, err := db.Exec(`ALTER TABLE ` + m.table + ` RENAME TO ` + m.table + `_legacy`); err != nil {
			panic(fmt.Sprintf("rename %s: %v", m.table, err))
		}
	}
}

// copyLegacyEventLogs runs after ApplySchema: each *_legacy table left by
// renameOldShapeEventLogs is copied into the new-shape table and dropped, in
// one transaction per table so a crash retries cleanly on the next startup.
func copyLegacyEventLogs(db *sql.DB) {
	for _, m := range eventFlagMigrations {
		var exists int
		legacy := m.table + "_legacy"
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			legacy).Scan(&exists); err != nil {
			panic(fmt.Sprintf("inspect %s: %v", legacy, err))
		}
		if exists == 0 {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			panic(fmt.Sprintf("begin %s copy: %v", m.table, err))
		}
		if _, err := tx.Exec(m.copySQL); err != nil {
			tx.Rollback()
			panic(fmt.Sprintf("copy %s: %v", legacy, err))
		}
		if _, err := tx.Exec(`DROP TABLE ` + legacy); err != nil {
			tx.Rollback()
			panic(fmt.Sprintf("drop %s: %v", legacy, err))
		}
		if err := tx.Commit(); err != nil {
			panic(fmt.Sprintf("commit %s copy: %v", m.table, err))
		}
	}
}
