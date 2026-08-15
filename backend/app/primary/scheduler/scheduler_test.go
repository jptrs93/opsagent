package scheduler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// fakeBarrier stands in for the applied-sequence barrier. Held blocks every
// wait, which is how tests distinguish "drained and retired" from "draining and
// still waiting for the cluster to catch up".
type fakeBarrier struct {
	sequence int64
	held     bool
	acks     chan struct{}
}

func newFakeBarrier() *fakeBarrier {
	return &fakeBarrier{acks: make(chan struct{}, 1)}
}

func (b *fakeBarrier) CurrentSequence() int64                { return b.sequence }
func (b *fakeBarrier) AppliedEverywhere(sequence int64) bool { return !b.held }
func (b *fakeBarrier) AckUpdates() <-chan struct{}           { return b.acks }

func testRunningSpec(version string) *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{Container1Spec: &apigen.ContainerSpec{
		Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
		Version: version,
		Running: true,
	}}
}

func rolloverSpec(version string) *apigen.DeploymentSpec {
	spec := testRunningSpec(version)
	spec.Container1Spec.UpgradeStrategy = apigen.ContainerUpgradeStrategy_ROLLOVER
	return spec
}

func statesByID(store *state.Service, deploymentID int32) map[int32]apigen.ScheduledInstanceTarget {
	out := map[int32]apigen.ScheduledInstanceTarget{}
	for _, inst := range store.ListNonFinalScheduledInstancesForDeployment(deploymentID) {
		out[inst.ID] = inst.State
	}
	return out
}

func markRunning(t *testing.T, store *state.Service, instanceID, configVersion int32, status apigen.RunningStatus) {
	t.Helper()
	store.MustWriteScheduledInstanceStatus(instanceID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{DeploymentConfigVersion: configVersion, Status: status}
		return true
	})
}

func fetchState(t *testing.T, store *state.Service, instanceID int32) apigen.ScheduledInstanceState {
	t.Helper()
	for _, state := range store.FetchScheduledSnapshot(nil) {
		if state.Instance.ID == instanceID {
			return state
		}
	}
	t.Fatalf("scheduled instance %d not found", instanceID)
	return apigen.ScheduledInstanceState{}
}

func TestDrainSupersededOnlyRetiresOlderInstances(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, testRunningSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	next := *testRunningSpec("v2")
	updated, _, versionOK := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &next,
	})
	if !versionOK {
		t.Fatal("expected config update to succeed")
	}
	newer := store.CreateScheduledInstance(updated.ID, updated.Version, updated.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	barrier := newFakeBarrier()
	barrier.held = true
	s := New(store, barrier)

	// An older instance must not drain the newer replacement.
	s.drainSuperseded(*older)
	byID := statesByID(store, cfg.ID)
	if byID[newer.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("newer instance state = %v, want RUN_SERVING", byID[newer.ID])
	}
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("older instance state = %v, want untouched", byID[older.ID])
	}

	s.drainSuperseded(*newer)
	byID = statesByID(store, cfg.ID)
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("older instance state = %v, want RUN_DRAINING", byID[older.ID])
	}
	if byID[newer.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("newer instance state = %v, want RUN_SERVING", byID[newer.ID])
	}
}

func TestStartupReconcileDoesNotLetOlderRunningKillReplacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, testRunningSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, older.ID, cfg.Version, apigen.RunningStatus_RUNNING)

	next := *testRunningSpec("v2")
	updated, _, versionOK := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &next,
	})
	if !versionOK {
		t.Fatal("expected config update to succeed")
	}

	s := New(store, newFakeBarrier())
	// Match Run()'s startup order: instances first, then configs.
	for _, state := range store.FetchScheduledSnapshot(nil) {
		s.onInstance(state)
	}
	for _, config := range store.FetchDeploymentSnapshot(nil) {
		s.onConfig(config)
	}

	active := store.ListNonFinalScheduledInstancesForDeployment(cfg.ID)
	if len(active) != 1 || active[0].ID != older.ID || active[0].State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("RECREATE first reconciliation = %+v, want only older TERMINATE", active)
	}

	markRunning(t, store, older.ID, cfg.Version, apigen.RunningStatus_STOPPED)
	s.onInstance(fetchState(t, store, older.ID))

	active = store.ListNonFinalScheduledInstancesForDeployment(cfg.ID)
	if len(active) != 1 || active[0].DeploymentVersion != updated.Version ||
		active[0].State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("RECREATE after terminal status = %+v, want replacement RUN_SERVING", active)
	}
}

