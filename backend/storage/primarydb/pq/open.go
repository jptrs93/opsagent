package pq

import (
	"context"
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

// Open opens (creating if needed) the primary database and returns the query
// layer bound to it. All SQL — generated and hand-written — lives on *Queries.
func Open(dbPath string) *Queries {
	db := sqlitedb.MustOpen(dbPath)
	// Go shape migrations (renaming or rewriting an old-shape table) have to
	// run before ApplySchema: CREATE TABLE IF NOT EXISTS silently no-ops
	// against the old shape.
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema*.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return New(db)
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
