package state

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestAuthzEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE authz_rule_templates (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			name    TEXT    NOT NULL,
			builtin INTEGER NOT NULL DEFAULT 0,
			deleted_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE authz_rule_template_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			template_id INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			created_at  INTEGER NOT NULL,
			author  INTEGER NOT NULL DEFAULT 0,
			data_blob   BLOB    NOT NULL,
			global_seq  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (template_id, version)
		);
		CREATE TABLE authz_grants (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id     INTEGER NOT NULL,
			template_id INTEGER NOT NULL DEFAULT 0,
			deleted_at  INTEGER NOT NULL DEFAULT 0,
			author  INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL DEFAULT 0,
			data_blob   BLOB    NOT NULL
		);
		CREATE TABLE global_access_rules (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL DEFAULT '',
			deleted_at INTEGER NOT NULL DEFAULT 0,
			author INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			data_blob  BLOB    NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, db, `INSERT INTO authz_rule_templates (id, name, builtin, deleted_at) VALUES
		(10, 'custom', 0, 0), (11, 'gone', 0, 5000)`)
	mustExecSQL(t, db, `INSERT INTO authz_rule_template_versions (template_id, version, created_at, author, data_blob, global_seq) VALUES
		(10, 1, 1000, 5, X'01', 100), (10, 2, 2000, 6, X'02', 101), (11, 1, 1100, 5, X'03', 102)`)
	mustExecSQL(t, db, `INSERT INTO authz_grants (id, user_id, template_id, deleted_at, author, created_at, data_blob) VALUES
		(1, 7, 10, 0, 5, 1500, X'04'), (2, 8, 10, 6000, 5, 1600, X'05')`)
	mustExecSQL(t, db, `INSERT INTO global_access_rules (id, name, deleted_at, author, created_at, data_blob) VALUES
		(1, 'r1', 0, 5, 1700, X'06'), (2, 'r2', 7000, 5, 1800, X'07')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	templates, err := store.ListAuthzRuleTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 {
		t.Fatalf("templates = %+v, want 2", templates)
	}
	custom, gone := templates[0], templates[1]
	if custom.ID != 10 || custom.Name != "custom" || custom.Deleted || custom.Author != 6 ||
		custom.CreatedAt != 1000 || !bytes.Equal(custom.Blob, []byte{0x02}) {
		t.Fatalf("custom template = %+v", custom)
	}
	if gone.ID != 11 || !gone.Deleted || gone.CreatedAt != 1100 {
		t.Fatalf("deleted template = %+v", gone)
	}
	grants, err := store.ListAuthzGrants()
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].ID != 1 || grants[0].UserID != 7 || grants[0].TemplateID != 10 ||
		grants[0].Author != 5 || grants[0].CreatedAt != 1500 || !bytes.Equal(grants[0].Blob, []byte{0x04}) {
		t.Fatalf("grants = %+v", grants)
	}
	rules, err := store.ListAuthzGlobalRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ID != 1 || rules[0].Name != "r1" || rules[0].CreatedAt != 1700 {
		t.Fatalf("rules = %+v", rules)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = Open(dbPath)
	defer store.Close()
	db = sqlitedb.MustOpen(dbPath)
	defer db.Close()
	for _, table := range []string{"authz_rule_templates", "authz_rule_template_versions", "authz_grants", "global_access_rules"} {
		if _, err := db.Exec(`SELECT * FROM ` + table); err == nil {
			t.Fatalf("legacy table %s still present after migration", table)
		}
	}
	for query, want := range map[string]int64{
		`SELECT COUNT(*) FROM authz_rule_template_event_log`:                                                              4,
		`SELECT COUNT(*) FROM authz_rule_template_event_log WHERE template_id = 11 AND version = 2 AND event_type = 3 AND event_time = 5000`: 1,
		`SELECT COUNT(*) FROM authz_grant_event_log`:                                                                      3,
		`SELECT COUNT(*) FROM authz_grant_event_log WHERE grant_id = 2 AND version = 2 AND event_type = 3 AND event_time = 6000`:             1,
		`SELECT COUNT(*) FROM global_access_rule_event_log`:                                                               3,
		`SELECT COUNT(*) FROM global_access_rule_event_log WHERE rule_id = 2 AND version = 2 AND event_type = 3 AND event_time = 7000`:       1,
		`SELECT COUNT(*) FROM global_access_rule_event_log WHERE disabled != 0`:                                           0,
	} {
		var got int64
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	var globalSeq int64
	if err := db.QueryRow(`SELECT global_seq FROM authz_rule_template_event_log WHERE template_id = 10 AND version = 2`).Scan(&globalSeq); err != nil {
		t.Fatal(err)
	}
	if globalSeq != 101 {
		t.Fatalf("template version global_seq = %d, want 101", globalSeq)
	}
}

func mustExecSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
