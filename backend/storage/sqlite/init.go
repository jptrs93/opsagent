package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed sql/schema.sql
var schema string

//go:embed sql/primary-migrations/migrations.sql
var primaryMigrations string

//go:embed sql/secondary-migrations/migrations.sql
var secondaryMigrations string

func mustInitPrimary(dbPath string) *sql.DB {
	return mustInit(dbPath, primaryMigrations)
}

func mustInitSecondary(dbPath string) *sql.DB {
	return mustInit(dbPath, secondaryMigrations)
}

func mustInit(dbPath, migrations string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	migrateVersionedSecretConfigTables(db)
	applyMigrations(db, migrations)
	return db
}

func migrateVersionedSecretConfigTables(db *sql.DB) {
	if tableHasColumn(db, "user_configs", "config_group") || tableHasColumn(db, "user_configs", "updated_at") || !tableHasColumn(db, "user_configs", "version") {
		rebuildUserConfigsTable(db)
	}
	if tableHasColumn(db, "secrets", "secret_group") || tableHasColumn(db, "secrets", "updated_at") || !tableHasColumn(db, "secrets", "version") {
		rebuildSecretsTable(db)
	}
}

func tableHasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		panic(fmt.Sprintf("table_info %s: %v", table, err))
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			panic(fmt.Sprintf("scan table_info %s: %v", table, err))
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("table_info rows %s: %v", table, err))
	}
	return false
	}

func rebuildUserConfigsTable(db *sql.DB) {
	const stmts = `
ALTER TABLE user_configs RENAME TO user_configs_pre_versioning;
CREATE TABLE user_configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    value        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);
INSERT INTO user_configs (id, name, version, space_id, value, created_at, updated_by)
SELECT id, name, 1, space_id, value, created_at, updated_by
FROM user_configs_pre_versioning;
DROP TABLE user_configs_pre_versioning;`
	if _, err := db.Exec(stmts); err != nil {
		panic(fmt.Sprintf("rebuild user_configs: %v", err))
	}
}

func rebuildSecretsTable(db *sql.DB) {
	const stmts = `
ALTER TABLE secrets RENAME TO secrets_pre_versioning;
CREATE TABLE secrets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    space_id     INTEGER NOT NULL DEFAULT 1,
    smk_version  INTEGER NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_by   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name, version)
);
INSERT INTO secrets (id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by)
SELECT id, name, 1, space_id, smk_version, ciphertext, nonce, created_at, updated_by
FROM secrets_pre_versioning;
DROP TABLE secrets_pre_versioning;`
	if _, err := db.Exec(stmts); err != nil {
		panic(fmt.Sprintf("rebuild secrets: %v", err))
	}
}

func applyMigrations(db *sql.DB, migrations string) {
	for _, stmt := range strings.Split(migrations, ";") {
		if !hasExecutableSQL(stmt) {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			// Migrations re-run on every startup, so statements that already
			// reached their target state must be tolerated. SQLite lacks
			// IF [NOT] EXISTS on ADD/RENAME COLUMN, and a statement that reads a
			// table a prior migration dropped fails at prepare time even when no
			// rows would match. Treat those "already applied" errors as no-ops;
			// anything else is a real failure.
			if isAlreadyAppliedErr(err) {
				slog.Debug("skipping already-applied migration", "err", err, "stmt", strings.TrimSpace(stmt))
				continue
			}
			panic(fmt.Sprintf("migration failed: %v\nstmt: %s", err, stmt))
		}
	}
}

// isAlreadyAppliedErr reports whether a migration error indicates the change is
// already in place: a RENAME of a column that no longer exists under the old
// name, an ADD of a column that already exists, or a reference to a table a
// prior migration already dropped.
func isAlreadyAppliedErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such column") ||
		strings.Contains(msg, "duplicate column name") ||
		strings.Contains(msg, "no such table")
}

func hasExecutableSQL(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return true
	}
	return false
}
