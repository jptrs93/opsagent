package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed sql/schema.sql
var schema string

// mustInitPrimary and mustInitSecondary exist only to hang the one-shot
// scheduled instance migration off the right side. Both run it before any
// caller can build a store: newDeploymentStore snapshots the tables into memory
// on construction, so anything written afterwards would be invisible until the
// next restart.
func mustInitPrimary(dbPath string) *sql.DB {
	db := mustInit(dbPath)
	mustMigrateScheduledInstancesPrimary(db)
	return db
}

func mustInitSecondary(dbPath string) *sql.DB {
	db := mustInit(dbPath)
	mustMigrateScheduledInstancesSecondary(db)
	return db
}

func mustInit(dbPath string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		panic(fmt.Sprintf("open sqlite: %v", err))
	}
	if _, err := db.Exec(schema); err != nil {
		panic(fmt.Sprintf("exec schema: %v", err))
	}
	return db
}
