package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func legacyFlatState(instanceID, deploymentID int32) *apigen.ScheduledInstanceState {
	return &apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:                    instanceID,
			NodeID:                23,
			DeploymentID:          deploymentID,
			DeploymentSpecVersion: 3,
			State:                 apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: apigen.Deployment{
			ID:              deploymentID,
			SpecVersion:     3,
			NodeID:          23,
			SpaceID:         1,
			Name:            "api",
			Spec:            *testSpecWithState("v3", true),
			LegacyCreatedAt: 1000,
			LegacyUpdatedAt: 2000,
		},
	}
}

func assertFolded(t *testing.T, cfg *apigen.Deployment) {
	t.Helper()
	if cfg.Def.NodeID != 23 || cfg.Def.SpaceID != 1 || cfg.Def.Name != "api" || cfg.Def.Spec.WorkloadVersion() != "v3" {
		t.Fatalf("def not synthesized from legacy flat fields: %+v", cfg)
	}
	if cfg.CreatedTime.UnixMilli() != 1000 || cfg.EventTime.UnixMilli() != 2000 {
		t.Fatalf("times not folded: %d/%d", cfg.CreatedTime.UnixMilli(), cfg.EventTime.UnixMilli())
	}
}

func TestSecondaryFoldsLegacyAssignmentOnBoot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	store := Open(dbPath)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`INSERT INTO local_scheduled_instance_cache (instance_id, blob) VALUES (?, ?)`,
		11, legacyFlatState(11, 7).Encode()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store = Open(dbPath)
	defer store.Close()
	got, _, unsub := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsub()
	if len(got) != 1 {
		t.Fatalf("expected 1 scheduled instance, got %d", len(got))
	}
	assertFolded(t, &got[0].Config)
}

func TestSecondaryFoldsLegacyAssignmentFromStream(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "secondary.db"))
	defer store.Close()

	state := legacyFlatState(12, 8)
	if decoded, err := apigen.DecodeScheduledInstanceState(state.Encode()); err != nil {
		t.Fatal(err)
	} else {
		state = decoded
	}
	if !state.Config.Def.IsZero() {
		t.Fatal("test fixture should decode with a zero def")
	}
	store.MustWriteScheduledInstanceAssignment(state)

	got, _, unsub := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsub()
	if len(got) != 1 {
		t.Fatalf("expected 1 scheduled instance, got %d", len(got))
	}
	assertFolded(t, &got[0].Config)
}
