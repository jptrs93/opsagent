package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// TestEventFlagsMigration seeds the pre-flag shape of the four event logs —
// nullable payload columns marking value writes, no *_changed columns — and
// checks the startup rebuild: flags recomputed from the sub-version deltas,
// payloads carried forward and NOT NULL, row ids preserved, legacy tables
// dropped, and a second open a no-op.
func TestEventFlagsMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE deployment_event_log (
			id                       INTEGER PRIMARY KEY AUTOINCREMENT,
			global_seq               INTEGER NOT NULL,
			event_time               INTEGER NOT NULL,
			created_time             INTEGER NOT NULL,
			author                   INTEGER NOT NULL,
			deployment_id            INTEGER NOT NULL,
			version                  INTEGER NOT NULL,
			spec_version             INTEGER NOT NULL,
			space_assignment_version INTEGER NOT NULL,
			name_version             INTEGER NOT NULL,
			value                    BLOB NOT NULL,
			event_type               INTEGER NOT NULL DEFAULT 0,
			UNIQUE (deployment_id, version)
		);
		CREATE INDEX idx_deployment_event_log_spec_version
			ON deployment_event_log (deployment_id, spec_version);
		CREATE TABLE asset_event_log (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			global_seq         INTEGER NOT NULL,
			event_time         INTEGER NOT NULL,
			created_time       INTEGER NOT NULL,
			author             INTEGER NOT NULL,
			asset_id           INTEGER NOT NULL,
			version            INTEGER NOT NULL,
			value_version      INTEGER NOT NULL,
			space_version      INTEGER NOT NULL,
			key                TEXT    NOT NULL,
			asset_directory_id INTEGER NOT NULL,
			space_id           INTEGER NOT NULL,
			size_bytes         INTEGER,
			sha256             TEXT,
			event_type         INTEGER NOT NULL,
			UNIQUE (asset_id, version)
		);
		CREATE INDEX idx_asset_event_log_sha256
			ON asset_event_log (sha256) WHERE sha256 IS NOT NULL;
		CREATE TABLE secret_event_log (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			global_seq         INTEGER NOT NULL,
			event_time         INTEGER NOT NULL,
			created_time       INTEGER NOT NULL,
			author             INTEGER NOT NULL,
			secret_id          INTEGER NOT NULL,
			version            INTEGER NOT NULL,
			value_version      INTEGER NOT NULL,
			space_version      INTEGER NOT NULL,
			name               TEXT    NOT NULL,
			value_directory_id INTEGER NOT NULL,
			space_id           INTEGER NOT NULL,
			smk_version        INTEGER,
			ciphertext         BLOB,
			nonce              BLOB,
			event_type         INTEGER NOT NULL,
			UNIQUE (secret_id, version)
		);
		CREATE TABLE asset_store (
			id            TEXT    PRIMARY KEY,
			sha256        TEXT    NOT NULL DEFAULT '',
			size_bytes    INTEGER NOT NULL DEFAULT 0,
			inline_blob   BLOB    NOT NULL DEFAULT x'',
			local_status  INTEGER NOT NULL DEFAULT 0,
			remote_status INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL
		);
		CREATE TABLE config_event_log (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			global_seq         INTEGER NOT NULL,
			event_time         INTEGER NOT NULL,
			created_time       INTEGER NOT NULL,
			author             INTEGER NOT NULL,
			config_id          INTEGER NOT NULL,
			version            INTEGER NOT NULL,
			value_version      INTEGER NOT NULL,
			space_version      INTEGER NOT NULL,
			name               TEXT    NOT NULL,
			value_directory_id INTEGER NOT NULL,
			space_id           INTEGER NOT NULL,
			value              TEXT,
			event_type         INTEGER NOT NULL,
			UNIQUE (config_id, version)
		);
	`); err != nil {
		t.Fatal(err)
	}

	def := apigen.DeploymentDef{NodeID: 3, SpaceID: 1, Name: "api", Spec: *testSpecWithState("v1", true)}
	blob := def.Encode()
	// dep 7: create, spec bump, name bump, delete.
	mustExecSQL(t, db, `INSERT INTO deployment_event_log
		(id, global_seq, event_time, created_time, author, deployment_id, version,
		 spec_version, space_assignment_version, name_version, value, event_type) VALUES
		(1, 100, 1000, 1000, 5, 7, 1, 1, 1, 1, ?, 1),
		(2, 101, 2000, 1000, 5, 7, 2, 2, 1, 1, ?, 2),
		(3, 102, 3000, 1000, 5, 7, 3, 2, 1, 2, ?, 2),
		(4, 103, 4000, 1000, 5, 7, 4, 2, 1, 2, ?, 3)`, blob, blob, blob, blob)
	// asset 3: create with content, rename (NULL payload), content write, space move (NULL payload).
	mustExecSQL(t, db, `INSERT INTO asset_event_log
		(id, global_seq, event_time, created_time, author, asset_id, version,
		 value_version, space_version, key, asset_directory_id, space_id, size_bytes, sha256, event_type) VALUES
		(1, 110, 1000, 1000, 5, 3, 1, 1, 1, 'a.txt', 0, 1, 10, 'aa', 1),
		(2, 111, 2000, 1000, 5, 3, 2, 1, 1, 'b.txt', 0, 1, NULL, NULL, 2),
		(3, 112, 3000, 1000, 5, 3, 3, 2, 1, 'b.txt', 0, 1, 20, 'bb', 2),
		(4, 113, 4000, 1000, 5, 3, 4, 2, 2, 'b.txt', 0, 2, NULL, NULL, 2)`)
	mustExecSQL(t, db, `INSERT INTO asset_store (id, sha256, size_bytes, created_at) VALUES
		('s1', 'aa', 10, 900), ('s2', 'bb', 20, 900)`)
	// secret 4: create with value, rename (NULL payload), value write.
	mustExecSQL(t, db, `INSERT INTO secret_event_log
		(id, global_seq, event_time, created_time, author, secret_id, version,
		 value_version, space_version, name, value_directory_id, space_id,
		 smk_version, ciphertext, nonce, event_type) VALUES
		(1, 120, 1000, 1000, 5, 4, 1, 1, 1, 'tok', 0, 1, 1, X'AA', X'01', 1),
		(2, 121, 2000, 1000, 5, 4, 2, 1, 1, 'token', 0, 1, NULL, NULL, NULL, 2),
		(3, 122, 3000, 1000, 5, 4, 3, 2, 1, 'token', 0, 1, 1, X'BB', X'02', 2)`)
	// config 5: create with value, space move (NULL payload), value write.
	mustExecSQL(t, db, `INSERT INTO config_event_log
		(id, global_seq, event_time, created_time, author, config_id, version,
		 value_version, space_version, name, value_directory_id, space_id, value, event_type) VALUES
		(1, 130, 1000, 1000, 5, 5, 1, 1, 1, 'cfg', 0, 1, 'one', 1),
		(2, 131, 2000, 1000, 5, 5, 2, 1, 2, 'cfg', 0, 2, NULL, 2),
		(3, 132, 3000, 1000, 5, 5, 3, 2, 2, 'cfg', 0, 2, 'two', 2)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)

	if cfg := store.FetchDeployment(7); cfg == nil || !cfg.Deleted() || cfg.SpecVersion != 2 || cfg.NameVersion != 2 {
		t.Fatalf("deployment 7 after migration = %+v", cfg)
	}
	c, ok := store.GetConfig(5)
	if !ok || len(c.ValueVersions) != 2 || len(c.SpaceVersions) != 2 {
		t.Fatalf("config 5 = %+v", c)
	}
	if c.ValueVersions[0].Value != "two" || c.ValueVersions[1].Value != "one" {
		t.Fatalf("config values = %q/%q", c.ValueVersions[0].Value, c.ValueVersions[1].Value)
	}
	sec, ok := store.GetSecret(4)
	if !ok || len(sec.Versions) != 2 || len(sec.SpaceVersions) != 1 {
		t.Fatalf("secret 4 = %+v", sec)
	}
	records := store.ListSecretVersionRecords()
	if len(records) != 2 || string(records[0].Ciphertext) != "\xaa" || string(records[1].Ciphertext) != "\xbb" {
		t.Fatalf("secret records = %+v", records)
	}
	asset, ok := store.GetAsset(3)
	if !ok || len(asset.ContentVersions) != 2 || len(asset.SpaceVersions) != 2 {
		t.Fatalf("asset 3 = %+v", asset)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Second open must be a no-op rebuild-wise.
	store = Open(dbPath)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db = sqlitedb.MustOpen(dbPath)
	defer db.Close()
	for _, table := range []string{"deployment_event_log_legacy", "asset_event_log_legacy",
		"secret_event_log_legacy", "config_event_log_legacy"} {
		if _, err := db.Exec(`SELECT * FROM ` + table); err == nil {
			t.Fatalf("legacy table %s still present after migration", table)
		}
	}
	for query, want := range map[string]int64{
		// Flags: one per event that bumped the matching sub-version, create rows flag everything.
		`SELECT COUNT(*) FROM deployment_event_log WHERE deployment_id = 7 AND spec_changed != 0`:             2,
		`SELECT COUNT(*) FROM deployment_event_log WHERE deployment_id = 7 AND name_changed != 0`:             2,
		`SELECT COUNT(*) FROM deployment_event_log WHERE deployment_id = 7 AND space_assignment_changed != 0`: 1,
		`SELECT COUNT(*) FROM deployment_event_log WHERE id = 1 AND spec_changed != 0 AND name_changed != 0`:  1,
		`SELECT COUNT(*) FROM deployment_event_log WHERE id = 4 AND spec_changed = 0 AND name_changed = 0`:    1,
		`SELECT COUNT(*) FROM asset_event_log WHERE asset_id = 3 AND value_changed != 0`:                      2,
		`SELECT COUNT(*) FROM asset_event_log WHERE asset_id = 3 AND space_changed != 0`:                      2,
		// Payloads carried forward onto the non-writing rows.
		`SELECT COUNT(*) FROM asset_event_log WHERE id = 2 AND sha256 = 'aa' AND size_bytes = 10`:  1,
		`SELECT COUNT(*) FROM asset_event_log WHERE id = 4 AND sha256 = 'bb' AND size_bytes = 20`:  1,
		`SELECT COUNT(*) FROM secret_event_log WHERE id = 2 AND ciphertext = X'AA' AND nonce = X'01' AND smk_version = 1`: 1,
		`SELECT COUNT(*) FROM config_event_log WHERE id = 2 AND value = 'one'`:                     1,
		`SELECT COUNT(*) FROM config_event_log WHERE value IS NULL`:                                0,
		`SELECT COUNT(*) FROM asset_event_log WHERE sha256 IS NULL OR size_bytes IS NULL`:          0,
		`SELECT COUNT(*) FROM secret_event_log WHERE ciphertext IS NULL`:                           0,
	} {
		var got int64
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
}

func mustExecSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
