package pq

import (
	"context"
	"database/sql"
	"embed"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

//go:embed sql/schema*.sql
var schemaFiles embed.FS

//go:embed sql/migrations.sql
var migrations string

type DBTX interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

type Queries struct {
	db DBTX
}

type conn struct {
	DBTX
	root *sql.DB
}

func Open(dbPath string) *Queries {
	db := sqlitedb.MustOpenWriter(dbPath)
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema*.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	return &Queries{db: &conn{DBTX: db, root: db}}
}

func (q *Queries) conn() *conn {
	return q.db.(*conn)
}

func (q *Queries) sqlDB() *sql.DB {
	return q.conn().root
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
	transaction := &Queries{db: &conn{DBTX: tx}}
	if err := fn(transaction); err != nil {
		return err
	}
	return tx.Commit()
}