// TestRolloverReplacementWarmsUpAsStandby is the invariant that keeps a
// cross-node rollover from breaking traffic: a replacement must never be born
// serving, because that would point the instance's inbound route at a node
// whose container does not exist yet.
func TestRolloverReplacementWarmsUpAsStandby(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	next := *rolloverSpec("v2")
	updated, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &next,
	})
	if !ok {
		t.Fatal("expected config update to succeed")
	}

	barrier := newFakeBarrier()
	barrier.held = true
	s := New(store, barrier)
	s.onConfig(*updated)

	byID := statesByID(store, cfg.ID)
	if len(byID) != 2 {
		t.Fatalf("ROLLOVER active instances = %d, want 2", len(byID))
	}
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("older instance state = %v, want still RUN_SERVING", byID[older.ID])
	}
	var replacement int32
	for id, state := range byID {
		if id == older.ID {
			continue
		}
		replacement = id
		if state != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
			t.Fatalf("replacement state = %v, want RUN_STANDBY", state)
		}
	}

	// The standby takes over only once it reports RUNNING against its own config.
	markRunning(t, store, replacement, updated.Version, apigen.RunningStatus_STARTING)
	s.onInstance(fetchState(t, store, replacement))
	if got := statesByID(store, cfg.ID)[replacement]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
		t.Fatalf("STARTING standby state = %v, want still RUN_STANDBY", got)
	}

	markRunning(t, store, replacement, updated.Version, apigen.RunningStatus_RUNNING)
	s.onInstance(fetchState(t, store, replacement))
	byID = statesByID(store, cfg.ID)
	if byID[replacement] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("promoted state = %v, want RUN_SERVING", byID[replacement])
	}
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("superseded state = %v, want RUN_DRAINING", byID[older.ID])
	}
}

// TestFailedRolloutDoesNotAccumulateStandbys covers a rollout that keeps
// failing: a prepare that errors, or a container that never reports ready,
// leaves a standby warming up behind the serving placement. Nothing else retires
// it — drainSuperseded only runs when a replacement reports RUNNING — so before
// this each pushed version added another live placement to the deployment, all
// of them rendered in the UI alongside the one actually being worked on.
func TestFailedRolloutDoesNotAccumulateStandbys(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))
	serving := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, serving.ID, cfg.Version, apigen.RunningStatus_RUNNING)

	barrier := newFakeBarrier()
	barrier.held = true
	s := New(store, barrier)

	push := func(version string) *apigen.DeploymentConfig {
		t.Helper()
		next := *rolloverSpec(version)
		current := store.FetchDeploymentConfig(cfg.ID)
		updated, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
			ExpectedVersion: current.Version + 1,
			Spec:            &next,
		})
		if !ok {
			t.Fatalf("expected config update to %s to succeed", version)
		}
		s.onConfig(*updated)
		return updated
	}

	// The v2 standby's prepare fails, so it never reports RUNNING and its runner
	// status stays empty.
	v2 := push("v2")
	byID := statesByID(store, cfg.ID)
	if len(byID) != 2 {
		t.Fatalf("active instances after the first push = %d, want 2", len(byID))
	}
	var staleStandby int32
	for id := range byID {
		if id != serving.ID {
			staleStandby = id
		}
	}

	// Pushing a fix supersedes the stale standby: it never held the instance
	// address, so it needs no drain and goes straight to TERMINATE.
	push("v3")
	byID = statesByID(store, cfg.ID)
	if len(byID) != 3 {
		t.Fatalf("active instances after the second push = %d, want 3", len(byID))
	}
	if got := byID[staleStandby]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("superseded standby state = %v, want TERMINATE", got)
	}
	if got := byID[serving.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("serving state = %v, want still RUN_SERVING while the rollout retries", got)
	}

	// Once its runner reports terminal, the stale standby finalizes and drops out
	// of the deployment's live placements entirely.
	markRunning(t, store, staleStandby, v2.Version, apigen.RunningStatus_STOPPED)
	s.onInstance(fetchState(t, store, staleStandby))
	byID = statesByID(store, cfg.ID)
	if _, live := byID[staleStandby]; live {
		t.Fatalf("stale standby is still live: %v", byID[staleStandby])
	}
	if len(byID) != 2 {
		t.Fatalf("active instances after finalization = %d, want 2 (serving + the v3 standby)", len(byID))
	}
}

