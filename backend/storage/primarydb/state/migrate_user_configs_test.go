package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestMigrateConfigsToEventLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := pq.Open(path)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	cfg, err := q.InsertConfigRow(ctx, pq.InsertConfigRowParams{Name: "log-level", SpaceID: 1, ValueDirectoryID: 0, CreatedAt: now, CreatedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	var rowIDs []int64
	for i, value := range []string{"debug", "info", "warn"} {
		v, err := q.InsertConfigVersion(ctx, pq.InsertConfigVersionParams{ConfigID: cfg.ID, Version: int64(i + 1), Value: value, CreatedAt: now + int64(i), CreatedBy: 7})
		if err != nil {
			t.Fatal(err)
		}
		rowIDs = append(rowIDs, v.ID)
	}

	pinnedID := int32(rowIDs[0])
	spec := nonEmptySpec()
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"LOG_LEVEL": {ConfigRefID: &pinnedID},
	}
	if _, err := q.CreateDeploymentConfig(ctx, pq.CreateDeploymentConfigParams{
		NodeID: 1, SpaceID: 1, Name: "app", CreatedAt: now, Version: 1, UpdatedAt: now, UpdatedBy: 0, SpecBlob: spec.Encode(), Deleted: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	s := Open(path)
	defer s.Close()

	meta, ok := s.GetConfigMeta(int32(cfg.ID))
	if !ok {
		t.Fatal("config missing after migration")
	}
	if len(meta.VersionRefs) != 2 {
		t.Fatalf("version refs = %d, want 2 (latest + pinned)", len(meta.VersionRefs))
	}
	if meta.VersionRefs[0].Value != "warn" || meta.VersionRefs[1].Value != "debug" {
		t.Fatalf("imported values = %q, %q", meta.VersionRefs[0].Value, meta.VersionRefs[1].Value)
	}

	rows, err := s.q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("deployments = %d", len(rows))
	}
	migrated, err := apigen.DecodeDeploymentSpec(rows[0].SpecBlob)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Version != 1 {
		t.Fatalf("deployment version bumped to %d", rows[0].Version)
	}
	env := migrated.Container().Runtime.EnvVars["LOG_LEVEL"]
	if env.ConfigRef == nil || env.ConfigRef.ID != int32(cfg.ID) {
		t.Fatalf("config ref = %+v", env.ConfigRef)
	}
	for _, old := range rowIDs {
		if env.ConfigRef.Version <= old {
			t.Fatalf("seq %d collides with legacy version row id space (max %d)", env.ConfigRef.Version, rowIDs[len(rowIDs)-1])
		}
	}
	if env.ConfigRefID == nil || int64(*env.ConfigRefID) != env.ConfigRef.Version {
		t.Fatalf("deprecated field %v != ref version %d", env.ConfigRefID, env.ConfigRef.Version)
	}
	value, ok := s.ResolveConfig(*env.ConfigRefID)
	if !ok || value != "debug" {
		t.Fatalf("pinned resolve = %q ok=%v", value, ok)
	}

	if _, done := s.FetchLocalKV(configsEventLogMarker); !done {
		t.Fatal("marker not set")
	}
}
