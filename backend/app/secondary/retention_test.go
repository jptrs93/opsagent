package secondary

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/localinputs"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func retentionTestStore(t *testing.T) (*sqlite.SecondaryStorage, *runtimeinputs.RuntimeInputs) {
	t.Helper()
	dir := t.TempDir()
	store := sqlite.NewSecondaryStorage(filepath.Join(dir, "secondary.db"))
	persistence, err := localinputs.Open(store, &machinekey.File{Path: filepath.Join(dir, machinekey.FileName)})
	if err != nil {
		t.Fatalf("localinputs.Open: %v", err)
	}
	inputs, err := runtimeinputs.NewPersistent(nil, nil, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if err := persistence.StoreRuntimeInputs(map[int32]string{1: "kept", 2: "dropped"}, map[int32]string{3: "dropped"}); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}
	// Reload so the in-memory maps match what is on disk.
	inputs, err = runtimeinputs.NewPersistent(nil, nil, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	return store, inputs
}

func withRetentionAssetDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	previous := ainit.StaticConfig.AssetCacheDir
	ainit.StaticConfig.AssetCacheDir = dir
	t.Cleanup(func() { ainit.StaticConfig.AssetCacheDir = previous })
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	return dir
}

// referencing builds a config that references secret 1 and asset 4, matching the
// values seeded by retentionTestStore and withRetentionAssetDir.
func referencingConfig(version int32) apigen.DeploymentConfig {
	return apigen.DeploymentConfig{
		ID:       7,
		NodeID:   23,
		Version:  version,
		Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
		Spec: apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
			Runtime: apigen.ContainerRuntime{
				AssetMounts: []*apigen.AssetMount{{AssetVersionID: 4}},
				EnvVars:     map[string]*apigen.EnvVarValue{"TOKEN": {SecretVersionID: ptrInt32(1)}},
			},
		}},
	}
}

func writeInstance(t *testing.T, store *sqlite.SecondaryStorage, instanceID int32, cfg apigen.DeploymentConfig, target apigen.ScheduledInstanceTarget, preparerVersion, runnerVersion int32) {
	t.Helper()
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:                instanceID,
			NodeID:            23,
			DeploymentID:      cfg.ID,
			DeploymentVersion: cfg.Version,
			State:             target,
		},
		Config: cfg,
	})
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Preparer = apigen.PreparerStatus{DeploymentConfigVersion: preparerVersion, Inputs: apigen.InputsStatus_INPUTS_READY, Image: apigen.ImageStatus_IMAGE_READY}
		s.Runner = apigen.RunnerStatus{DeploymentConfigVersion: runnerVersion, Status: apigen.RunningStatus_RUNNING}
		return true
	})
}

func TestSweepDropsOnlyUnreferencedInputsAndAssets(t *testing.T) {
	store, inputs := retentionTestStore(t)
	assetDir := withRetentionAssetDir(t, "4", "9")
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 3, 3)

	sweepRuntimeInputs(context.Background(), store, inputs, nil)

	if _, ok := inputs.ResolveSecret(1); !ok {
		t.Fatal("referenced secret 1 was dropped")
	}
	if _, ok := inputs.ResolveSecret(2); ok {
		t.Fatal("unreferenced secret 2 survived")
	}
	if _, ok := inputs.ResolveConfig(3); ok {
		t.Fatal("unreferenced config 3 survived")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "4")); err != nil {
		t.Fatalf("referenced asset 4 was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assetDir, "9")); !os.IsNotExist(err) {
		t.Fatal("unreferenced asset 9 survived")
	}
}

// An instance whose runner still trails the desired config version is running a
// config whose referenced ids this node can no longer enumerate, so sweeping
// could delete an input its live container needs to respawn. The sweep must wait
// — and because ids are shared between deployments, it has to wait for all of
// them, not just the one that is mid-rollout.
func TestSweepSkipsEntirelyWhileAnyInstanceIsMidRollout(t *testing.T) {
	store, inputs := retentionTestStore(t)
	assetDir := withRetentionAssetDir(t, "4", "9")
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 3, 3)
	// A second instance mid-rollout: prepared at v4, still running v3.
	other := referencingConfig(4)
	other.ID = 8
	other.Identity.Name = "worker"
	writeInstance(t, store, 12, other, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 4, 3)

	sweepRuntimeInputs(context.Background(), store, inputs, nil)

	if _, ok := inputs.ResolveSecret(2); !ok {
		t.Fatal("swept while an instance was mid-rollout")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "9")); err != nil {
		t.Fatalf("swept cached assets while an instance was mid-rollout: %v", err)
	}
}

// An instance being torn down is not going to start a new container, so it
// cannot be holding a previous config version's inputs open and must not block
// the sweep for as long as it lingers.
func TestSweepTreatsTerminatingInstancesAsSettled(t *testing.T) {
	store, inputs := retentionTestStore(t)
	withRetentionAssetDir(t)
	// Deliberately trailing versions: for a terminating instance they say nothing.
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE, 1, 1)

	sweepRuntimeInputs(context.Background(), store, inputs, nil)

	if _, ok := inputs.ResolveSecret(2); ok {
		t.Fatal("stopped instance blocked the sweep")
	}
	if _, ok := inputs.ResolveSecret(1); !ok {
		t.Fatal("a stopped instance stopped contributing its own refs")
	}
}

func ptrInt32(v int32) *int32 { return &v }