// TestDrainedInstanceWaitsForTheBarrier covers the whole point of the barrier:
// a superseded placement keeps running, and keeps its routes, until every node
// has programmed the routing that replaced it.
func TestDrainedInstanceWaitsForTheBarrier(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	newer := store.CreateScheduledInstance(cfg.ID, cfg.Version+1, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)

	barrier := newFakeBarrier()
	barrier.held = true
	s := New(store, barrier)
	s.drainSuperseded(*newer)
	if got := statesByID(store, cfg.ID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state after supersede = %v, want RUN_DRAINING", got)
	}

	s.retireDrainedInstances()
	if got := statesByID(store, cfg.ID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state while the barrier is held = %v, want still RUN_DRAINING", got)
	}

	barrier.held = false
	s.retireDrainedInstances()
	if got := statesByID(store, cfg.ID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after the barrier cleared = %v, want TERMINATE", got)
	}
	if len(s.draining) != 0 {
		t.Fatalf("retired instance left in the draining set: %+v", s.draining)
	}
}

// TestStandbyPromotedWhenServingDies covers the failure the plain readiness
// handoff cannot: if the serving container stops for good mid-rollover, no
// readiness signal is ever coming, and leaving the inbound address pointed at
// it would blackhole the deployment.
func TestStandbyPromotedWhenServingDies(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	next := *rolloverSpec("v2")
	updated, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &next,
	})
	if !ok {
		t.Fatal("expected config update to succeed")
	}
	standby := store.CreateScheduledInstance(updated.ID, updated.Version, updated.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)

	barrier := newFakeBarrier()
	barrier.held = true
	s := New(store, barrier)

	// The serving placement stops for good while the standby is still warming.
	markRunning(t, store, older.ID, cfg.Version, apigen.RunningStatus_STOPPED)
	s.onInstance(fetchState(t, store, older.ID))

	byID := statesByID(store, cfg.ID)
	if byID[standby.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("standby state = %v, want promoted to RUN_SERVING", byID[standby.ID])
	}
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("failed serving state = %v, want RUN_DRAINING", byID[older.ID])
	}
}

// TestTerminateDeploymentStopsEveryRunnableState guards the state-machine seam:
// a stopped deployment must retire standbys and draining placements too, not
// just the serving one.
func TestTerminateDeploymentStopsEveryRunnableState(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))

	serving := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	standby := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	draining := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)

	New(store, newFakeBarrier()).terminateDeployment(cfg.ID)

	byID := statesByID(store, cfg.ID)
	for name, id := range map[string]int32{"serving": serving.ID, "standby": standby.ID, "draining": draining.ID} {
		if byID[id] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
			t.Fatalf("%s instance state = %v, want TERMINATE", name, byID[id])
		}
	}
}

// restartWith rebuilds the scheduler from what is on disk, exactly as Run does
// on process start: statuses first, then configs.
// restartWith mirrors Run()'s startup order: sweep stopped placements, replay
// instances, then reconcile configs.
func restartWith(store *state.Service, barrier routeBarrier, cfg *apigen.DeploymentConfig) *Scheduler {
	s := New(store, barrier)
	s.finalizeStopped()
	for _, state := range store.FetchScheduledSnapshot(nil) {
		s.onInstance(state)
	}
	s.onConfig(*cfg)
	return s
}

