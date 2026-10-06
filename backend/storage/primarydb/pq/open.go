package pq

import (
	"context"
	"database/sql"
	"embed"

	"github.com/jptrs93/opsagent/backend/apigen"
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
	// applied counts, per update, the mutations a transaction has already
	// materialised, so an update grown by a trigger is reduced once.
	applied map[*apigen.CoreWriteUpdate]int
}

type conn struct {
	DBTX
	root *sql.DB
}

func Open(dbPath string) *Queries {
	db := sqlitedb.MustOpenWriter(dbPath)
	refuseLegacyDatabase(db)
	if legacyDataModel(db) {
		backupBeforeConversion(db, dbPath)
		convertDataModel(context.Background(), db)
	}
	sqlitedb.ApplySchema(db, schemaFiles, "sql/schema*.sql")
	sqlitedb.ApplyMigrations(db, migrations)
	markFormatVersion(db)
	seedWriteLogGenesis(db)
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
	transaction := &Queries{db: &conn{DBTX: tx}, applied: map[*apigen.CoreWriteUpdate]int{}}
	if err := fn(transaction); err != nil {
		return err
	}
	return tx.Commit()
}
