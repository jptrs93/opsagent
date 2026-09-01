package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentVersionBackfillMatchesSpecAndSpace(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := store.EnsurePrimaryNode("primary", "primary-id")
	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, DefaultSpaceID, "web", node.ID, testSpecWithState("v1", true))
	updated := updateDeployment(store, apigen.Context{}, cfg.ID, DeploymentUpdate{Spec: testSpecWithState("v2", true)})
	moved := updateDeployment(store, apigen.Context{}, cfg.ID, DeploymentUpdate{SpaceID: i32ptr(2)})
	if updated.Version != 2 || moved.Version != 3 || moved.SpecVersion != updated.SpecVersion {
		t.Fatalf("fixture events wrong: updated=%+v moved=%+v", updated, moved)
	}

	preMove := store.CreateScheduledInstanceForTest(cfg.ID, updated.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	postMove := store.CreateScheduledInstanceForTest(cfg.ID, moved.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db := sqlitedb.MustOpen(dbPath)
	if _, err := db.Exec(`UPDATE scheduled_instances SET deployment_version = 0`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store = Open(dbPath)
	defer store.Close()
	if st := findInstanceState(t, store, preMove.ID); st.Instance.DeploymentVersion != updated.Version ||
		st.Config.Def.SpaceID != DefaultSpaceID {
		t.Fatalf("pre-move instance restamped to v%d space %d, want v%d space %d",
			st.Instance.DeploymentVersion, st.Config.Def.SpaceID, updated.Version, DefaultSpaceID)
	}
	if st := findInstanceState(t, store, postMove.ID); st.Instance.DeploymentVersion != moved.Version ||
		st.Config.Def.SpaceID != 2 {
		t.Fatalf("post-move instance restamped to v%d space %d, want v%d space 2",
			st.Instance.DeploymentVersion, st.Config.Def.SpaceID, moved.Version)
	}
}
