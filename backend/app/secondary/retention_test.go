package secondary

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/secondary/localinputs"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

func retentionTestStore(t *testing.T) (*state.Service, *runtimeinputs.RuntimeInputs) {
	t.Helper()
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "secondary.db"))
	persistence, err := localinputs.Open(context.Background(), store, &machinekey.File{Path: filepath.Join(dir, machinekey.FileName)})
	if err != nil {
		t.Fatalf("localinputs.Open: %v", err)
	}
	inputs, err := runtimeinputs.NewPersistent(nil, nil, nil, persistence)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if err := persistence.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(1): "kept", vr(2): "dropped"}, map[apigen.ValueRef]string{vr(3): "dropped"}); err != nil {
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
func referencingConfig(version uint32) apigen.DeploymentRecord {
	return apigen.DeploymentRecord{
		Deployment: apigen.Deployment{
			ID:         7,
			Scheduling: apigen.DedicatedScheduling(false, 23),
			SpaceID:    1,
			Name:       "api",
			Spec: apigen.DeploymentSpec{Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
				Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
				UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
				Runtime: apigen.ContainerRuntime{
					AssetMounts: []apigen.AssetMount{{Asset: vr(4).Asset(), ContainerPath: "/srv/asset", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY}},
					EnvVars:     map[string]apigen.EnvVar{"TOKEN": {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: 1, Version: 1}}}}},
				},
			}}}, Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST}},
		},
		Meta: apigen.EntityMeta{Version: version, SpecVersion: version},
	}
}

func writeInstance(t *testing.T, store *state.Service, instanceID uint64, cfg apigen.DeploymentRecord, target apigen.ScheduledInstanceTarget, preparerVersion, runnerVersion uint32) {
	t.Helper()
	store.MustApplyAssignments([]apigen.ScheduledInstanceState{{
		Instance: apigen.ScheduledInstance{
			ID:         instanceID,
			NodeID:     23,
			Deployment: apigen.DeploymentRef{DeploymentID: cfg.Deployment.ID, Version: cfg.Meta.Version},
			State:      target,
		},
		Config: cfg,
	}}, nil, nil)
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Preparer = apigen.Some(apigen.PreparerStatus{DeploymentSpecVersion: preparerVersion, Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY)})
		s.Runner = apigen.Some(apigen.RunnerStatus{DeploymentSpecVersion: runnerVersion, Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING})
		return true
	})
}

func TestSweepDropsOnlyUnreferencedInputsAndAssets(t *testing.T) {
	store, inputs := retentionTestStore(t)
	assetDir := withRetentionAssetDir(t, "4@1", "9@1")
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 3, 3)

	sweepRuntimeInputs(context.Background(), store, inputs, nil, nil)

	if _, ok := inputs.ResolveSecret(vr(1)); !ok {
		t.Fatal("referenced secret 1 was dropped")
	}
	if _, ok := inputs.ResolveSecret(vr(2)); ok {
		t.Fatal("unreferenced secret 2 survived")
	}
	if _, ok := inputs.ResolveConfig(vr(3)); ok {
		t.Fatal("unreferenced config 3 survived")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "4@1")); err != nil {
		t.Fatalf("referenced asset 4 was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assetDir, "9@1")); !os.IsNotExist(err) {
		t.Fatal("unreferenced asset 9 survived")
	}
}

// An instance whose runner still trails the desired spec version is running a
// config whose referenced ids this node can no longer enumerate, so sweeping
// could delete an input its live container needs to respawn. The sweep must wait
// — and because ids are shared between deployments, it has to wait for all of
// them, not just the one that is mid-rollout.
func TestSweepSkipsEntirelyWhileAnyInstanceIsMidRollout(t *testing.T) {
	store, inputs := retentionTestStore(t)
	assetDir := withRetentionAssetDir(t, "4@1", "9@1")
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 3, 3)
	// A second instance mid-rollout: prepared at v4, still running v3.
	other := referencingConfig(4)
	other.Deployment.ID = 8
	other.Deployment.Name = "secondary"
	writeInstance(t, store, 12, other, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, 4, 3)

	sweepRuntimeInputs(context.Background(), store, inputs, nil, nil)

	if _, ok := inputs.ResolveSecret(vr(2)); !ok {
		t.Fatal("swept while an instance was mid-rollout")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "9@1")); err != nil {
		t.Fatalf("swept cached assets while an instance was mid-rollout: %v", err)
	}
}

// An instance being torn down is not going to start a new container, so it
// cannot be holding a previous spec version's inputs open and must not block
// the sweep for as long as it lingers.
func TestSweepTreatsTerminatingInstancesAsSettled(t *testing.T) {
	store, inputs := retentionTestStore(t)
	withRetentionAssetDir(t)
	// Deliberately trailing versions: for a terminating instance they say nothing.
	writeInstance(t, store, 11, referencingConfig(3), apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE, 1, 1)

	sweepRuntimeInputs(context.Background(), store, inputs, nil, nil)

	if _, ok := inputs.ResolveSecret(vr(2)); ok {
		t.Fatal("stopped instance blocked the sweep")
	}
	if _, ok := inputs.ResolveSecret(vr(1)); !ok {
		t.Fatal("a stopped instance stopped contributing its own refs")
	}
}

func vr(id uint64) apigen.ValueRef { return apigen.ValueRef{ID: id, Version: 1} }
