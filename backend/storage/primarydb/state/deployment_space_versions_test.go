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

	inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0,
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
	if moved.SpaceID != 2 || moved.Version != cfg.Version || moved.SpaceVersion != 2 {
		t.Fatalf("moved config = v%d space %d spaceV%d, want v%d space 2 spaceV2 (no config version bump)",
			moved.Version, moved.SpaceID, moved.SpaceVersion, cfg.Version)
	}
	if same, err := store.MoveDeploymentSpace(cfg.ID, 2, moved.SpaceVersion+1, 9); err != nil || same.SpaceVersion != 2 {
		t.Fatalf("same-space move = spaceV%d err %v, want no-op at spaceV2", same.SpaceVersion, err)
	}
	rows, err := store.q.ListDeploymentSpaceVersionsByDeploymentID(t.Context(), int64(cfg.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SpaceID != int64(DefaultSpaceID) || rows[0].Version != 1 ||
		rows[1].SpaceID != 2 || rows[1].Version != 2 || rows[1].Author != 9 {
		t.Fatalf("space version log = %+v, want v1 space %d then v2 space 2 author 9", rows, DefaultSpaceID)
	}

	if st := findInstanceState(t, store, inst.ID); st.Config.SpaceID != DefaultSpaceID {
		t.Fatalf("pinned view after move = space %d, want space %d", st.Config.SpaceID, DefaultSpaceID)
	}

	replacement, created := store.EnsureRunScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0,
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