// TestRestartHandlesEveryInstanceState pins the startup reconcile against the
// full target-state set. The draining case is the one with no other owner: the
// wait that retires it lives only in memory, so a restart has to rebuild it or
// the placement runs forever.
func TestRestartHandlesEveryInstanceState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  apigen.ScheduledInstanceTarget
		status apigen.RunningStatus
		want   apigen.ScheduledInstanceTarget
		gone   bool
	}{
		{
			name:   "serving stays serving",
			state:  apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
			status: apigen.RunningStatus_RUNNING,
			want:   apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		{
			name:   "standby that came up while the primary was down takes over",
			state:  apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY,
			status: apigen.RunningStatus_RUNNING,
			want:   apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		{
			name:   "standby still warming keeps warming",
			state:  apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY,
			status: apigen.RunningStatus_STARTING,
			want:   apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY,
		},
		{
			name:   "terminate with a live container waits for it to stop",
			state:  apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE,
			status: apigen.RunningStatus_RUNNING,
			want:   apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
			t.Cleanup(func() { _ = store.Close() })
			node := store.EnsurePrimaryNode("primary", "primary")
			cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
				SpaceID: state.DefaultSpaceID,
				Name:    "app",
			}, node.ID, rolloverSpec("v1"))
			// Instances are always born runnable; non-runnable targets are reached
			// by transition, so build the fixture the same way.
			initial := tc.state
			if !initial.WantsRunning() {
				initial = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
			}
			inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, initial)
			if initial != tc.state {
				store.SetScheduledInstanceState(inst.ID, tc.state)
			}
			markRunning(t, store, inst.ID, cfg.Version, tc.status)

			restartWith(store, newFakeBarrier(), cfg)

			if got := statesByID(store, cfg.ID)[inst.ID]; got != tc.want {
				t.Fatalf("state after restart = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRestartAdoptsDrainingInstances is the leak case: nothing but the in-memory
// wait ever moves a placement out of RUN_DRAINING, and drainSuperseded skips
// anything already draining, so without adoption the container, and its
// published routes, survive every subsequent reconcile.
func TestRestartAdoptsDrainingInstances(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, rolloverSpec("v1"))

	next := *rolloverSpec("v2")
	updated, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1, Spec: &next,
	})
	if !ok {
		t.Fatal("config update failed")
	}
	// Mid-rollover, as found on disk: the superseded placement draining, its
	// replacement already serving, both containers up.
	drainingInst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	servingInst := store.CreateScheduledInstance(updated.ID, updated.Version, updated.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, drainingInst.ID, cfg.Version, apigen.RunningStatus_RUNNING)
	markRunning(t, store, servingInst.ID, updated.Version, apigen.RunningStatus_RUNNING)

	s := restartWith(store, newFakeBarrier(), updated)

	// An adopted wait must not trust the barrier: with no acknowledgements
	// recorded yet, AppliedEverywhere is vacuously true and would retire the
	// placement instantly, before any worker has confirmed the flip.
	s.retireDrainedInstances()
	if got := statesByID(store, cfg.ID)[drainingInst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state before the backstop expired = %v, want still RUN_DRAINING: "+
			"an adopted wait must not be satisfied by a barrier that has heard from nobody", got)
	}

	wait, tracked := s.draining[drainingInst.ID]
	if !tracked {
		t.Fatal("draining instance was not adopted after restart: nothing else can ever retire it")
	}
	if !wait.adopted {
		t.Fatal("rebuilt wait must be marked adopted")
	}

	// Repeated reconciles must not keep pushing the deadline out.
	s.onInstance(fetchState(t, store, drainingInst.ID))
	if s.draining[drainingInst.ID].deadline != wait.deadline {
		t.Fatal("re-adoption moved the deadline: the drain would never expire")
	}

	s.draining[drainingInst.ID] = drainWait{deadline: time.Now().Add(-time.Second), adopted: true}
	s.retireDrainedInstances()
	if got := statesByID(store, cfg.ID)[drainingInst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after the backstop expired = %v, want TERMINATE", got)
	}
}

func stoppedSpec(version string) *apigen.DeploymentSpec {
	spec := testRunningSpec(version)
	spec.Container1Spec.Running = false
	return spec
}

