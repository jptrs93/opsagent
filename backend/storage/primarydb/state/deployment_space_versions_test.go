package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentSpaceVersionsAndPlacementPins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := testNode(store, "primary-id")
	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, defaultSpaceID, "web", node.ID, nonEmptySpec())

	inst := createScheduledInstanceForTest(store, cfg.Deployment.ID, cfg.Meta.Version, cfg.Deployment.PlacementNodeID(), 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if inst.SpaceID != defaultSpaceID {
		t.Fatalf("placement pin = space %d, want space %d", inst.SpaceID, defaultSpaceID)
	}

	author9 := apigen.Context{User: &apigen.User{ID: 9}}
	moved := moveDeploymentSpace(store, author9, cfg.Deployment.ID, 2)
	if moved.Deployment.SpaceID != 2 || moved.Meta.SpecVersion != cfg.Meta.SpecVersion || moved.Meta.Version != 2 {
		t.Fatalf("moved config = v%d specV%d space %d, want v2 specV%d space 2 (no spec version bump)",
			moved.Meta.Version, moved.Meta.SpecVersion, moved.Deployment.SpaceID, cfg.Meta.SpecVersion)
	}
	history, err := store.q.ListDeploymentHistory(t.Context(), cfg.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Meta.Version != 1 || history[1].Meta.Version != 2 || history[0].Deleted() || history[1].Deleted() ||
		history[1].Meta.UpdatedActor != 9 || history[1].Meta.SpecVersion != 1 || history[1].Meta.UpdatedSeq != moved.Meta.UpdatedSeq {
		t.Fatalf("event log = %+v, want create at v1 then move to v2 author 9 with no spec bump", history)
	}
	if first := history[0]; first.Deployment.SpaceID != defaultSpaceID {
		t.Fatalf("create snapshot space = %d, want %d", first.Deployment.SpaceID, defaultSpaceID)
	}
	if second := history[1]; second.Deployment.SpaceID != 2 {
		t.Fatalf("move snapshot space = %d, want 2", second.Deployment.SpaceID)
	}

	if st := findInstanceState(t, store, inst.ID); st.Config.Deployment.SpaceID != defaultSpaceID {
		t.Fatalf("pinned view after move = space %d, want space %d", st.Config.Deployment.SpaceID, defaultSpaceID)
	}

	replacement := createScheduledInstanceForTest(store, cfg.Deployment.ID, moved.Meta.Version, cfg.Deployment.PlacementNodeID(), 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	if replacement.SpaceID != 2 {
		t.Fatalf("replacement pin = space %d, want space 2", replacement.SpaceID)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	defer store.Close()
	if cur := fetchDeploymentForTest(store, cfg.Deployment.ID); cur.Deployment.SpaceID != 2 || cur.Meta.Version != 2 {
		t.Fatalf("reloaded config = space %d v%d, want space 2 v2", cur.Deployment.SpaceID, cur.Meta.Version)
	}
	if st := findInstanceState(t, store, inst.ID); st.Config.Deployment.SpaceID != defaultSpaceID {
		t.Fatalf("reloaded pinned view = space %d, want space %d", st.Config.Deployment.SpaceID, defaultSpaceID)
	}
	if st := findInstanceState(t, store, replacement.ID); st.Config.Deployment.SpaceID != 2 {
		t.Fatalf("reloaded replacement view = space %d, want space 2", st.Config.Deployment.SpaceID)
	}
}

func findInstanceState(t *testing.T, store *Service, instanceID uint64) apigen.ScheduledInstanceState {
	t.Helper()
	for _, st := range store.FetchScheduledSnapshot(nil) {
		if st.Instance.ID == instanceID {
			return st
		}
	}
	t.Fatalf("scheduled instance %d not found", instanceID)
	return apigen.ScheduledInstanceState{}
}

func TestSpecComparisonSurvivesMapEncodingOrder(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	node := testNode(store, "primary-id")

	envSpec := func() *apigen.DeploymentSpec {
		spec := nonEmptySpec()
		env := make(map[string]apigen.EnvVar)
		for _, key := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
			env[key] = apigen.EnvVar{Value: apigen.EnvVarValueOneof{Literal: &apigen.LiteralEnv{Value: "value-" + key}}}
		}
		spec.Container().Runtime.EnvVars = env
		return spec
	}

	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, defaultSpaceID, "envy", node.ID, envSpec())

	updated := updateDeploymentSpec(store, apigen.Context{}, cfg.Deployment.ID, envSpec())
	if updated.Meta.SpecVersion != cfg.Meta.SpecVersion {
		t.Fatalf("same-spec update = v%d specV%d, want no spec bump from specV%d", updated.Meta.Version, updated.Meta.SpecVersion, cfg.Meta.SpecVersion)
	}

	for i, target := range []uint64{2, defaultSpaceID, 2, defaultSpaceID} {
		moved := moveDeploymentSpace(store, apigen.Context{}, cfg.Deployment.ID, target)
		if moved.Meta.SpecVersion != cfg.Meta.SpecVersion {
			t.Fatalf("move %d bumped spec version to %d, want %d", i, moved.Meta.SpecVersion, cfg.Meta.SpecVersion)
		}
	}
}
