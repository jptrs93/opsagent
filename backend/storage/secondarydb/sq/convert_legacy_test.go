package sq

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestOpenMarksAFreshDatabaseAsCurrent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	q := Open(dbPath)
	defer q.Close()
	raw, err := q.GetLocalKV(context.Background(), formatVersionKey)
	if err != nil || string(raw) != "2" {
		t.Fatalf("format version = %q, %v", raw, err)
	}
	if legacyDataModel(q.sqlDB()) {
		t.Fatal("a fresh database reads as version 1")
	}
}

// TestConversionOfASecondaryCopy opens a copy of a real v0.0.615 secondary
// database named by OPENDEPLOY_CONVERSION_CHECK_SECONDARY_DB and checks that
// every surviving cache row decodes under the new contract. Rows the rules
// refuse are dropped and refetched, so their count is reported, not failed.
func TestConversionOfASecondaryCopy(t *testing.T) {
	source := os.Getenv("OPENDEPLOY_CONVERSION_CHECK_SECONDARY_DB")
	if source == "" {
		t.Skip("set OPENDEPLOY_CONVERSION_CHECK_SECONDARY_DB to a copy of a v0.0.615 secondary.db")
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	src := sqlitedb.MustOpen(source)
	var instances, statuses, extras int
	if err := src.QueryRow(`SELECT COUNT(*) FROM local_scheduled_instance_cache`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if err := src.QueryRow(`SELECT COUNT(*), SUM(length(runner_extra_blob) > 0) FROM scheduled_instance_status`).Scan(&statuses, &extras); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`VACUUM INTO ?`, dbPath); err != nil {
		t.Fatal(err)
	}
	src.Close()
	q := Open(dbPath)
	defer q.Close()
	rows, err := q.sqlDB().QueryContext(ctx, `SELECT instance_id, blob FROM local_scheduled_instance_cache`)
	if err != nil {
		t.Fatal(err)
	}
	kept := 0
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			t.Fatal(err)
		}
		st, err := apigen.DecodeScheduledInstanceState(blob)
		if err != nil {
			t.Fatalf("instance %d: %v", id, err)
		}
		if err := st.Validate(); err != nil || st.Instance.ID != uint64(id) {
			t.Fatalf("instance %d: %+v %v", id, st.Instance, err)
		}
		kept++
	}
	rows.Close()
	if kept != instances {
		t.Logf("instance cache: %d of %d rows survived the conversion; the primary resends the rest at the session head", kept, instances)
	}
	if raw, ok := mustKV(t, q, storage.LocalKVClusterNetwork); ok {
		if _, err := apigen.DecodeClusterNetworkInfo(raw); err != nil {
			t.Fatalf("cluster_network: %v", err)
		}
	}
	if raw, ok := mustKV(t, q, storage.LocalKVClusterNetMap); ok {
		m, err := apigen.DecodeClusterNetMap(raw)
		if err != nil {
			t.Fatalf("net map: %v", err)
		}
		t.Logf("net map: node %d, %d nodes, %d routes, seq %d", m.TargetNodeID, len(m.Nodes), len(m.Routes), m.DerivedFromSeq)
	}
	if _, ok := mustKV(t, q, storage.LocalKVAcmeState); ok {
		t.Fatal("acme_state survived the conversion")
	}
	var after, hasDeploymentID int
	if err := q.sqlDB().QueryRow(`SELECT COUNT(*) FROM scheduled_instance_status`).Scan(&after); err != nil || after != statuses {
		t.Fatalf("status rows = %d of %d, %v", after, statuses, err)
	}
	if err := q.sqlDB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduled_instance_status') WHERE name = 'deployment_id'`).Scan(&hasDeploymentID); err != nil || hasDeploymentID != 0 {
		t.Fatalf("deployment_id column present: %d, %v", hasDeploymentID, err)
	}
	if _, err := q.ListLatestScheduledInstanceStatuses(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("converted %d instances, %d status rows (%d with extra blobs)", kept, statuses, extras)
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	Open(dbPath).Close()
}

func mustKV(t *testing.T, q *Queries, key string) ([]byte, bool) {
	t.Helper()
	raw, err := q.GetLocalKV(context.Background(), key)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, false
		}
		t.Fatal(err)
	}
	return raw, true
}
