package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	applyCodeMigrations(db)
	applyMigrations(db, migrations)
	return db
}

func applyCodeMigrations(db *sql.DB) {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS opendeploy_migrations (
			name TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		panic(fmt.Sprintf("create migrations table: %v", err))
	}
	applyOneShotMigration(db, "reset_secrets_for_opendeploy_aad", func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM secrets"); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM secret_keyslots"); err != nil {
			return err
		}
		return nil
	})
	applyOneShotMigration(db, "add_internal_secret_flag", func(tx *sql.Tx) error {
		if _, err := tx.Exec("ALTER TABLE secrets ADD COLUMN internal INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
		return nil
	})
	applyOneShotMigration(db, "reset_secrets_for_internal_aad", func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM secrets"); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM secret_keyslots"); err != nil {
			return err
		}
		return nil
	})
}

func applyOneShotMigration(db *sql.DB, name string, fn func(*sql.Tx) error) {
	tx, err := db.Begin()
	if err != nil {
		panic(fmt.Sprintf("begin migration %s: %v", name, err))
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow("SELECT 1 FROM opendeploy_migrations WHERE name = ?", name).Scan(&exists); err == nil {
		return
	} else if err != sql.ErrNoRows {
		panic(fmt.Sprintf("check migration %s: %v", name, err))
	}

	if err := fn(tx); err != nil {
		panic(fmt.Sprintf("apply migration %s: %v", name, err))
	}
	if _, err := tx.Exec("INSERT INTO opendeploy_migrations (name, applied_at) VALUES (?, ?)", name, time.Now().UnixMilli()); err != nil {
		panic(fmt.Sprintf("record migration %s: %v", name, err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit migration %s: %v", name, err))
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
