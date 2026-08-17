package state

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestSpaceAssignmentHistory(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	sha := store.MustPutInlineAssetContent([]byte("content"))
	v, err := store.CreateAssetWithVersion("app.conf", DefaultSpaceID, 0, 5, sha, 7)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := store.MoveAssetSpace(v.AssetID, 2, 0, 9); err != nil {
		t.Fatalf("move asset space: %v", err)
	}
	// A directory-only move through the space endpoint must not log.
	if err := store.MoveAssetSpace(v.AssetID, 2, 0, 9); err != nil {
		t.Fatalf("repeat asset space move: %v", err)
	}
	rows, err := store.q.ListAssetSpaceRowsByAssetID(t.Context(), int64(v.AssetID))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("asset space log rows = %d, want 2", len(rows))
	}
	if rows[0].SpaceID != int64(DefaultSpaceID) || rows[0].Author != 5 {
		t.Fatalf("initial assignment = space %d author %d, want space %d author 5", rows[0].SpaceID, rows[0].Author, DefaultSpaceID)
	}
	if rows[1].SpaceID != 2 || rows[1].Author != 9 {
		t.Fatalf("move row = space %d author %d, want space 2 author 9", rows[1].SpaceID, rows[1].Author)
	}
	if a, ok := store.GetAssetRow(v.AssetID); !ok || a.SpaceID != 2 {
		t.Fatalf("asset row after move = %+v ok=%v, want current space 2", a, ok)
	}

	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 5, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := store.MoveSecretSpace(sec.SecretID, 2, 0, 9); err != nil {
		t.Fatalf("move secret space: %v", err)
	}
	secRows, err := store.q.ListSecretSpaceRowsBySecretID(t.Context(), int64(sec.SecretID))
	if err != nil {
		t.Fatal(err)
	}
	if len(secRows) != 2 || secRows[0].SpaceID != int64(DefaultSpaceID) || secRows[0].Author != 5 || secRows[1].SpaceID != 2 || secRows[1].Author != 9 {
		t.Fatalf("secret space log = %+v, want initial space %d author 5 then space 2 author 9", secRows, DefaultSpaceID)
	}

	cfg, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 5, "on")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if err := store.MoveConfigSpace(cfg.ID, 2, 0, 9); err != nil {
		t.Fatalf("move config space: %v", err)
	}
	cfgRows, err := store.q.ListConfigSpaceRowsByConfigID(t.Context(), int64(cfg.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgRows) != 2 || cfgRows[0].SpaceID != int64(DefaultSpaceID) || cfgRows[0].Author != 5 || cfgRows[1].SpaceID != 2 || cfgRows[1].Author != 9 {
		t.Fatalf("config space log = %+v, want initial space %d author 5 then space 2 author 9", cfgRows, DefaultSpaceID)
	}
	if meta, ok := store.GetConfigMeta(cfg.ID); !ok || meta.SpaceID != 2 {
		t.Fatalf("config meta after move = %+v ok=%v, want current space 2", meta, ok)
	}
}

func TestSoftDeleteHidesRowAndFreesName(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()

	v := store.SetAssetByKey("app.conf", []byte("v1"))
	store.DeleteAsset(v.AssetID)
	if _, ok := store.GetAssetRow(v.AssetID); ok {
		t.Fatal("deleted asset still resolves by id")
	}
	if _, ok := store.GetAssetInRootByKey(DefaultSpaceID, "app.conf"); ok {
		t.Fatal("deleted asset still resolves by key")
	}
	if got := store.ListAssets(); len(got) != 0 {
		t.Fatalf("ListAssets after delete = %d items, want 0", len(got))
	}
	// The name is reusable, and the old asset's version rows survive.
	replacement := store.SetAssetByKey("app.conf", []byte("v2"))
	if replacement.AssetID == v.AssetID {
		t.Fatal("recreated asset reused the deleted identity")
	}
	if versions := store.ListAssetVersionsJoinedOfAsset(v.AssetID); len(versions) != 1 {
		t.Fatalf("deleted asset version rows = %d, want 1 retained", len(versions))
	}

	sec, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 1, testSealFunc(1))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if err := store.DeleteSecret(sec.SecretID); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	if _, ok := store.GetSecretInRootByName(DefaultSpaceID, "token"); ok {
		t.Fatal("deleted secret still resolves by name")
	}
	if got := store.ListSecretVersionRecords(); len(got) != 0 {
		t.Fatalf("deleted secret leaked %d records into the manager load", len(got))
	}
	if _, err := store.CreateSecretWithVersion("token", DefaultSpaceID, 0, 1, testSealFunc(2)); err != nil {
		t.Fatalf("recreate secret with freed name: %v", err)
	}

	cfg, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 1, "on")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if meta, ok := store.DeleteConfig(cfg.ID); !ok || !meta.Deleted {
		t.Fatalf("delete config = %+v ok=%v", meta, ok)
	}
	if _, ok := store.GetConfigMeta(cfg.ID); ok {
		t.Fatal("deleted config still resolves by id")
	}
	if got := store.ListConfigMetas(); len(got) != 0 {
		t.Fatalf("ListConfigMetas after delete = %d items, want 0", len(got))
	}
	recreated, err := store.CreateConfigWithVersion("mode", DefaultSpaceID, 0, 1, "off")
	if err != nil {
		t.Fatalf("recreate config with freed name: %v", err)
	}
	if recreated.ID == cfg.ID {
		t.Fatal("recreated config reused the deleted identity")
	}
	// Pinned version rows of the deleted config still resolve.
	if ref, ok := store.GetConfigVersionByID(cfg.VersionRefs[0].ID); !ok || ref.Value != "on" {
		t.Fatalf("pinned version of deleted config = %+v ok=%v", ref, ok)
	}
}