func updateSpec(t *testing.T, store *state.Service, cfg *apigen.DeploymentConfig, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig {
	t.Helper()
	next := *spec
	updated, _, ok := store.UpdateDeploymentConfig(apigen.Context{}, cfg.ID, state.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            &next,
	})
	if !ok {
		t.Fatal("expected config update to succeed")
	}
	return updated
}

// TestStoppedInstanceIsFinalized pins the meaning of target state: it says what a
// placement should be doing, and a stopped one should be doing nothing. Leaving
// it scheduled so the UI has something to render made the UI the reason a
// placement stayed live, and left it there forever — finalization is driven by an
// instance's own status updates, and a stopped instance produces no more of them.
func TestStoppedInstanceIsFinalized(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, testRunningSpec("v1"))
	inst := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, inst.ID, cfg.Version, apigen.RunningStatus_RUNNING)

	s := New(store, newFakeBarrier())
	stopped := updateSpec(t, store, cfg, stoppedSpec("v1"))
	s.onConfig(*stopped)

	if got := statesByID(store, cfg.ID)[inst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after stop = %v, want TERMINATE while the container is still up", got)
	}

	markRunning(t, store, inst.ID, stopped.Version, apigen.RunningStatus_STOPPED)
	s.onInstance(fetchState(t, store, inst.ID))

	if active := store.ListNonFinalScheduledInstancesForDeployment(cfg.ID); len(active) != 0 {
		t.Fatalf("active after the node stopped = %+v, want none", active)
	}
}

// TestRestartingAfterStopLeavesOnlyTheReplacement is the bug this all came from:
// a stopped placement kept its schedule until something superseded it, but
// nothing re-examined it once something did, so the old run sat in the UI beside
// the new one indefinitely.
func TestRestartingAfterStopLeavesOnlyTheReplacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, testRunningSpec("v1"))
	older := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, older.ID, cfg.Version, apigen.RunningStatus_RUNNING)

	s := New(store, newFakeBarrier())
	stopped := updateSpec(t, store, cfg, stoppedSpec("v1"))
	s.onConfig(*stopped)
	markRunning(t, store, older.ID, stopped.Version, apigen.RunningStatus_STOPPED)
	s.onInstance(fetchState(t, store, older.ID))

	restarted := updateSpec(t, store, stopped, testRunningSpec("v2"))
	s.onConfig(*restarted)

	active := store.ListNonFinalScheduledInstancesForDeployment(cfg.ID)
	if len(active) != 1 {
		t.Fatalf("active after restart = %+v, want only the replacement", active)
	}
	if active[0].ID == older.ID {
		t.Fatal("the stopped placement is still scheduled")
	}
	if active[0].DeploymentVersion != restarted.Version {
		t.Fatalf("replacement version = %d, want %d", active[0].DeploymentVersion, restarted.Version)
	}
}

// TestStartupFinalizesStoppedInstances covers placements stranded by a build that
// did not retire them, and by a crash between the node stopping and the primary
// reacting. Nothing else will ever revisit them.
func TestStartupFinalizesStoppedInstances(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := store.EnsurePrimaryNode("primary", "primary")

	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{
		SpaceID: state.DefaultSpaceID,
		Name:    "app",
	}, node.ID, testRunningSpec("v1"))

	stranded := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.SetScheduledInstanceState(stranded.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
	markRunning(t, store, stranded.ID, cfg.Version, apigen.RunningStatus_STOPPED)

	// A placement still shutting down is not stopped and must survive the sweep.
	shuttingDown := store.CreateScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	store.SetScheduledInstanceState(shuttingDown.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
	markRunning(t, store, shuttingDown.ID, cfg.Version, apigen.RunningStatus_RUNNING)

	New(store, newFakeBarrier()).finalizeStopped()

	byID := statesByID(store, cfg.ID)
	if _, still := byID[stranded.ID]; still {
		t.Fatal("startup left a stopped placement scheduled")
	}
	if byID[shuttingDown.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("shutting-down placement = %v, want still TERMINATE", byID[shuttingDown.ID])
	}
}
