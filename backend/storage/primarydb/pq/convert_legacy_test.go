package pq

import (
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func oldEntity(t *testing.T, e apigenold.CoreEntity) []byte {
	t.Helper()
	return e.Encode()
}

// withOldCounters appends Deployment tags 15 and 16 (the counters the v0.0.614
// backfill wrote) inside the CoreEntity deployment field of an old payload.
func withOldCounters(t *testing.T, d *apigenold.Deployment, version, spec uint64) []byte {
	t.Helper()
	body := d.Encode()
	body = binary.AppendUvarint(body, 15<<3)
	body = binary.AppendUvarint(body, version)
	body = binary.AppendUvarint(body, 16<<3)
	body = binary.AppendUvarint(body, spec)
	out := binary.AppendUvarint(nil, 1<<3|2)
	out = binary.AppendUvarint(out, uint64(len(body)))
	return append(out, body...)
}

func writeVersion1Log(t *testing.T, dbPath string) {
	t.Helper()
	db := sqlitedb.MustOpenWriter(dbPath)
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE write_events (seq INTEGER PRIMARY KEY, time INTEGER NOT NULL, actor INTEGER NOT NULL)`,
		`CREATE TABLE write_event_mutations (seq INTEGER NOT NULL, idx INTEGER NOT NULL, entity_type INTEGER NOT NULL, entity_id INTEGER NOT NULL, op INTEGER NOT NULL, payload BLOB, PRIMARY KEY (seq, idx))`,
		`CREATE TABLE global_seq (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL)`,
		`CREATE TABLE entity_ids (entity_type INTEGER PRIMARY KEY, next INTEGER NOT NULL)`,
		`CREATE TABLE value_names (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE authz_rule_templates (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE asset_store (id TEXT PRIMARY KEY, sha256 TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0, local_status INTEGER NOT NULL DEFAULT 0, remote_status INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	deployment := &apigenold.Deployment{
		ID: 7, SpaceID: 1, Name: "app",
		Spec: apigenold.DeploymentSpec{
			Networking:     apigenold.NetworkingConfig{Mode: apigenold.NetworkingMode_NETWORKING_MODE_HOST},
			Container1Spec: &apigenold.ContainerSpec{Source: apigenold.ContainerBundleSource{RemoteImage: &apigenold.RemoteDockerImage{Image: "example/app"}}, Version: "1.2", UpgradeStrategy: apigenold.ContainerUpgradeStrategy_RECREATE},
		},
		Scheduling: apigenold.Scheduling{Running: true, DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{1}}},
	}
	rows := []struct {
		seq, idx, typ, id, op int64
		payload               []byte
	}{
		{0, 0, int64(apigen.CoreEntityType_CORE_ENTITY_SPACE), 0, 1, oldEntity(t, apigenold.CoreEntity{Space: &apigenold.Space{ID: 0, Name: "_system"}})},
		{0, 1, int64(apigen.CoreEntityType_CORE_ENTITY_SPACE), 1, 1, oldEntity(t, apigenold.CoreEntity{Space: &apigenold.Space{ID: 1, Name: "global"}})},
		{1, 0, int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), 7, 1, withOldCounters(t, deployment, 3, 2)},
		{2, 0, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT), 513, 1, oldEntity(t, apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE, NodeID: 2, SmkVersion: 1, WrappedSmk: []byte("wrapped"), Nonce: []byte("nonce")}})},
		{3, 0, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT), 2, 1, oldEntity(t, apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY, SmkVersion: 1, WrappedSmk: []byte("wrapped"), Nonce: []byte("nonce"), KdfSalt: []byte("salt")}})},
		{4, 0, int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), 7, 2, oldEntity(t, apigenold.CoreEntity{Deployment: deployment})},
		{5, 0, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT), 513, 3, nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT OR IGNORE INTO write_events (seq, time, actor) VALUES (?, ?, 0)`, r.seq, 1000+r.seq); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO write_event_mutations (seq, idx, entity_type, entity_id, op, payload) VALUES (?, ?, ?, ?, ?, ?)`, r.seq, r.idx, r.typ, r.id, r.op, r.payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO global_seq (id, value) VALUES (1, 5)`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenConvertsAVersion1Log(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	writeVersion1Log(t, dbPath)
	probe := sqlitedb.MustOpen(dbPath)
	report, err := DryRunConversion(ctx, probe)
	probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Refusals) != 0 || report.Converted[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT] != 2 || report.Counters != 1 || report.Deletes != 1 {
		t.Fatalf("dry run report = %+v", report)
	}
	if !reflect.DeepEqual(report.KeyslotIDs, map[uint64]uint64{513: 1, 2: 2}) {
		t.Fatalf("keyslot ids = %v", report.KeyslotIDs)
	}
	if _, err := os.Stat(dbPath + preConversionSuffix); err == nil {
		t.Fatal("dry run wrote the pre-conversion copy")
	}
	q := Open(dbPath)
	defer q.Close()
	if _, err := os.Stat(dbPath + preConversionSuffix); err != nil {
		t.Fatalf("pre-conversion copy: %v", err)
	}
	var version int64
	if err := q.db.QueryRowContext(ctx, `SELECT version FROM format_version WHERE id = 1`).Scan(&version); err != nil || version != DataModelFormatVersion {
		t.Fatalf("format_version = %d, %v", version, err)
	}
	var logged, loggedSpec sql.NullInt64
	if err := q.db.QueryRowContext(ctx, `SELECT version, spec_version FROM write_event_mutations WHERE seq = 1`).Scan(&logged, &loggedSpec); err != nil {
		t.Fatal(err)
	}
	if logged.Int64 != 3 || loggedSpec.Int64 != 2 {
		t.Fatalf("logged counters = %v %v", logged, loggedSpec)
	}
	if err := q.db.QueryRowContext(ctx, `SELECT version, spec_version FROM write_event_mutations WHERE seq = 4`).Scan(&logged, &loggedSpec); err != nil {
		t.Fatal(err)
	}
	if logged.Valid || loggedSpec.Valid {
		t.Fatalf("a payload without counters logged %v %v", logged, loggedSpec)
	}
	record, err := q.GetLatestDeployment(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if record.Meta.Version != 4 || record.Meta.SpecVersion != 2 || record.Deployment.Spec.Workload.Value.Container.Source.Value.RemoteImage.Image != "example/app" {
		t.Fatalf("converted deployment = %+v", record)
	}
	var ids []int64
	rows, err := q.db.QueryContext(ctx, `SELECT entity_id FROM write_event_mutations WHERE entity_type = ? ORDER BY seq`, int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if !reflect.DeepEqual(ids, []int64{1, 2, 1}) {
		t.Fatalf("keyslot entity ids in the log = %v", ids)
	}
	slots, err := q.ListSecretKeyslots(ctx)
	if err != nil || len(slots) != 1 || slots[0].ID != 2 || slots[0].Wrapping.Value.RecoveryCode == nil || string(slots[0].Wrapping.Value.RecoveryCode.KdfSalt) != "salt" {
		t.Fatalf("keyslots after conversion = %+v, %v", slots, err)
	}
	next, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT)
	if err != nil || next != 3 {
		t.Fatalf("next keyslot id = %d, %v", next, err)
	}
	var stale int64
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('value_names', 'authz_rule_templates')`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("old tables left behind: %d, %v", stale, err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	copyBefore, _ := os.Stat(dbPath + preConversionSuffix)
	again := Open(dbPath)
	defer again.Close()
	copyAfter, _ := os.Stat(dbPath + preConversionSuffix)
	if copyBefore.ModTime() != copyAfter.ModTime() || copyBefore.Size() != copyAfter.Size() {
		t.Fatal("a second open touched the pre-conversion copy")
	}
	probe = sqlitedb.MustOpen(dbPath)
	defer probe.Close()
	if report, err := DryRunConversion(ctx, probe); err != nil || len(report.Converted) != 0 {
		t.Fatalf("a converted database reports work: %+v, %v", report, err)
	}
}

