package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildOldShapeValueDB creates a database with the pre-directories secrets and
// configs tables: one row per version, grouped only by name.
func buildOldShapeValueDB(t *testing.T, path string, withCollision bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE secrets (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT    NOT NULL,
			version      INTEGER NOT NULL DEFAULT 1,
			space_id     INTEGER NOT NULL DEFAULT 1,
			smk_version  INTEGER NOT NULL,
			ciphertext   BLOB    NOT NULL,
			nonce        BLOB    NOT NULL,
			created_at   INTEGER NOT NULL,
			updated_by   INTEGER NOT NULL DEFAULT 0,
			UNIQUE (name, version)
		)`,
		`CREATE TABLE configs (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT    NOT NULL,
			version      INTEGER NOT NULL DEFAULT 1,
			space_id     INTEGER NOT NULL DEFAULT 1,
			value        TEXT    NOT NULL DEFAULT '',
			created_at   INTEGER NOT NULL,
			updated_by   INTEGER NOT NULL DEFAULT 0,
			UNIQUE (name, version)
		)`,
		// db.password: two versions whose space diverged; the identity must
		// take the newest version's space (2) and the first version's author.
		`INSERT INTO secrets (id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by)
		 VALUES (1, 'db.password', 1, 1, 1, x'aa01', x'bb01', 100, 7)`,
		`INSERT INTO secrets (id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by)
		 VALUES (4, 'db.password', 2, 2, 1, x'aa02', x'bb02', 400, 8)`,
		`INSERT INTO secrets (id, name, version, space_id, smk_version, ciphertext, nonce, created_at, updated_by)
		 VALUES (2, 'api.token', 1, 1, 1, x'aa03', x'bb03', 200, 7)`,
		`INSERT INTO configs (id, name, version, space_id, value, created_at, updated_by)
		 VALUES (1, 'discord.client_id', 1, 1, 'abc', 150, 7)`,
		`INSERT INTO configs (id, name, version, space_id, value, created_at, updated_by)
		 VALUES (2, 'discord.client_id', 2, 1, 'def', 250, 9)`,
		`INSERT INTO configs (id, name, version, space_id, value, created_at, updated_by)
		 VALUES (5, 'stripe.price_id', 1, 1, 'price_1', 500, 7)`,
	}
	if withCollision {
		stmts = append(stmts,
			`INSERT INTO configs (id, name, version, space_id, value, created_at, updated_by)
			 VALUES (6, 'api.token', 1, 1, 'not-a-secret', 600, 7)`)
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build old shape: %v\n%s", err, stmt)
		}
	}
}

func TestValueShapeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	buildOldShapeValueDB(t, path, false)

	store := NewPrimaryStorage(path)

	// Version row ids are preserved verbatim: deployment env refs, system
	// settings, and secondary local_runtime_inputs pin them. Ciphertext bytes
	// are untouched (still sealed under the legacy name AAD until the Manager
	// sweep).
	type secretVersionRow struct {
		id, secretID, version, smkVersion, createdBy int64
		ciphertext                                   []byte
		createdAt                                    int64
	}
	querySecretVersion := func(id int64) secretVersionRow {
		var r secretVersionRow
		err := store.db.QueryRow(
			`SELECT id, secret_id, version, smk_version, ciphertext, created_at, created_by FROM secret_versions WHERE id = ?`, id).
			Scan(&r.id, &r.secretID, &r.version, &r.smkVersion, &r.ciphertext, &r.createdAt, &r.createdBy)
		if err != nil {
			t.Fatalf("secret version %d: %v", id, err)
		}
		return r
	}
	v1 := querySecretVersion(1)
	v4 := querySecretVersion(4)
	v2 := querySecretVersion(2)
	if v1.version != 1 || string(v1.ciphertext) != "\xaa\x01" || v1.createdAt != 100 || v1.createdBy != 7 {
		t.Fatalf("secret version 1 = %+v", v1)
	}
	if v4.version != 2 || v4.secretID != v1.secretID || string(v4.ciphertext) != "\xaa\x02" || v4.createdBy != 8 {
		t.Fatalf("secret version 4 = %+v", v4)
	}
	if v2.secretID == v1.secretID {
		t.Fatal("api.token grouped with db.password")
	}

	// Identity rows: one per name, ids above every preserved version id, space
	// from the newest version, created_at from the oldest, author of v1.
	var name string
	var spaceID, dirID, createdAt, createdBy int64
	err := store.db.QueryRow(
		`SELECT name, space_id, value_directory_id, created_at, created_by FROM secrets WHERE id = ?`, v1.secretID).
		Scan(&name, &spaceID, &dirID, &createdAt, &createdBy)
	if err != nil {
		t.Fatal(err)
	}
	if name != "db.password" || spaceID != 2 || dirID != 0 || createdAt != 100 || createdBy != 7 {
		t.Fatalf("db.password identity = %s space %d dir %d created %d by %d", name, spaceID, dirID, createdAt, createdBy)
	}
	var minSecretID int64
	if err := store.db.QueryRow(`SELECT MIN(id) FROM secrets`).Scan(&minSecretID); err != nil {
		t.Fatal(err)
	}
	if minSecretID <= 4 {
		t.Fatalf("secret identity id %d overlaps preserved version ids", minSecretID)
	}

	// Configs mirror the same split.
	var configID, version int64
	var value string
	if err := store.db.QueryRow(`SELECT config_id, version, value FROM config_versions WHERE id = 2`).Scan(&configID, &version, &value); err != nil {
		t.Fatal(err)
	}
	if version != 2 || value != "def" {
		t.Fatalf("config version 2 = v%d %q", version, value)
	}
	var minConfigID int64
	if err := store.db.QueryRow(`SELECT MIN(id) FROM configs`).Scan(&minConfigID); err != nil {
		t.Fatal(err)
	}
	if minConfigID <= 5 {
		t.Fatalf("config identity id %d overlaps preserved version ids", minConfigID)
	}
	var configCount int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM configs`).Scan(&configCount); err != nil {
		t.Fatal(err)
	}
	if configCount != 2 {
		t.Fatalf("config identities = %d, want 2", configCount)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Idempotence: a second startup sees the new shape and changes nothing.
	store = NewPrimaryStorage(path)
	defer store.Close()
	var versionCount int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM secret_versions`).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 3 {
		t.Fatalf("second startup secret_versions = %d, want 3", versionCount)
	}
}

func TestValueShapeMigrationRejectsSecretConfigCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	buildOldShapeValueDB(t, path, true)

	defer func() {
		if recover() == nil {
			t.Fatal("migration accepted a secret/config name collision")
		}
	}()
	store := NewPrimaryStorage(path)
	store.Close()
}

func TestValueShapeMigrationOnSecondary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.db")
	buildOldShapeValueDB(t, path, false)

	store := NewSecondaryStorage(path)
	defer store.Close()

	for _, check := range []struct {
		table  string
		column string
	}{
		{"secrets", "ciphertext"},
		{"configs", "value"},
	} {
		var has int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, check.table, check.column).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != 0 {
			t.Fatalf("%s table still has old shape on secondary", check.table)
		}
	}
}

func TestFreshInstallHasTargetValueShape(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	for _, table := range []string{"secrets", "secret_versions", "configs", "config_versions", "value_directories"} {
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%s has %d rows on fresh install", table, n)
		}
	}
}
