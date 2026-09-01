package state

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentSpaceVersionsAndPlacementPins(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := store.EnsurePrimaryNode("primary", "primary-id")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "web", node.ID, nonEmptySpec())
	if cfg.SpaceVersion != 1 {
		t.Fatalf("created config space version = %d, want 1", cfg.SpaceVersion)
	}

	inst := store.CreateScheduledInstanceForTest(cfg.ID, cfg.SpecVersion, cfg.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	if inst.SpaceID != DefaultSpaceID {
		t.Fatalf("placement pin = space %d, want space %d", inst.SpaceID, DefaultSpaceID)
	}

	if _, err := store.MoveDeploymentSpace(cfg.ID, 2, cfg.SpaceVersion, 9); !errors.Is(err, SpaceVersionMismatchErr) {
		t.Fatalf("stale space version err = %v, want %v", err, SpaceVersionMismatchErr)
	}
	moved, err := store.MoveDeploymentSpace(cfg.ID, 2, cfg.SpaceVersion+1, 9)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.SpaceID != 2 || moved.SpecVersion != cfg.SpecVersion || moved.SpaceVersion != 2 {
		t.Fatalf("moved config = v%d space %d spaceV%d, want v%d space 2 spaceV2 (no spec version bump)",
			moved.SpecVersion, moved.SpaceID, moved.SpaceVersion, cfg.SpecVersion)
	}
	if same, err := store.MoveDeploymentSpace(cfg.ID, 2, moved.SpaceVersion+1, 9); err != nil || same.SpaceVersion != 2 {
		t.Fatalf("same-space move = spaceV%d err %v, want no-op at spaceV2", same.SpaceVersion, err)
	}
	events, err := store.q.ListDeploymentEvents(t.Context(), int64(cfg.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].SpaceAssignmentVersion != 1 || events[1].SpaceAssignmentVersion != 2 ||
		events[1].Author != 9 || events[1].SpecVersion != 1 {
		t.Fatalf("event log = %+v, want create at spaceV1 then move to spaceV2 author 9 with no spec bump", events)
	}
	if first := deploymentEventToProto(events[0]); first.SpaceID != DefaultSpaceID {
		t.Fatalf("create snapshot space = %d, want %d", first.SpaceID, DefaultSpaceID)
	}
	if second := deploymentEventToProto(events[1]); second.SpaceID != 2 {
		t.Fatalf("move snapshot space = %d, want 2", second.SpaceID)
	}

	if st := findInstanceState(t, store, inst.ID); st.Config.SpaceID != DefaultSpaceID {
		t.Fatalf("pinned view after move = space %d, want space %d", st.Config.SpaceID, DefaultSpaceID)
	}

	replacement, created := store.EnsureRunScheduledInstance(cfg.ID, cfg.SpecVersion, cfg.NodeID, 0,
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
	if cur := store.FetchDeployment(cfg.ID); cur.SpaceID != 2 || cur.SpaceVersion != 2 {
		t.Fatalf("reloaded config = space %d spaceV%d, want space 2 spaceV2", cur.SpaceID, cur.SpaceVersion)
	}
	if st := findInstanceState(t, store, inst.ID); st.Config.SpaceID != DefaultSpaceID {
		t.Fatalf("reloaded pinned view = space %d, want space %d", st.Config.SpaceID, DefaultSpaceID)
	}
	if st := findInstanceState(t, store, replacement.ID); st.Config.SpaceID != 2 {
		t.Fatalf("reloaded replacement view = space %d, want space 2", st.Config.SpaceID)
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

// Spec equality must be structural: env vars are a proto map field and encode
// in Go map iteration order, so byte-comparing two encodes of the same spec
// falsely reports a change. A same-spec update must stay a no-op and a space
// move must not observe (or assert on) a phantom spec change.
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

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, DefaultSpaceID, "envy", node.ID, envSpec())

	updated, changed, versionOK := store.UpdateDeploymentSpec(apigen.Context{}, cfg.ID, DeploymentSpecUpdate{
		ExpectedSpecVersion: cfg.SpecVersion + 1,
		Spec:                envSpec(),
	})
	if changed || !versionOK || updated.SpecVersion != cfg.SpecVersion {
		t.Fatalf("same-spec update = specV%d changed=%v ok=%v, want no-op at specV%d", updated.SpecVersion, changed, versionOK, cfg.SpecVersion)
	}

	current := cfg
	for i, target := range []int32{2, DefaultSpaceID, 2, DefaultSpaceID} {
		moved, err := store.MoveDeploymentSpace(cfg.ID, target, current.SpaceVersion+1, 0)
		if err != nil {
			t.Fatalf("move %d: %v", i, err)
		}
		if moved.SpecVersion != cfg.SpecVersion {
			t.Fatalf("move %d bumped spec version to %d, want %d", i, moved.SpecVersion, cfg.SpecVersion)
		}
		current = moved
	}
}
