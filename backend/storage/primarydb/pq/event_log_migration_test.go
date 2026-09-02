package pq

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func seedLegacyValueTables(t *testing.T, dbPath string, seed func(db *sql.DB)) {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE secrets (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT    NOT NULL,
			value_directory_id INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL,
			deleted_at         INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE secret_spaces (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			secret_id   INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			space_id    INTEGER NOT NULL,
			global_seq  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE secret_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			secret_id   INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			smk_version INTEGER NOT NULL,
			ciphertext  BLOB    NOT NULL,
			nonce       BLOB    NOT NULL,
			created_at  INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			global_seq  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (secret_id, version)
		);
		CREATE TABLE configs (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT    NOT NULL,
			value_directory_id INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL,
			deleted_at         INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE config_spaces (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			config_id   INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			space_id    INTEGER NOT NULL,
			global_seq  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE config_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			config_id   INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			value       TEXT    NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			global_seq  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (config_id, version)
		);
		CREATE TABLE assets (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			key                 TEXT    NOT NULL,
			asset_directory_id  INTEGER NOT NULL DEFAULT 0,
			created_at          INTEGER NOT NULL,
			deleted_at          INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE asset_spaces (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			asset_id    INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			space_id    INTEGER NOT NULL,
			global_seq  INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE asset_versions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			asset_id    INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			created_at  INTEGER NOT NULL,
			author      INTEGER NOT NULL DEFAULT 0,
			size_bytes  INTEGER NOT NULL DEFAULT 0,
			sha256      TEXT    NOT NULL DEFAULT '',
			global_seq  INTEGER NOT NULL DEFAULT 0,
			UNIQUE (asset_id, version)
		);
	`); err != nil {
		t.Fatal(err)
	}
	seed(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func assertTablesDropped(t *testing.T, db *sql.DB, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if _, err := db.Exec(`SELECT * FROM ` + table); err == nil {
			t.Fatalf("legacy table %s still present after migration", table)
		}
	}
}

func TestValueEntityEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	smk := make([]byte, machinekey.KeyLen)
	if _, err := rand.Read(smk); err != nil {
		t.Fatal(err)
	}
	aad := func(secretID, version int64) []byte {
		return []byte(fmt.Sprintf("opendeploy-secret:user:s%d:v%d", secretID, version))
	}
	seal := func(t *testing.T, secretID, version int64, value string) (ct, nonce []byte) {
		t.Helper()
		ct, nonce, err := machinekey.Seal(smk, []byte(value), aad(secretID, version))
		if err != nil {
			t.Fatal(err)
		}
		return ct, nonce
	}

	seedLegacyValueTables(t, dbPath, func(db *sql.DB) {
		ct1, n1 := seal(t, 3, 1, "v1")
		ct2, n2 := seal(t, 3, 2, "v2")
		mustExec(t, db, `INSERT INTO secrets (id, name, value_directory_id, created_at) VALUES (3, 'token', 7, 1000)`)
		mustExec(t, db, `INSERT INTO secret_versions (id, secret_id, version, smk_version, ciphertext, nonce, created_at, author, global_seq)
			VALUES (5, 3, 1, 1, ?, ?, 1000, 4, 10), (8, 3, 2, 1, ?, ?, 2000, 4, 12)`, ct1, n1, ct2, n2)
		mustExec(t, db, `INSERT INTO secret_spaces (secret_id, author, created_at, space_id, global_seq)
			VALUES (3, 4, 1000, 1, 10), (3, 9, 3000, 2, 13)`)
		ct3, n3 := seal(t, 4, 1, "gone")
		mustExec(t, db, `INSERT INTO secrets (id, name, value_directory_id, created_at, deleted_at) VALUES (4, 'old', 0, 1500, 5000)`)
		mustExec(t, db, `INSERT INTO secret_versions (id, secret_id, version, smk_version, ciphertext, nonce, created_at, author, global_seq)
			VALUES (9, 4, 1, 1, ?, ?, 1500, 4, 11)`, ct3, n3)
		mustExec(t, db, `INSERT INTO secret_spaces (secret_id, author, created_at, space_id, global_seq)
			VALUES (4, 4, 1500, 1, 11)`)

		mustExec(t, db, `INSERT INTO configs (id, name, value_directory_id, created_at) VALUES (2, 'mode', 0, 1000)`)
		mustExec(t, db, `INSERT INTO config_versions (id, config_id, version, value, created_at, author, global_seq)
			VALUES (4, 2, 1, 'one', 1000, 4, 20), (6, 2, 2, 'two', 2000, 5, 22)`)
		mustExec(t, db, `INSERT INTO config_spaces (config_id, author, created_at, space_id, global_seq) VALUES (2, 4, 1000, 1, 20)`)
		mustExec(t, db, `INSERT INTO configs (id, name, value_directory_id, created_at, deleted_at) VALUES (5, 'dead', 0, 1200, 6000)`)
		mustExec(t, db, `INSERT INTO config_versions (id, config_id, version, value, created_at, author, global_seq)
			VALUES (7, 5, 1, 'kept', 1200, 4, 21)`)
		mustExec(t, db, `INSERT INTO config_spaces (config_id, author, created_at, space_id, global_seq) VALUES (5, 4, 1200, 1, 21)`)

		mustExec(t, db, `INSERT INTO assets (id, key, asset_directory_id, created_at) VALUES (6, 'app.conf', 0, 1000)`)
		mustExec(t, db, `INSERT INTO asset_versions (id, asset_id, version, created_at, author, size_bytes, sha256, global_seq)
			VALUES (11, 6, 1, 1000, 4, 3, 'abc123', 30)`)
		mustExec(t, db, `INSERT INTO asset_spaces (asset_id, author, created_at, space_id, global_seq) VALUES (6, 4, 1000, 1, 30)`)
	})

	q := Open(dbPath)
	defer q.Close()
	ctx := context.Background()
	db := q.sqlDB()

	mustExec(t, db, `INSERT INTO asset_store (id, sha256, size_bytes, inline_blob, created_at)
		VALUES ('store-1', 'abc123', 3, x'616263', 1000), ('staging-1', '', 0, x'', 1000)`)

	records, err := q.ListSecretVersionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("migrated secret records = %+v, want 2 (deleted secret excluded)", records)
	}
	if records[0].ID != 5 || records[0].Version != 1 || records[1].ID != 8 || records[1].Version != 2 {
		t.Fatalf("migrated secret record ids/versions = %+v, want ids 5/8 versions 1/2", records)
	}
	if records[0].Name != "token" || records[0].SpaceID != 2 {
		t.Fatalf("migrated secret identity = name %q space %d, want current token/2", records[0].Name, records[0].SpaceID)
	}
	for i, rec := range records {
		pt, err := machinekey.Open(smk, rec.Ciphertext, rec.Nonce, aad(rec.SecretID, rec.Version))
		if err != nil {
			t.Fatalf("migrated secret record %d does not decrypt under its AAD tuple: %v", i, err)
		}
		if want := fmt.Sprintf("v%d", rec.Version); string(pt) != want {
			t.Fatalf("migrated secret record %d plaintext = %q, want %q", i, pt, want)
		}
	}

	var spaceEvents, maxVersion, deleteEvents int64
	if err := db.QueryRow(`SELECT COUNT(*), MAX(version) FROM secret_event_log WHERE secret_id = 3`).Scan(&spaceEvents, &maxVersion); err != nil {
		t.Fatal(err)
	}
	if spaceEvents != 3 || maxVersion != 3 {
		t.Fatalf("secret 3 events = %d rows max version %d, want 3/3", spaceEvents, maxVersion)
	}
	var spaceEventID, spaceVersion int64
	var ciphertext []byte
	if err := db.QueryRow(`SELECT id, space_version, ciphertext FROM secret_event_log WHERE secret_id = 3 AND version = 3`).
		Scan(&spaceEventID, &spaceVersion, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if spaceEventID <= 9 || spaceVersion != 2 || ciphertext != nil {
		t.Fatalf("space event = id %d space_version %d payload %v, want fresh id above 9, space_version 2, NULL payload", spaceEventID, spaceVersion, ciphertext)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM secret_event_log WHERE secret_id = 4 AND event_type = 3 AND event_time = 5000 AND author = 0 AND ciphertext IS NULL`).Scan(&deleteEvents); err != nil {
		t.Fatal(err)
	}
	if deleteEvents != 1 {
		t.Fatalf("deleted secret delete events = %d, want 1", deleteEvents)
	}

	ref, err := q.GetConfigVersionByID(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ConfigID != 2 || ref.Version != 2 || ref.Value != "two" || ref.Name != "mode" {
		t.Fatalf("migrated config version = %+v", ref)
	}
	deletedRef, err := q.GetConfigVersionByID(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if deletedRef.ConfigID != 5 || deletedRef.Value != "kept" {
		t.Fatalf("pinned version of deleted config = %+v", deletedRef)
	}
	if _, err := q.GetConfigRowByID(ctx, 5); err != sql.ErrNoRows {
		t.Fatalf("deleted config current read err = %v, want ErrNoRows", err)
	}

	joined, err := q.GetAssetVersionJoinedByID(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Version.AssetID != 6 || joined.Version.Version != 1 || joined.Version.Sha256 != "abc123" ||
		joined.Asset.Key != "app.conf" || string(joined.Store.InlineBlob) != "abc" {
		t.Fatalf("migrated asset version = %+v", joined)
	}
	unreferenced, err := q.ListUnreferencedAssetStoreRows(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(unreferenced) != 1 || unreferenced[0].ID != "staging-1" {
		t.Fatalf("unreferenced store rows = %+v, want only staging-1", unreferenced)
	}

	assertTablesDropped(t, db,
		"secrets", "secret_spaces", "secret_versions",
		"configs", "config_spaces", "config_versions",
		"assets", "asset_spaces", "asset_versions")

	if next, err := q.NextSecretID(ctx); err != nil || next != 5 {
		t.Fatalf("NextSecretID = %d, %v, want 5", next, err)
	}
	if next, err := q.NextConfigID(ctx); err != nil || next != 6 {
		t.Fatalf("NextConfigID = %d, %v, want 6", next, err)
	}
	if next, err := q.NextAssetID(ctx); err != nil || next != 7 {
		t.Fatalf("NextAssetID = %d, %v, want 7", next, err)
	}
	newRowID, err := q.InsertConfigEvent(ctx, ConfigEvent{
		EventTime: 9000, CreatedTime: 9000, ConfigID: 5, Version: 3, ValueVersion: 2, SpaceVersion: 1,
		Name: "dead", SpaceID: 1, Value: sql.NullString{String: "x", Valid: true}, EventType: EventUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if newRowID <= 7 {
		t.Fatalf("new config event id = %d, want above every migrated pinned id", newRowID)
	}

	var before int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM secret_event_log`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = Open(dbPath)
	defer q.Close()
	var after int64
	if err := q.sqlDB().QueryRow(`SELECT COUNT(*) FROM secret_event_log`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("secret event log rows changed across reopen: %d -> %d", before, after)
	}
}

func TestNodeEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE nodes (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at    INTEGER NOT NULL DEFAULT 0,
			enrolled_at   INTEGER NOT NULL DEFAULT 0,
			name          TEXT    NOT NULL,
			identifier    TEXT    NOT NULL DEFAULT '',
			UNIQUE(name),
			UNIQUE(identifier)
		);
		CREATE TABLE node_versions (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			node_id        INTEGER NOT NULL,
			version        INTEGER NOT NULL,
			created_at     INTEGER NOT NULL,
			author         INTEGER NOT NULL DEFAULT 0,
			status         INTEGER NOT NULL DEFAULT 0,
			roles          TEXT    NOT NULL DEFAULT '[]',
			addresses      TEXT    NOT NULL DEFAULT '[]',
			wg_public_key  TEXT    NOT NULL DEFAULT '',
			allowed_spaces TEXT    NOT NULL DEFAULT '[0]',
			global_seq     INTEGER NOT NULL DEFAULT 0,
			UNIQUE (node_id, version)
		);
		INSERT INTO nodes (id, created_at, enrolled_at, name, identifier)
		VALUES (1, 1000, 1000, 'primary', 'primary-id'), (3, 2000, 2500, 'worker', 'worker-id');
		INSERT INTO node_versions (node_id, version, created_at, author, status, roles, addresses, wg_public_key, allowed_spaces, global_seq)
		VALUES (1, 1, 1000, 0, 4, '[0]', '[]', '', '[0,1]', 5),
		       (3, 1, 2000, 0, 1, '[1]', '["10.0.0.2"]', '', '[0,1]', 6),
		       (3, 2, 2500, 0, 4, '[1]', '["10.0.0.2"]', 'wgkey', '[0,1]', 8);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	q := Open(dbPath)
	defer q.Close()
	ctx := context.Background()

	worker, err := q.GetNodeRowByID(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name != "worker" || worker.Identifier != "worker-id" || worker.Version != 2 ||
		worker.Status != 4 || worker.WgPublicKey != "wgkey" || worker.CreatedAt != 2000 || worker.EnrolledAt != 2500 {
		t.Fatalf("migrated worker = %+v", worker)
	}
	events, err := q.ListNodeEvents(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Version != 1 || events[0].EventType != 1 ||
		events[1].Version != 2 || events[1].EventType != 2 || events[1].GlobalSeq != 8 ||
		events[0].Name != "worker" || events[0].EnrolledTime != 2500 {
		t.Fatalf("migrated worker events = %+v", events)
	}

	assertTablesDropped(t, q.sqlDB(), "nodes", "node_versions")

	if _, err := q.InsertNodeRow(ctx, InsertNodeParams{CreatedAt: 3000, Name: "new", Identifier: "new-id"}); err != nil {
		t.Fatal(err)
	}
	created, err := q.GetNodeRowByIdentifier(ctx, "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 4 {
		t.Fatalf("new node id = %d, want MAX(node_id)+1 = 4", created.ID)
	}
}

func TestNetworkPolicyEventLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`
		CREATE TABLE network_policies (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			deleted_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE network_policy_versions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			policy_id  INTEGER NOT NULL,
			version    INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			author     INTEGER NOT NULL DEFAULT 0,
			data_blob  BLOB    NOT NULL,
			global_seq INTEGER NOT NULL DEFAULT 0,
			UNIQUE (policy_id, version)
		);
		INSERT INTO network_policies (id, deleted_at) VALUES (1, 0), (2, 7000);
		INSERT INTO network_policy_versions (policy_id, version, created_at, author, data_blob, global_seq)
		VALUES (1, 1, 1000, 4, x'01', 40), (1, 2, 2000, 5, x'02', 42),
		       (2, 1, 1500, 4, x'03', 41);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	q := Open(dbPath)
	defer q.Close()
	ctx := context.Background()

	latest, err := q.ListLatestNetworkPolicyEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("latest policy events = %+v, want 2", latest)
	}
	if latest[0].PolicyID != 1 || latest[0].Version != 2 || latest[0].EventType != 2 ||
		string(latest[0].DataBlob) != "\x02" || latest[0].GlobalSeq != 42 || latest[0].CreatedTime != 1000 {
		t.Fatalf("live policy latest event = %+v", latest[0])
	}
	if latest[1].PolicyID != 2 || latest[1].Version != 2 || latest[1].EventType != 3 ||
		string(latest[1].DataBlob) != "\x03" || latest[1].EventTime != 7000 || latest[1].Author != 0 {
		t.Fatalf("deleted policy tombstone = %+v", latest[1])
	}

	assertTablesDropped(t, q.sqlDB(), "network_policies", "network_policy_versions")

	if next, err := q.NextNetworkPolicyID(ctx); err != nil || next != 3 {
		t.Fatalf("NextNetworkPolicyID = %d, %v, want 3", next, err)
	}

	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = Open(dbPath)
	defer q.Close()
	var rows int64
	if err := q.sqlDB().QueryRow(`SELECT COUNT(*) FROM network_policy_event_log`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Fatalf("policy event log rows after reopen = %d, want 4", rows)
	}
}
