package sqlite

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// migrateAssetShape evolves the pre-directories assets table — one row per
// version, grouped only by key — into the assets + asset_versions split.
//
// It must run before applySchema: the schema files CREATE IF NOT EXISTS the
// target tables, which would silently no-op against the old-shape assets table
// and leave the two shapes to collide. It runs under both roles; a secondary's
// assets table is always empty but still needs its shape replaced. The whole
// transform is one transaction, so a crash mid-way re-runs it cleanly on the
// next start.
//
// Version row ids are preserved verbatim into asset_versions.id: deployment
// configs pin them, workers fetch and cache by them, and existing S3 object
// keys were derived from them. The new assets sequence is seeded above
// max(asset_versions.id) so the two id spaces never overlap — an accidental
// join of a version reference against assets then resolves to nothing instead
// of silently resolving to the wrong asset.
func migrateAssetShape(db *sql.DB) {
	if !hasColumn(db, "assets", "blob") {
		return
	}
	slog.Info("migrating assets table to assets/asset_versions split")

	tx, err := db.Begin()
	if err != nil {
		panic(fmt.Sprintf("begin asset shape migration: %v", err))
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE asset_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			asset_id    INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			created_at  INTEGER NOT NULL,
			created_by  INTEGER NOT NULL DEFAULT 0,
			location    TEXT    NOT NULL DEFAULT '',
			size_bytes  INTEGER NOT NULL DEFAULT 0,
			blob        BLOB    NOT NULL,
			UNIQUE (asset_id, version)
		)`,
		`CREATE TABLE assets_new (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			space_id            INTEGER NOT NULL DEFAULT 1,
			key                 TEXT    NOT NULL,
			asset_directory_id  INTEGER NOT NULL DEFAULT 0,
			created_at          INTEGER NOT NULL,
			created_by          INTEGER NOT NULL DEFAULT 0
		)`,
		// One asset per key, with explicit ids starting above the highest
		// preserved version id so the two id spaces never overlap (see doc
		// comment). The old schema stored space_id per version row and a
		// targeted upload could override it, so versions of one key may
		// disagree; the group takes the newest version's space, matching what
		// the latest-version list has always displayed.
		`INSERT INTO assets_new (id, key, space_id, asset_directory_id, created_at, created_by)
		 SELECT (SELECT COALESCE(MAX(id), 0) FROM assets) + ROW_NUMBER() OVER (ORDER BY o.key),
		        o.key,
		        (SELECT x.space_id FROM assets x WHERE x.key = o.key ORDER BY x.version DESC LIMIT 1),
		        0, MIN(o.created_at), 0
		 FROM assets o
		 GROUP BY o.key`,
		// pending:// rows migrate too: upload recovery finds its row by this
		// same preserved id.
		`INSERT INTO asset_versions (id, asset_id, version, created_at, created_by, location, size_bytes, blob)
		 SELECT o.id, n.id, o.version, o.created_at, 0, o.location, o.size_bytes, o.blob
		 FROM assets o
		 JOIN assets_new n ON n.key = o.key`,
		`DROP TABLE assets`,
		`ALTER TABLE assets_new RENAME TO assets`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			panic(fmt.Sprintf("asset shape migration failed: %v\nstmt: %s", err, stmt))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit asset shape migration: %v", err))
	}
}

func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		panic(fmt.Sprintf("inspect %s columns: %v", table, err))
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			panic(fmt.Sprintf("inspect %s columns: %v", table, err))
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("inspect %s columns: %v", table, err))
	}
	return false
}
