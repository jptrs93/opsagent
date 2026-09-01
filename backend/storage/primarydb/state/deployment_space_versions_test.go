package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentSpaceVersionsAndPlacementPins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := store.EnsurePrimaryNode("primary", "primary-id")
	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, DefaultSpaceID, "web", node.ID, nonEmptySpec())
	if cfg.SpaceVersion != 1 {
		t.Fatalf("created config space version = %d, want 1", cfg.SpaceVersion)
	}

	inst := store.CreateScheduledInstanceForTest(cfg.ID, cfg.Version, cfg.Def.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if inst.SpaceID != DefaultSpaceID {
		t.Fatalf("placement pin = space %d, want space %d", inst.SpaceID, DefaultSpaceID)
	}

	author9 := apigen.Context{User: &apigen.InternalUser{ID: 9}}
	moved := updateDeployment(store, author9, cfg.ID, DeploymentUpdate{SpaceID: i32ptr(2)})
	if moved.Def.SpaceID != 2 || moved.SpecVersion != cfg.SpecVersion || moved.SpaceVersion != 2 {
		t.Fatalf("moved config = v%d space %d spaceV%d, want v%d space 2 spaceV2 (no spec version bump)",
			moved.SpecVersion, moved.Def.SpaceID, moved.SpaceVersion, cfg.SpecVersion)
	}
	if same := updateDeployment(store, author9, cfg.ID, DeploymentUpdate{SpaceID: i32ptr(2)}); same.SpaceVersion != 2 {
		t.Fatalf("same-space move = spaceV%d, want no-op at spaceV2", same.SpaceVersion)
	}
	events, err := store.q.ListDeploymentEvents(t.Context(), int64(cfg.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].SpaceAssignmentVersion != 1 || events[1].SpaceAssignmentVersion != 2 ||
		events[1].Author != 9 || events[1].SpecVersion != 1 {
		t.Fatalf("event log = %+v, want create at spaceV1 then move to spaceV2 author 9 with no spec bump", events)
	}
	if first := deploymentFromRow(events[0]); first.Def.SpaceID != DefaultSpaceID {
		t.Fatalf("create snapshot space = %d, want %d", first.Def.SpaceID, DefaultSpaceID)
	}
	if second := deploymentFromRow(events[1]); second.Def.SpaceID != 2 {
		t.Fatalf("move snapshot space = %d, want 2", second.Def.SpaceID)
	}

	if st := findInstanceState(t, store, inst.ID); st.Config.Def.SpaceID != DefaultSpaceID {
		t.Fatalf("pinned view after move = space %d, want space %d", st.Config.Def.SpaceID, DefaultSpaceID)
	}

	replacement, created := store.EnsureRunScheduledInstance(cfg.ID, moved.Version, cfg.Def.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	if !created {
		t.Fatal("space move did not require a replacement incarnation")
	}
	if replacement.SpaceID != 2 {
		t.Fatalf("replacement pin = space %d, want space 2", replacement.SpaceID)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = Open(dbPath)
	defer store.Close()
	if cur := store.FetchDeployment(cfg.ID); cur.Def.SpaceID != 2 || cur.SpaceVersion != 2 {
		t.Fatalf("reloaded config = space %d spaceV%d, want space 2 spaceV2", cur.Def.SpaceID, cur.SpaceVersion)
	}
	if st := findInstanceState(t, store, inst.ID); st.Config.Def.SpaceID != DefaultSpaceID {
		t.Fatalf("reloaded pinned view = space %d, want space %d", st.Config.Def.SpaceID, DefaultSpaceID)
	}
	if st := findInstanceState(t, store, replacement.ID); st.Config.Def.SpaceID != 2 {
		t.Fatalf("reloaded replacement view = space %d, want space 2", st.Config.Def.SpaceID)
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
	node := store.EnsurePrimaryNode("primary", "primary-id")

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

	cfg := mustCreateDeploymentForNode(store, apigen.Context{}, DefaultSpaceID, "envy", node.ID, envSpec())

	updated := updateDeployment(store, apigen.Context{}, cfg.ID, DeploymentUpdate{Spec: envSpec()})
	if updated.SpecVersion != cfg.SpecVersion || updated.Version != cfg.Version {
		t.Fatalf("same-spec update = v%d specV%d, want no-op at specV%d", updated.Version, updated.SpecVersion, cfg.SpecVersion)
	}

	for i, target := range []int32{2, DefaultSpaceID, 2, DefaultSpaceID} {
		moved := updateDeployment(store, apigen.Context{}, cfg.ID, DeploymentUpdate{SpaceID: i32ptr(target)})
		if moved.SpecVersion != cfg.SpecVersion {
			t.Fatalf("move %d bumped spec version to %d, want %d", i, moved.SpecVersion, cfg.SpecVersion)
		}
	}
}