// TestConversionOfAClusterCopy runs the conversion on a copy of a real
// v0.0.615 primary database named by OPENDEPLOY_CONVERSION_CHECK_DB: the dry
// run must refuse nothing, the open must convert, every reader must run, and
// a rebuild from the converted log must reproduce the snapshot.
func TestConversionOfAClusterCopy(t *testing.T) {
	source := os.Getenv("OPENDEPLOY_CONVERSION_CHECK_DB")
	if source == "" {
		t.Skip("set OPENDEPLOY_CONVERSION_CHECK_DB to a copy of a v0.0.615 primary.db")
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	src := sqlitedb.MustOpen(source)
	if _, err := src.Exec(`VACUUM INTO ?`, dbPath); err != nil {
		t.Fatal(err)
	}
	src.Close()
	probe := sqlitedb.MustOpen(dbPath)
	report, err := DryRunConversion(ctx, probe)
	probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Refusals) > 0 {
		var lines []string
		for _, r := range report.Refusals {
			lines = append(lines, r.String())
		}
		t.Fatalf("%d refusals:\n%s", len(report.Refusals), strings.Join(lines, "\n"))
	}
	t.Logf("dry run: converted %v, deletes %d, counters %d, keyslots %v", report.Converted, report.Deletes, report.Counters, report.KeyslotIDs)
	q := Open(dbPath)
	defer q.Close()
	before, err := q.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Tx(ctx, func(tx *Queries) error { return tx.RebuildFromLog(ctx) }); err != nil {
		t.Fatal(err)
	}
	after, err := q.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a second rebuild differs from the first: %d vs %d entities", len(before), len(after))
	}
	counts := map[apigen.CoreEntityType]int{}
	for _, e := range after {
		counts[e.EntityType]++
	}
	t.Logf("snapshot: %v", counts)
	readers := map[string]func() error{
		"spaces":            func() error { _, err := q.ListSpaces(ctx); return err },
		"users":             func() error { _, err := q.ListFullUsers(ctx); return err },
		"deployments":       func() error { _, err := q.ListActiveDeployments(ctx); return err },
		"deployment pins":   func() error { _, err := q.ListRetainedDeploymentVersions(ctx); return err },
		"deleted":           func() error { _, err := q.ListDeletedDeployments(ctx); return err },
		"instances":         func() error { _, err := q.ListRetainedScheduledInstances(ctx); return err },
		"instance states":   func() error { _, err := q.ListLiveScheduledInstanceStates(ctx); return err },
		"instance statuses": func() error { _, err := q.ListLatestScheduledInstanceStatuses(ctx); return err },
		"node statuses":     func() error { _, err := q.ListLatestNodeStatuses(ctx); return err },
		"templates":         func() error { _, err := q.ListAuthzGrantTemplates(ctx); return err },
		"grants":            func() error { _, err := q.ListAuthzGrants(ctx); return err },
		"global rules":      func() error { _, err := q.ListAuthzGlobalRules(ctx); return err },
		"policies":          func() error { _, err := q.ListNetworkPolicies(ctx); return err },
		"secrets":           func() error { _, err := q.ListSecretVersionJoined(ctx); return err },
		"configs":           func() error { _, err := q.ListConfigVersions(ctx); return err },
		"assets":            func() error { _, err := q.ListAssetVersions(ctx); return err },
		"keyslots":          func() error { _, err := q.ListSecretKeyslots(ctx); return err },
		"agent sessions":    func() error { _, err := q.ListAllAgentSessions(ctx); return err },
		"user sessions":     func() error { _, err := q.ListAllUserSessions(ctx); return err },
		"nix store resets":  func() error { _, err := q.ListNixStoreResetRows(ctx); return err },
		"system config":     func() error { _, err := q.GetSystemConfig(ctx); return err },
	}
	for name, read := range readers {
		if err := read(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	Open(dbPath).Close()
}
