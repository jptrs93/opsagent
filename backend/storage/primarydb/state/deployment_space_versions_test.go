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
	if cfg.SpaceVersion != 1 {
		t.Fatalf("created config space version = %d, want 1", cfg.SpaceVersion)
	}

	inst := createScheduledInstanceForTest(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if inst.SpaceID != defaultSpaceID {
		t.Fatalf("placement pin = space %d, want space %d", inst.SpaceID, defaultSpaceID)
	}

	author9 := apigen.Context{User: &apigen.InternalUser{ID: 9}}
	moved := moveDeploymentSpace(store, author9, cfg.DeploymentID, 2)
	if moved.Value.SpaceID != 2 || moved.SpecVersion != cfg.SpecVersion || moved.SpaceVersion != 2 {
		t.Fatalf("moved config = v%d space %d spaceV%d, want v%d space 2 spaceV2 (no spec version bump)",
			moved.SpecVersion, moved.Value.SpaceID, moved.SpaceVersion, cfg.SpecVersion)
	}
	events, err := store.q.ListDeploymentEvents(t.Context(), int64(cfg.DeploymentID))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].SpaceVersion != 1 || events[1].SpaceVersion != 2 ||
		events[1].Author != 9 || events[1].SpecVersion != 1 {
		t.Fatalf("event log = %+v, want create at spaceV1 then move to spaceV2 author 9 with no spec bump", events)
	}
	if first := events[0]; first.Value.SpaceID != defaultSpaceID {
		t.Fatalf("create snapshot space = %d, want %d", first.Value.SpaceID, defaultSpaceID)
	}
	if second := events[1]; second.Value.SpaceID != 2 {
		t.Fatalf("move snapshot space = %d, want 2", second.Value.SpaceID)
	}

	if st := findInstanceState(t, store, inst.ID); st.Config.Value.SpaceID != defaultSpaceID {
		t.Fatalf("pinned view after move = space %d, want space %d", st.Config.Value.SpaceID, defaultSpaceID)
	}

	replacement := createScheduledInstanceForTest(store, cfg.DeploymentID, moved.Version, cfg.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	if replacement.SpaceID != 2 {
		t.Fatalf("replacement pin = space %d, want space 2", replacement.SpaceID)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	defer store.Close()
	if cur := fetchDeploymentForTest(store, cfg.DeploymentID); cur.Value.SpaceID != 2 || cur.SpaceVersion != 2 {
		t.Fatalf("reloaded config = space %d spaceV%d, want space 2 spaceV2", cur.Value.SpaceID, cur.SpaceVersion)
	}
	if st := findInstanceState(t, store, inst.ID); st.Config.Value.SpaceID != defaultSpaceID {
		t.Fatalf("reloaded pinned view = space %d, want space %d", st.Config.Value.SpaceID, defaultSpaceID)
	}
	if st := findInstanceState(t, store, replacement.ID); st.Config.Value.SpaceID != 2 {
		t.Fatalf("reloaded replacement view = space %d, want space 2", st.Config.Value.SpaceID)
	}
}

func findInstanceState(t *testing.T, store *Service, instanceID int32) apigen.ScheduledInstanceState {
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
		env := make(map[string]*apigen.EnvVarValue)
		for _, key := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
			value := "value-" + key
			env[key] = &apigen.EnvVarValue{Value: &value}
		}
		spec.Container1Spec.Runtime.EnvVars = env
		return spec
	}

	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, defaultSpaceID, "envy", node.ID, envSpec())

	updated := updateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, envSpec())
	if updated.SpecVersion != cfg.SpecVersion {
		t.Fatalf("same-spec update = v%d specV%d, want no spec bump from specV%d", updated.Version, updated.SpecVersion, cfg.SpecVersion)
	}

	for i, target := range []int32{2, defaultSpaceID, 2, defaultSpaceID} {
		moved := moveDeploymentSpace(store, apigen.Context{}, cfg.DeploymentID, target)
		if moved.SpecVersion != cfg.SpecVersion {
			t.Fatalf("move %d bumped spec version to %d, want %d", i, moved.SpecVersion, cfg.SpecVersion)
		}
	}
}
