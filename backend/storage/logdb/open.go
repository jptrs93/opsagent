package logdb

import (
	"context"
	"database/sql"
	"embed"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

//go:embed sql/schema.sql
var schemaFiles embed.FS

//go:embed sql/migrations.sql
var migrations string

func Open(dbPath string) *Queries {
	db := sqlitedb.MustOpen(dbPath)
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return New(db)
}

func (q *Queries) sqlDB() *sql.DB {
	return q.db.(*sql.DB)
}

func (q *Queries) Close() error {
	return q.sqlDB().Close()
}

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
