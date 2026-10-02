package state

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

var materialisedTablesForTest = []string{
	"entity_ids", "value_names", "secrets", "secret_versions", "configs", "config_versions", "value_directories",
	"asset_keys", "assets", "asset_versions", "asset_directories",
	"spaces", "users", "network_policies", "authz_rule_templates", "authz_grants", "authz_global_rules",
	"agent_sessions", "user_sessions", "nix_store_resets", "secret_keyslots", "system_config",
	"deployments", "deployment_versions", "scheduled_instances", "scheduled_instance_status", "nodes", "node_status",
}

func truncateWriteLogAfter(t *testing.T, dbPath string, seq int64) {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath)
	for _, stmt := range []string{`DELETE FROM write_event_mutations WHERE seq > ?`, `DELETE FROM write_events WHERE seq > ?`} {
		if _, err := db.Exec(stmt, seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// dumpTables reads every row of the given tables through a second
// connection, as sorted text per table.
func dumpTables(t *testing.T, dbPath string, tables []string) map[string][]string {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols := erru.Must(rows.Columns())
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(cols))
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					v = fmt.Sprintf("%x", b)
				}
				parts[i] = fmt.Sprintf("%s=%v", cols[i], v)
			}
			out[table] = append(out[table], strings.Join(parts, " "))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(out[table])
	}
	return out
}

func seedValueHistory(t *testing.T, s *Service) {
	t.Helper()
	ctx := apigen.Context{}
	node := testNode(s, "node-a")
	space := createSpaceForTest(s, "team")
	deleteSpaceForTest(s, createSpaceForTest(s, "disbanded").ID)
	createUserForTest(s, "alice")
	deleteNetworkPolicyForTest(s, createNetworkPolicyForTest(s, 1))
	createNetworkPolicyForTest(s, 1)
	insertRuleTemplateForTest(t, s, "ops", 1)
	deleteGrantForTest(t, s, insertGrantForTest(t, s, 1, 1, 10))
	insertGrantForTest(t, s, 1, 1, 11)
	insertGlobalRuleForTest(t, s, "lockdown", 1)
	seedLatestOnlyHistory(t, s)
	dep := mustCreateDeploymentForNode(s, ctx, space.ID, "web", node.ID, testSpecWithVersion("v1"))
	pinned := createScheduledInstanceForTest(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	writeInstanceStatusForTest(s, pinned.ID, func(st *apigen.ScheduledInstanceStatus) {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING, RunningPid: 7}
	})
	kept := mustCreateDeploymentForNode(s, ctx, space.ID, "kept", node.ID, testSpecWithVersion("v1"))
	retired := createScheduledInstanceForTest(s, kept.DeploymentID, kept.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setScheduledInstanceState(s, retired.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	updateDeploymentSpec(s, ctx, kept.DeploymentID, testSpecWithVersion("v2"))
	createScheduledInstanceForTest(s, kept.DeploymentID, kept.Version+1, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setNodeStatusForTest(s, "node-a", true, time.Now())
	asset := setAssetByKeyForTest(s, "bundle", []byte("x"))
	setAssetByKeyForTest(s, "bundle", []byte("xy"))
	deleteAssetForTest(s, setAssetByKeyForTest(s, "gone", []byte("z")).AssetID)
	dir := createAssetDirectoryForTest(s, 1, 0, "dir", 1)
	deleteAssetDirectoryForTest(s, createAssetDirectoryForTest(s, 1, dir.ID, "sub", 1).ID)
	secret := createSecretForTest(s, "token")
	appendSecretVersionForTest(s, secret, []byte{5})
	carrySecretForTest(s, secret, "token-renamed", apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	carrySecretForTest(s, createSecretForTest(s, "doomed"), "doomed", apigen.AuthzVerb_AUTHZ_VERB_DELETE)
	config := createConfigForTest(s, "level", "one")
	writeConfigForTest(s, config, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, "two")
	renameConfigForTest(s, config, "level-renamed")
	writeConfigForTest(s, createConfigForTest(s, "stale", "one"), apigen.AuthzVerb_AUTHZ_VERB_DELETE, "")
	folder := createValueDirectoryForTest(s, 1, 0, "folder", 1)
	deleteValueDirectoryForTest(s, createValueDirectoryForTest(s, 1, folder.ID, "nested", 1).ID)
	updateDeploymentSpec(s, ctx, dep.DeploymentID, testSpecWithVersion("v2"))
	deleteDeployment(s, ctx, dep.DeploymentID)
	if asset.AssetID == 0 {
		t.Fatal("asset was not created")
	}
}

func TestRebuildFromLogReproducesMaterialisedTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	s := Open(dbPath)
	defer s.Close()
	seedValueHistory(t, s)
	ctx := context.Background()
	before := dumpTables(t, dbPath, materialisedTablesForTest)
	for _, table := range materialisedTablesForTest {
		if len(before[table]) == 0 {
			t.Fatalf("scenario left %s empty", table)
		}
	}
	if err := s.q.RebuildFromLog(ctx); err != nil {
		t.Fatal(err)
	}
	after := dumpTables(t, dbPath, materialisedTablesForTest)
	for _, table := range materialisedTablesForTest {
		if !reflect.DeepEqual(before[table], after[table]) {
			t.Fatalf("%s differs after the rebuild\nlive:\n%s\nrebuilt:\n%s", table, strings.Join(before[table], "\n"), strings.Join(after[table], "\n"))
		}
	}
	next := createSecretForTest(s, "after-rebuild")
	var maxID sql.NullInt64
	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	if err := db.QueryRow(`SELECT MAX(entity_id) FROM write_event_mutations WHERE entity_type = ? AND seq < (SELECT MAX(seq) FROM write_events)`, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET)).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if next <= maxID.Int64 {
		t.Fatalf("id %d allocated after the rebuild reuses an id at or below %d", next, maxID.Int64)
	}
}

func TestOpenRefusesTruncatedWriteLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	s := Open(dbPath)
	seedValueHistory(t, s)
	seq := erru.Must(s.q.GetGlobalSeq(context.Background()))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	truncateWriteLogAfter(t, dbPath, seq-2)
	defer func() {
		if recover() == nil {
			t.Fatal("Open accepted a database whose write log stops short of its sequence")
		}
	}()
	Open(dbPath).Close()
}
