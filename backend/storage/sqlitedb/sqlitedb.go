// Package sqlitedb holds the SQLite plumbing shared by the role-specific
// storage packages (primarydb, secondarydb): opening a database with the
// standard pragmas, applying embedded schema files, and re-running idempotent
// migrations. It knows nothing about either role's tables.
package sqlitedb

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	_ "modernc.org/sqlite"
)

func MustOpen(dbPath string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	return db
}

// ApplySchema executes every embedded schema file matching glob. Tables must
// have no cross-file dependencies, so the (sorted) glob order is enough.
func ApplySchema(db *sql.DB, schemaFiles embed.FS, glob string) {
	names, err := fs.Glob(schemaFiles, glob)
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

func ApplyMigrations(db *sql.DB, migrations string) {
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
