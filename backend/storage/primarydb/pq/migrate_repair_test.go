package pq

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const legacyAssetDDL = `CREATE TABLE asset_event_log (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq         INTEGER NOT NULL,
    event_time         INTEGER NOT NULL,
    created_time       INTEGER NOT NULL,
    author             INTEGER NOT NULL,
    asset_id           INTEGER NOT NULL,
    version            INTEGER NOT NULL,
    value_version      INTEGER NOT NULL,
    value_changed      INTEGER NOT NULL,
    key                TEXT    NOT NULL,
    asset_directory_id INTEGER NOT NULL,
    space_id           INTEGER NOT NULL,
    size_bytes         INTEGER NOT NULL,
    sha256             TEXT    NOT NULL,
    event_type         INTEGER NOT NULL,
    storage_key        TEXT    NOT NULL DEFAULT ''
)`

func TestMaterialiseAppendsALegacyDeleteTheLogReplayedAlive(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	q := Open(dbPath)
	const asset = apigen.CoreEntityType_CORE_ENTITY_ASSET
	kept := &apigen.CoreEntity{Asset: &apigen.Asset{ID: 8, SpaceID: 1, Fs: &apigen.AssetFs{Key: "kept.txt"}, Sha256: "cd", SizeBytes: 2, StorageKey: "sk-8"}}
	revived := &apigen.CoreEntity{Asset: &apigen.Asset{ID: 7, SpaceID: 1, Fs: &apigen.AssetFs{Key: "gone.txt"}, Sha256: "ab", SizeBytes: 2, StorageKey: "sk-7"}}
	commitForTest(t, q, 1, 1000, 0, del(asset, 7))
	commitForTest(t, q, 2, 2000, 0, create(asset, 7, revived), create(asset, 8, kept))
	if _, err := q.db.ExecContext(ctx, legacyAssetDDL); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]any{
		{2, 2000, 2000, 0, 7, 1, 1, 1, "gone.txt", 0, 1, 2, "ab", 1, "sk-7"},
		{2, 2000, 2000, 0, 8, 1, 1, 1, "kept.txt", 0, 1, 2, "cd", 1, "sk-8"},
		{0, 3000, 2000, 0, 7, 2, 1, 0, "gone.txt", 0, 1, 2, "ab", 3, "sk-7"},
	} {
		if _, err := q.db.ExecContext(ctx, `INSERT INTO asset_event_log (global_seq, event_time, created_time, author, asset_id, version, value_version, value_changed, key, asset_directory_id, space_id, size_bytes, sha256, event_type, storage_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row...); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = Open(dbPath)
	defer q.Close()
	present, err := q.existingTables(ctx, legacyEventTables)
	if err != nil || len(present) != 0 {
		t.Fatalf("legacy tables after the move: %v, %v", present, err)
	}
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil || seq != 3 {
		t.Fatalf("global_seq %d, %v", seq, err)
	}
	logged, err := q.LatestWriteEventSeq(ctx)
	if err != nil || logged != 3 {
		t.Fatalf("latest write event %d, %v", logged, err)
	}
	latest, err := q.LatestMutation(ctx, asset, 7)
	if err != nil || latest.Delete == nil {
		t.Fatalf("asset 7 newest mutation %+v, %v", latest, err)
	}
	if rows := tableRows(t, dbPath, "assets"); len(rows) != 1 {
		t.Fatalf("assets after the move: %v", rows)
	}
	if latest, err = q.LatestMutation(ctx, asset, 8); err != nil || latest.Create == nil {
		t.Fatalf("asset 8 newest mutation %+v, %v", latest, err)
	}
}

func legacyDeploymentPayload(d *apigen.Deployment, version, specVersion uint64) []byte {
	body := d.Encode()
	body = binary.AppendUvarint(body, 15<<3)
	body = binary.AppendUvarint(body, version)
	body = binary.AppendUvarint(body, 16<<3)
	body = binary.AppendUvarint(body, specVersion)
	out := binary.AppendUvarint(nil, uint64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)<<3|2)
	out = binary.AppendUvarint(out, uint64(len(body)))
	return append(out, body...)
}

func TestRebuildKeepsTheCountersTheBackfillLogged(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	q := Open(dbPath)
	defer q.Close()
	d := &apigen.Deployment{SpaceID: 1, Name: "app"}
	const typ = int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
	for _, row := range []struct {
		seq, op, version, spec int64
	}{{1, 1, 7, 3}, {2, 2, 8, 4}} {
		if _, err := q.db.ExecContext(ctx, `INSERT INTO write_events (seq, time, actor) VALUES (?, ?, 0)`, row.seq, row.seq*1000); err != nil {
			t.Fatal(err)
		}
		if _, err := q.db.ExecContext(ctx, `INSERT INTO write_event_mutations (seq, idx, entity_type, entity_id, op, payload) VALUES (?, 0, ?, 5, ?, ?)`,
			row.seq, typ, row.op, legacyDeploymentPayload(d, uint64(row.version), uint64(row.spec))); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.SetGlobalSeq(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := q.Tx(ctx, func(tx *Queries) error { return tx.RebuildFromLog(ctx) }); err != nil {
		t.Fatal(err)
	}
	var version, spec int64
	if err := q.db.QueryRowContext(ctx, `SELECT version, spec_version FROM deployments WHERE id = 5`).Scan(&version, &spec); err != nil {
		t.Fatal(err)
	}
	if version != 8 || spec != 4 {
		t.Fatalf("rebuilt deployment 5 as version %d spec %d, the backfill logged 8 and 4", version, spec)
	}
	if rows := tableRows(t, dbPath, "deployment_versions"); len(rows) != 1 {
		t.Fatalf("deployment_versions after the rebuild: %v", rows)
	}
	if q.logged != nil {
		t.Fatal("the logged counters outlived the rebuild")
	}
	if _, ok := loggedDeploymentCounters(d.Encode()); ok {
		t.Fatal("a payload without the reserved tags reported counters")
	}
}

func TestRepairRelogsAStatusTheBackfillPlacedBeforeItsInstance(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	q := Open(dbPath)
	defer q.Close()
	const (
		deployment = apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT
		instance   = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE
		status     = apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS
	)
	report := &apigen.CoreEntity{ScheduledInstanceStatus: &apigen.ScheduledInstanceStatus{ScheduledInstanceID: 9, DeploymentID: 3, UpdatedAt: time.Unix(0, 5000),
		Runner: apigen.RunnerStatus{DeploymentSpecVersion: 1, Status: apigen.RunningStatus_RUNNING}}}
	dep := &apigen.CoreEntity{Deployment: &apigen.Deployment{Name: "app", SpaceID: 1, Scheduling: apigen.DedicatedScheduling(true, 1),
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{Source: apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "img"}}}}}}
	inst := &apigen.CoreEntity{ScheduledInstance: &apigen.ScheduledInstance{ID: 9, DeploymentID: 3, DeploymentVersion: 1, DeploymentSpecVersion: 1, NodeID: 1, InstanceOrdinal: 1, SpaceID: 1}}
	commitForTest(t, q, 1, 1000, 0, update(status, 9, report))
	commitForTest(t, q, 2, 2000, 0, create(deployment, 3, dep))
	commitForTest(t, q, 3, 3000, 0, create(instance, 9, inst))
	countStatus := func(tx *Queries) int64 {
		var n int64
		if err := tx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_instance_status WHERE scheduled_instance_id = 9`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rollback := errors.New("rollback")
	if err := q.Tx(ctx, func(tx *Queries) error {
		if err := tx.RebuildFromLog(ctx); err != nil {
			return err
		}
		if n := countStatus(tx); n != 0 {
			t.Fatalf("a plain rebuild kept the status logged before its instance: %d rows", n)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := q.Tx(ctx, func(tx *Queries) error {
		if err := tx.RebuildFromLog(ctx); err != nil {
			return err
		}
		return tx.repairLegacyLog(ctx, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if n := countStatus(q); n != 1 {
		t.Fatalf("status rows for instance 9 after the repair: %d", n)
	}
	if seq, err := q.GetGlobalSeq(ctx); err != nil || seq != 4 {
		t.Fatalf("global_seq %d, %v", seq, err)
	}
	if latest, err := q.LatestMutation(ctx, status, 9); err != nil || latest.Update == nil || latest.Entity().ScheduledInstanceStatus.Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("relogged status %+v, %v", latest, err)
	}
	if err := q.Tx(ctx, func(tx *Queries) error { return tx.repairLegacyLog(ctx, nil) }); err != nil {
		t.Fatal(err)
	}
	if seq, err := q.GetGlobalSeq(ctx); err != nil || seq != 4 {
		t.Fatalf("a second repair appended again: global_seq %d, %v", seq, err)
	}
}
