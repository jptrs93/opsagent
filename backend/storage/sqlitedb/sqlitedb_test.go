package sqlitedb

import (
	"path/filepath"
	"testing"
)

// Migrations re-run in full on every startup, so the framework's whole job is
// making that safe: real changes apply once, "already applied" errors are
// tolerated on later runs, and comment-only chunks never execute.
func TestApplyMigrationsIsIdempotent(t *testing.T) {
	db := MustOpen(filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (a TEXT); CREATE TABLE retired (b TEXT)`); err != nil {
		t.Fatal(err)
	}

	migrations := `
-- add a column (fails with "duplicate column name" once applied)
ALTER TABLE t ADD COLUMN c TEXT;
-- drop a table (later statements referencing it fail with "no such table")
DROP TABLE retired;
DELETE FROM retired;
-- drop a column (fails with "no such column" once applied)
ALTER TABLE t DROP COLUMN a;
`
	ApplyMigrations(db, migrations)
	ApplyMigrations(db, migrations)

	if _, err := db.Exec(`INSERT INTO t (c) VALUES ('x')`); err != nil {
		t.Fatalf("column c missing after migrations: %v", err)
	}
	if _, err := db.Exec(`SELECT a FROM t`); err == nil {
		t.Fatal("column a still present after migrations")
	}
	if _, err := db.Exec(`SELECT * FROM retired`); err == nil {
		t.Fatal("table retired still present after migrations")
	}
}

func TestApplyMigrationsPanicsOnRealFailure(t *testing.T) {
	db := MustOpen(filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on a syntactically invalid migration")
		}
	}()
	ApplyMigrations(db, `NOT VALID SQL`)
}

func TestApplyMigrationsSkipsCommentOnlyChunks(t *testing.T) {
	db := MustOpen(filepath.Join(t.TempDir(), "test.db"))
	defer db.Close()

	// A comment-only file must be a no-op, not an executed empty statement.
	ApplyMigrations(db, "-- header comment\n\n-- another comment\n")
}
