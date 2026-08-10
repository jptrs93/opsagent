package primarydb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// migrateValueShape evolves the pre-directories secrets and configs tables —
// one row per version, grouped only by name — into the identity + versions
// split (secrets/secret_versions, configs/config_versions).
//
// Like migrateAssetShape it must run before applySchema, runs under both roles
// (a secondary's tables are always empty but still need their shape replaced),
// and each table's transform is one transaction so a crash mid-way re-runs
// cleanly on the next start.
//
// Version row ids are preserved verbatim: deployment env refs and system
// settings pin them, and a secondary's local_runtime_inputs rows are keyed by
// them. The new identity sequences are seeded above max(version id) so the two
// id spaces never overlap on a migrated install.
//
// Ciphertext bytes are copied untouched — the SMK is not available here, so
// migrated secret versions stay sealed under the legacy name-bound AAD until
// the secrets Manager's re-seal sweep converts them at first unlock.
//
// Secrets and configs share one file system per space after the split, so a
// name held by both a secret and a config in the same space would violate the
// sibling-uniqueness law on day one. That cannot be produced by this codebase
// going forward (the namespace mutex checks all tables), but a legacy database
// could contain it; the migration fails loudly rather than importing the
// violation.
func migrateValueShape(db *sql.DB) {
	migratedSecrets := hasColumn(db, "secrets", "ciphertext")
	migratedConfigs := hasColumn(db, "configs", "value")
	if migratedSecrets {
		migrateSecretShape(db)
	}
	if migratedConfigs {
		migrateConfigShape(db)
	}
	if migratedSecrets || migratedConfigs {
		assertNoSecretConfigNameCollisions(db)
	}
}

func migrateSecretShape(db *sql.DB) {
	slog.Info("migrating secrets table to secrets/secret_versions split")
	runShapeMigration(db, "secret shape migration", []string{
		`CREATE TABLE secret_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			secret_id   INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			smk_version INTEGER NOT NULL,
			ciphertext  BLOB    NOT NULL,
			nonce       BLOB    NOT NULL,
			created_at  INTEGER NOT NULL,
			created_by  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (secret_id, version)
		)`,
		`CREATE TABLE secrets_new (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT    NOT NULL,
			space_id           INTEGER NOT NULL DEFAULT 1,
			value_directory_id INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL,
			created_by         INTEGER NOT NULL DEFAULT 0
		)`,
		// One identity per name, with explicit ids starting above the highest
		// preserved version id so the two id spaces never overlap. The old
		// schema stored space_id per version row, so versions of one name may
		// disagree; the identity takes the newest version's space, matching
		// what the latest-version list has always displayed. Identity
		// created_by is the first version's author.
		`INSERT INTO secrets_new (id, name, space_id, value_directory_id, created_at, created_by)
		 SELECT (SELECT COALESCE(MAX(id), 0) FROM secrets) + ROW_NUMBER() OVER (ORDER BY o.name),
		        o.name,
		        (SELECT x.space_id FROM secrets x WHERE x.name = o.name ORDER BY x.version DESC LIMIT 1),
		        0, MIN(o.created_at),
		        (SELECT x.updated_by FROM secrets x WHERE x.name = o.name ORDER BY x.version ASC LIMIT 1)
		 FROM secrets o
		 GROUP BY o.name`,
		`INSERT INTO secret_versions (id, secret_id, version, smk_version, ciphertext, nonce, created_at, created_by)
		 SELECT o.id, n.id, o.version, o.smk_version, o.ciphertext, o.nonce, o.created_at, o.updated_by
		 FROM secrets o
		 JOIN secrets_new n ON n.name = o.name`,
		`DROP TABLE secrets`,
		`ALTER TABLE secrets_new RENAME TO secrets`,
	})
}

func migrateConfigShape(db *sql.DB) {
	slog.Info("migrating configs table to configs/config_versions split")
	runShapeMigration(db, "config shape migration", []string{
		`CREATE TABLE config_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			config_id   INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			value       TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			created_by  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (config_id, version)
		)`,
		`CREATE TABLE configs_new (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT    NOT NULL,
			space_id           INTEGER NOT NULL DEFAULT 1,
			value_directory_id INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL,
			created_by         INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO configs_new (id, name, space_id, value_directory_id, created_at, created_by)
		 SELECT (SELECT COALESCE(MAX(id), 0) FROM configs) + ROW_NUMBER() OVER (ORDER BY o.name),
		        o.name,
		        (SELECT x.space_id FROM configs x WHERE x.name = o.name ORDER BY x.version DESC LIMIT 1),
		        0, MIN(o.created_at),
		        (SELECT x.updated_by FROM configs x WHERE x.name = o.name ORDER BY x.version ASC LIMIT 1)
		 FROM configs o
		 GROUP BY o.name`,
		`INSERT INTO config_versions (id, config_id, version, value, created_at, created_by)
		 SELECT o.id, n.id, o.version, o.value, o.created_at, o.updated_by
		 FROM configs o
		 JOIN configs_new n ON n.name = o.name`,
		`DROP TABLE configs`,
		`ALTER TABLE configs_new RENAME TO configs`,
	})
}

func runShapeMigration(db *sql.DB, what string, stmts []string) {
	tx, err := db.Begin()
	if err != nil {
		panic(fmt.Sprintf("begin %s: %v", what, err))
	}
	defer tx.Rollback()
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			panic(fmt.Sprintf("%s failed: %v\nstmt: %s", what, err, stmt))
		}
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit %s: %v", what, err))
	}
}

func assertNoSecretConfigNameCollisions(db *sql.DB) {
	rows, err := db.Query(`
SELECT s.name, s.space_id
FROM secrets s
JOIN configs c ON c.name = s.name
             AND c.space_id = s.space_id
             AND c.value_directory_id = s.value_directory_id`)
	if err != nil {
		panic(fmt.Sprintf("check secret/config name collisions: %v", err))
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var name string
		var spaceID int64
		if err := rows.Scan(&name, &spaceID); err != nil {
			panic(fmt.Sprintf("check secret/config name collisions: %v", err))
		}
		collisions = append(collisions, fmt.Sprintf("%q in space %d", name, spaceID))
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("check secret/config name collisions: %v", err))
	}
	if len(collisions) > 0 {
		panic(fmt.Sprintf(
			"value shape migration: secrets and configs now share one file system, but these names exist as both: %s — rename one of each pair before upgrading",
			strings.Join(collisions, ", ")))
	}
}
