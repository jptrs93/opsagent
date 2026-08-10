package primarydb

import (
	"database/sql"
	"embed"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// The schema is split by table group across sql/schema*.sql. Every matching
// file is applied on startup, so adding a group needs no change here.
//
//go:embed sql/schema*.sql
var schemaFiles embed.FS

//go:embed sql/migrations.sql
var migrations string

func mustInit(dbPath string) *sql.DB {
	db := sqlitedb.MustOpen(dbPath)
	// Shape migrations run before ApplySchema: CREATE TABLE IF NOT EXISTS
	// silently no-ops against an old-shape table of the same name, so the
	// schema files can only ever see the target shape.
	migrateAssetShape(db)
	migrateValueShape(db)
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema*.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return db
}
