package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed sql/schema.sql
var schema string

//go:embed sql/primary-migrations/migrations.sql
var primaryMigrations string

func mustInit(dbPath string) *sql.DB {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	applyPrimaryMigrations(db)
	return db
}

func applyPrimaryMigrations(db *sql.DB) {
	for _, stmt := range strings.Split(primaryMigrations, ";") {
		if !hasExecutableSQL(stmt) {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			panic(fmt.Sprintf("primary migration failed: %v\nstmt: %s", err, stmt))
		}
	}
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
