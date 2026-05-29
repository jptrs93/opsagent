package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
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
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	applyMigrations(db, migrations)
	return db
}

func applyMigrations(db *sql.DB, migrations string) {
	for _, stmt := range strings.Split(migrations, ";") {
		if !hasExecutableSQL(stmt) {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			// ADD COLUMN / RENAME COLUMN have no IF [NOT] EXISTS in SQLite, so
			// they error when already in the target state (e.g. on a freshly
			// created DB that schema.sql already built correctly). Treat those
			// two errors as "already applied" so migrations stay idempotent;
			// anything else is a real failure.
			if isAlreadyAppliedErr(err) {
				slog.Debug("skipping already-applied migration", "err", err, "stmt", strings.TrimSpace(stmt))
				continue
			}
			panic(fmt.Sprintf("migration failed: %v\nstmt: %s", err, stmt))
		}
	}
}

// isAlreadyAppliedErr reports whether a migration error indicates the change
// is already in place: a RENAME of a column that no longer exists under the
// old name, or an ADD of a column that already exists.
func isAlreadyAppliedErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such column") ||
		strings.Contains(msg, "duplicate column name")
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
