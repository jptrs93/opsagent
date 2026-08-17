package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// TestAuthzVersionsSplitMigration builds a pre-split database — rule templates
// still carrying created_at/created_by/data_blob and grants without a deleted
// flag — and checks that opening it backfills one baseline version row per
// template, drops the moved columns, adds the grant deleted column, and loads
// templates with their original attribution and content. The second Open
// proves the migration is a no-op once applied; afterwards a template update
// appends to the log and a grant delete soft-deletes.
func TestAuthzVersionsSplitMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	for _, statement := range []string{
		`CREATE TABLE authz_rule_templates (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL,
			builtin    INTEGER NOT NULL DEFAULT 0,
			deleted    INTEGER NOT NULL DEFAULT 0,
			created_by INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			data_blob  BLOB    NOT NULL
		)`,
		`CREATE UNIQUE INDEX idx_authz_rule_templates_name
			ON authz_rule_templates (name) WHERE deleted = 0`,
		`CREATE TABLE authz_grants (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER NOT NULL,
			template_id INTEGER NOT NULL DEFAULT 0,
			created_by  INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL DEFAULT 0,
			data_blob   BLOB    NOT NULL
		)`,
		`INSERT INTO authz_rule_templates (id, name, builtin, deleted, created_by, created_at, data_blob)
			VALUES (1, 'cluster_admin', 1, 0, 0, 0, x'01'),
			       (5, 'custom', 0, 0, 42, 5000, x'02'),
			       (6, 'gone', 0, 1, 42, 6000, x'03')`,
		`INSERT INTO authz_grants (id, user_id, template_id, created_by, created_at, data_blob)
			VALUES (1, 9, 1, 42, 7000, x'04')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		store := Open(dbPath)
		templates, err := store.ListAuthzRuleTemplates()
		if err != nil {
			t.Fatal(err)
		}
		byID := map[int64]authz.RuleTemplateRow{}
		for _, row := range templates {
			byID[row.ID] = row
		}
		custom := byID[5]
		if custom.Name != "custom" || custom.CreatedBy != 42 || custom.CreatedAt != 5000 ||
			string(custom.Blob) != "\x02" || custom.Deleted {
			t.Fatalf("migrated template 5 = %+v", custom)
		}
		if !byID[6].Deleted {
			t.Fatal("deleted flag lost on template 6")
		}
		grants, err := store.ListAuthzGrants()
		if err != nil {
			t.Fatal(err)
		}
		if len(grants) != 1 || grants[0].CreatedBy != 42 || grants[0].CreatedAt != 7000 {
			t.Fatalf("migrated grants = %+v", grants)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Post-migration writes: an update appends to the log, a grant delete
	// soft-deletes.
	store := Open(dbPath)
	if err := store.UpdateAuthzRuleTemplate(5, "custom2", []byte{0x22}, 43, 8000); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAuthzGrant(1); err != nil {
		t.Fatal(err)
	}
	grants, err := store.ListAuthzGrants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants after delete = %+v", grants)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('authz_rule_templates')
		WHERE name IN ('created_at', 'created_by', 'data_blob')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("authz_rule_templates moved columns were not dropped")
	}
	if err := db.QueryRow(`SELECT deleted FROM authz_grants WHERE id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("grant 1 was not soft-deleted")
	}
	rows, err := db.Query(`SELECT template_id, version, created_at, created_by
		FROM authz_rule_template_versions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4]int64
	for rows.Next() {
		var r [4]int64
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 ||
		got[0] != [4]int64{1, 1, 0, 0} ||
		got[1] != [4]int64{5, 1, 5000, 42} ||
		got[2] != [4]int64{6, 1, 6000, 42} ||
		got[3] != [4]int64{5, 2, 8000, 43} {
		t.Fatalf("authz_rule_template_versions rows = %v, want three backfilled baselines plus the appended update", got)
	}
}