// TestDeletedAtAndSpaceLogMigration builds a v0.0.441-shape database — deleted
// flags instead of deleted_at, space_id columns instead of *_spaces logs — with
// soft-deleted deployment/authz rows, and checks that opening it purges the
// tombstones (with their version and scheduled-instance history), renames the
// flags, and backfills the space logs before dropping the columns. The second
// Open proves the migration is a no-op once applied.
func TestDeletedAtAndSpaceLogMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	for _, statement := range []string{
		`CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
			node_id       INTEGER NOT NULL DEFAULT -1,
			space_id      INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
			name          TEXT    NOT NULL DEFAULT '',
			deleted       INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_deployment_configs_active_node_identity
			ON deployment_configs(node_id, space_id, name) WHERE deleted = 0`,
		`INSERT INTO deployment_configs (deployment_id, node_id, space_id, name, deleted) VALUES (1, 1, 1, 'live', 0)`,
		`INSERT INTO deployment_configs (deployment_id, node_id, space_id, name, deleted) VALUES (2, 1, 1, 'gone', 1)`,
		`CREATE TABLE deployment_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, deployment_id INTEGER NOT NULL, version INTEGER NOT NULL,
			created_at INTEGER NOT NULL, author INTEGER NOT NULL DEFAULT 0, spec_blob BLOB NOT NULL,
			UNIQUE (deployment_id, version)
		)`,
		`INSERT INTO deployment_versions (deployment_id, version, created_at, spec_blob) VALUES (1, 1, 1000, x'')`,
		`INSERT INTO deployment_versions (deployment_id, version, created_at, spec_blob) VALUES (2, 1, 1000, x'')`,
		`CREATE TABLE scheduled_instances (
			id INTEGER PRIMARY KEY AUTOINCREMENT, deployment_id INTEGER NOT NULL,
			deployment_version INTEGER NOT NULL, node_id INTEGER NOT NULL, instance_ordinal INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO scheduled_instances (id, deployment_id, deployment_version, node_id) VALUES (1, 2, 1, 1)`,
		`CREATE TABLE scheduled_instance_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, scheduled_instance_id INTEGER NOT NULL,
			version INTEGER NOT NULL, created_at INTEGER NOT NULL, state INTEGER NOT NULL,
			UNIQUE (scheduled_instance_id, version)
		)`,
		`INSERT INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state) VALUES (1, 1, 1000, 2)`,
		`CREATE TABLE scheduled_instance_status (
			scheduled_instance_id INTEGER NOT NULL, updated_at INTEGER NOT NULL, deployment_id INTEGER NOT NULL DEFAULT 0,
			preparer_config_version INTEGER, preparer_artifact TEXT,
			preparer_inputs_status INTEGER NOT NULL DEFAULT 0, preparer_image_status INTEGER NOT NULL DEFAULT 0,
			runner_config_version INTEGER, runner_pid INTEGER, runner_artifact TEXT, runner_status INTEGER,
			runner_num_restarts INTEGER, runner_last_restart_at INTEGER, runner_extra_blob BLOB NOT NULL DEFAULT x'',
			PRIMARY KEY (scheduled_instance_id, updated_at)
		)`,
		`INSERT INTO scheduled_instance_status (scheduled_instance_id, updated_at, deployment_id) VALUES (1, 1000, 2)`,
		`CREATE TABLE authz_rule_templates (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
			builtin INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_authz_rule_templates_name ON authz_rule_templates (name) WHERE deleted = 0`,
		`INSERT INTO authz_rule_templates (id, name, deleted) VALUES (1, 'live', 0)`,
		`INSERT INTO authz_rule_templates (id, name, deleted) VALUES (2, 'gone', 1)`,
		`CREATE TABLE authz_rule_template_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, template_id INTEGER NOT NULL, version INTEGER NOT NULL,
			created_at INTEGER NOT NULL, author INTEGER NOT NULL DEFAULT 0, data_blob BLOB NOT NULL,
			UNIQUE (template_id, version)
		)`,
		`INSERT INTO authz_rule_template_versions (template_id, version, created_at, data_blob) VALUES (1, 1, 1000, x'')`,
		`INSERT INTO authz_rule_template_versions (template_id, version, created_at, data_blob) VALUES (2, 1, 1000, x'')`,
		`CREATE TABLE authz_grants (
			id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, template_id INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0, author INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0, data_blob BLOB NOT NULL
		)`,
		`INSERT INTO authz_grants (id, user_id, deleted, data_blob) VALUES (1, 10, 0, x'')`,
		`INSERT INTO authz_grants (id, user_id, deleted, data_blob) VALUES (2, 10, 1, x'')`,
		`CREATE TABLE assets (
			id INTEGER PRIMARY KEY AUTOINCREMENT, space_id INTEGER NOT NULL DEFAULT 1,
			key TEXT NOT NULL, asset_directory_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
		)`,
		`INSERT INTO assets (id, space_id, key, created_at) VALUES (1, 2, 'app.conf', 1000)`,
		`CREATE TABLE secrets (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, space_id INTEGER NOT NULL DEFAULT 1,
			value_directory_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
		)`,
		`INSERT INTO secrets (id, name, space_id, created_at) VALUES (1, 'token', 1, 2000)`,
		`CREATE TABLE configs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, space_id INTEGER NOT NULL DEFAULT 1,
			value_directory_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
		)`,
		`INSERT INTO configs (id, name, space_id, created_at) VALUES (1, 'mode', 3, 3000)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		store := Open(dbPath)
		if a, ok := store.GetAssetRow(1); !ok || a.SpaceID != 2 || a.Key != "app.conf" {
			t.Fatalf("migrated asset = %+v ok=%v, want space 2", a, ok)
		}
		if s, ok := store.GetSecretInRootByName(1, "token"); !ok || s.ID != 1 {
			t.Fatalf("migrated secret = %+v ok=%v", s, ok)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM pragma_table_info('deployment_configs') WHERE name = 'deleted'`:                          0,
		`SELECT COUNT(*) FROM pragma_table_info('deployment_configs') WHERE name = 'deleted_at'`:                       1,
		`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'space_id'`:                                     0,
		`SELECT COUNT(*) FROM pragma_table_info('secrets') WHERE name = 'space_id'`:                                    0,
		`SELECT COUNT(*) FROM pragma_table_info('configs') WHERE name = 'space_id'`:                                    0,
		`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'deleted_at'`:                                   1,
		`SELECT COUNT(*) FROM pragma_table_info('secrets') WHERE name = 'deleted_at'`:                                  1,
		`SELECT COUNT(*) FROM pragma_table_info('configs') WHERE name = 'deleted_at'`:                                  1,
		`SELECT COUNT(*) FROM deployment_configs`:                                                                      1,
		`SELECT COUNT(*) FROM deployment_versions`:                                                                     1,
		`SELECT COUNT(*) FROM scheduled_instances`:                                                                     0,
		`SELECT COUNT(*) FROM scheduled_instance_versions`:                                                             0,
		`SELECT COUNT(*) FROM scheduled_instance_status`:                                                               0,
		`SELECT COUNT(*) FROM authz_rule_templates`:                                                                    1,
		`SELECT COUNT(*) FROM authz_rule_template_versions`:                                                            1,
		`SELECT COUNT(*) FROM authz_grants`:                                                                            1,
		`SELECT COUNT(*) FROM asset_spaces WHERE asset_id = 1 AND author = 0 AND created_at = 1000 AND space_id = 2`:   1,
		`SELECT COUNT(*) FROM secret_spaces WHERE secret_id = 1 AND author = 0 AND created_at = 2000 AND space_id = 1`: 1,
		`SELECT COUNT(*) FROM config_spaces WHERE config_id = 1 AND author = 0 AND created_at = 3000 AND space_id = 3`: 1,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	// The rename carried the partial identity index across.
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'idx_deployment_configs_active_node_identity'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if want := "deleted_at = 0"; !strings.Contains(strings.ToLower(indexSQL), want) {
		t.Fatalf("identity index = %q, want it filtered on %q", indexSQL, want)
	}
}
