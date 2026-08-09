package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	_ "modernc.org/sqlite"
)

// The schema is split by table group across sql/schema*.sql. Every matching
// file is applied on startup, so adding a group needs no change here.
//
//go:embed sql/schema*.sql
var schemaFiles embed.FS

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
	// Shape migrations run before applySchema: CREATE TABLE IF NOT EXISTS
	// silently no-ops against an old-shape table of the same name, so the
	// schema files can only ever see the target shape.
	migrateAssetShape(db)
	applySchema(db)
	applyMigrations(db, migrations)
	return db
}

// applySchema executes every embedded schema file. The tables have no
// cross-file dependencies, so the (sorted) glob order is enough.
func applySchema(db *sql.DB) {
	names, err := fs.Glob(schemaFiles, "sql/schema*.sql")
	if err != nil {
		panic(fmt.Sprintf("glob schema files: %v", err))
	}
	for _, name := range names {
		stmts, err := fs.ReadFile(schemaFiles, name)
		if err != nil {
			panic(fmt.Sprintf("read %s: %v", name, err))
		}
		if _, err := db.Exec(string(stmts)); err != nil {
			panic(fmt.Sprintf("exec %s: %v", name, err))
		}
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
