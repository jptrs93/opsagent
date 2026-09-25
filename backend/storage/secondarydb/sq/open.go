package sq

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

//go:embed sql/schema.sql
var schemaFiles embed.FS

//go:embed sql/migrations.sql
var migrations string

// Open opens (creating if needed) the secondary-local database and returns the
// query layer bound to it. All SQL — generated and hand-written — lives on
// *Queries.
func Open(dbPath string) *Queries {
	db := sqlitedb.MustOpen(dbPath)
	dropRowIDRuntimeInputs(db)
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return New(db)
}

// dropRowIDRuntimeInputs drops a local_runtime_inputs table keyed by the
// pre-v0.0.613 value row ids. It holds only refetchable cache rows.
func dropRowIDRuntimeInputs(db *sql.DB) {
	var paired int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('local_runtime_inputs') WHERE name = 'ref_version'`).Scan(&paired); err != nil {
		panic(fmt.Sprintf("inspecting local_runtime_inputs: %v", err))
	}
	if paired != 0 {
		return
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS local_runtime_inputs`); err != nil {
		panic(fmt.Sprintf("dropping local_runtime_inputs: %v", err))
	}
}

// sqlDB returns the underlying connection. Only valid on a Queries created by
// Open (not one bound to a transaction), which is the only place Tx starts.
func (q *Queries) sqlDB() *sql.DB {
	return q.db.(*sql.DB)
}

func (q *Queries) Close() error {
	return q.sqlDB().Close()
}

// Tx runs fn inside a transaction; the *Queries passed to fn is bound to that
// transaction, so both generated and custom methods called on it participate.
// A nil error commits, anything else rolls back.
func (q *Queries) Tx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := q.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}
