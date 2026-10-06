package sq

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestOpenStampsAFreshDatabaseAndReopensIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.db")
	Open(path).Close()
	db := sqlitedb.MustOpen(path)
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM local_kv WHERE key = ?`, formatVersionKey).Scan(&raw); err != nil || string(raw) != "2" {
		t.Fatalf("format_version = %q, %v", raw, err)
	}
	db.Close()
	Open(path).Close()
}

func TestOpenRefusesADatabaseWithoutTheDataModelFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.db")
	legacy := sqlitedb.MustOpen(path)
	if _, err := legacy.Exec(`CREATE TABLE local_kv (key TEXT PRIMARY KEY, value BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	defer func() {
		r := recover()
		if msg, _ := r.(string); r == nil || !strings.Contains(msg, "v0.0.616") {
			t.Fatalf("Open accepted a database without a format version: %v", r)
		}
	}()
	Open(path).Close()
}
