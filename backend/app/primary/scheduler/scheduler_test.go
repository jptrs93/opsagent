package scheduler

import (
	"context"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

// fakeBarrier stands in for the applied-stamp barrier. Held blocks every
// wait, which is how tests distinguish "drained and retired" from "draining and
// still waiting for the cluster to catch up".
type fakeBarrier struct {
	held bool
	acks chan struct{}
}

func newFakeBarrier() *fakeBarrier {
	return &fakeBarrier{acks: make(chan struct{}, 1)}
}

func (b *fakeBarrier) DecisionInForce(seq int64) bool { return !b.held }
func (b *fakeBarrier) AckUpdates() <-chan struct{}    { return b.acks }

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
	for _, inst := range statetest.NonFinalInstances(store, deploymentID) {
		out[inst.ID] = inst.State
	}
	return out
}

func markRunning(t *testing.T, store *state.Service, instanceID, specVersion int32, status apigen.RunningStatus) {
	t.Helper()
	scheduledinstances.WriteStatus(store, instanceID, func(st *apigen.ScheduledInstanceStatus) bool {
		st.BumpUpdatedAt()
		st.Runner = apigen.RunnerStatus{DeploymentSpecVersion: specVersion, Status: status}
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

func TestOlderRunningStatusCannotDrainReplacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, rolloverSpec("v2"))
	var newer int32
	for id := range statesByID(store, cfg.DeploymentID) {
		if id != older.ID {
			newer = id
		}
	}
	markRunning(t, store, newer, updated.SpecVersion, apigen.RunningStatus_RUNNING)
	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	byID := statesByID(store, cfg.DeploymentID)
	if byID[newer] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING || byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("older status changed serving ownership: %v", byID)
	}
}

func TestStartupReconcileDoesNotLetOlderRunningKillReplacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	next := *testRunningSpec("v2")
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)

	startScheduler(t, store, newFakeBarrier())

	active := statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 || active[0].ID != older.ID || active[0].State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("RECREATE first reconciliation = %+v, want only older TERMINATE", active)
	}

	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_STOPPED)

	active = statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 || active[0].DeploymentSpecVersion != updated.SpecVersion ||
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	next := *rolloverSpec("v2")
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)

	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)

	byID := statesByID(store, cfg.DeploymentID)
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
	markRunning(t, store, replacement, updated.SpecVersion, apigen.RunningStatus_STARTING)
	if got := statesByID(store, cfg.DeploymentID)[replacement]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
		t.Fatalf("STARTING standby state = %v, want still RUN_STANDBY", got)
	}

	markRunning(t, store, replacement, updated.SpecVersion, apigen.RunningStatus_RUNNING)
	byID = statesByID(store, cfg.DeploymentID)
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	serving := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, serving.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)

	push := func(version string) *apigen.DeploymentEvent {
		t.Helper()
		next := *rolloverSpec(version)
		updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)
		return updated
	}

	// The v2 standby's prepare fails, so it never reports RUNNING and its runner
	// status stays empty.
	v2 := push("v2")
	byID := statesByID(store, cfg.DeploymentID)
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
	byID = statesByID(store, cfg.DeploymentID)
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
	markRunning(t, store, staleStandby, v2.SpecVersion, apigen.RunningStatus_STOPPED)
	byID = statesByID(store, cfg.DeploymentID)
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, rolloverSpec("v2"))
	newer := statetest.CreateScheduledInstance(store, cfg.DeploymentID, updated.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)

	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)
	markRunning(t, store, newer.ID, updated.SpecVersion, apigen.RunningStatus_RUNNING)
	if got := statesByID(store, cfg.DeploymentID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state after supersede = %v, want RUN_DRAINING", got)
	}

	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state while the barrier is held = %v, want still RUN_DRAINING", got)
	}

	barrier.held = false
	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID)[older.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after the barrier cleared = %v, want TERMINATE", got)
	}

}

// TestStandbyPromotedWhenServingDies covers the failure the plain readiness
// handoff cannot: if the serving container stops for good mid-rollover, no
// readiness signal is ever coming, and leaving the inbound address pointed at
// it would blackhole the deployment.
func TestStandbyPromotedWhenServingDies(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	next := *rolloverSpec("v2")
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)
	standby := statetest.CreateScheduledInstance(store, updated.DeploymentID, updated.Version, updated.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)

	barrier := newFakeBarrier()
	barrier.held = true
	startScheduler(t, store, barrier)

	// The serving placement stops for good while the standby is still warming.
	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_STOPPED)

	byID := statesByID(store, cfg.DeploymentID)
	if byID[standby.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("standby state = %v, want promoted to RUN_SERVING", byID[standby.ID])
	}
	if byID[older.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("failed serving state = %v, want RUN_DRAINING", byID[older.ID])
	}
}

func TestSpaceMoveRidesTheRolloverPath(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	serving := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	newSpace := int32(5)
	statetest.MoveDeploymentSpace(store, apigen.Context{User: &apigen.InternalUser{ID: 9}}, cfg.DeploymentID, newSpace)
	startScheduler(t, store, newFakeBarrier())
	sweep(t, store)

	var standby *apigen.ScheduledInstance
	for _, inst := range statetest.NonFinalInstances(store, cfg.DeploymentID) {
		if inst.ID != serving.ID {
			standby = inst
		}
	}
	if standby == nil || standby.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
		t.Fatalf("standby after move = %+v, want a RUN_STANDBY replacement", standby)
	}
	if standby.SpaceID != 5 || standby.DeploymentSpecVersion != cfg.SpecVersion {
		t.Fatalf("standby pin = space %d v%d, want space 5 v%d",
			standby.SpaceID, standby.DeploymentSpecVersion, cfg.SpecVersion)
	}
	if st := fetchState(t, store, serving.ID); st.Config.Value.SpaceID != nodes.DefaultSpaceID {
		t.Fatalf("serving view = space %d, want old space %d", st.Config.Value.SpaceID, nodes.DefaultSpaceID)
	}
	if st := fetchState(t, store, standby.ID); st.Config.Value.SpaceID != 5 {
		t.Fatalf("standby view = space %d, want new space 5", st.Config.Value.SpaceID)
	}
	sweep(t, store)
	if got := len(statetest.NonFinalInstances(store, cfg.DeploymentID)); got != 2 {
		t.Fatalf("placements after re-reconcile = %d, want 2", got)
	}

	markRunning(t, store, standby.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	byID := statesByID(store, cfg.DeploymentID)
	if byID[standby.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("standby state = %v, want promoted to RUN_SERVING", byID[standby.ID])
	}
	if byID[serving.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("old-space serving state = %v, want RUN_DRAINING", byID[serving.ID])
	}
}

// TestTerminateDeploymentStopsEveryRunnableState guards the state-machine seam:
// a stopped deployment must retire standbys and draining placements too, not
// just the serving one.
func TestTerminateDeploymentStopsEveryRunnableState(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))

	serving := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	standby := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	draining := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)

	statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, stoppedSpec("v1"))
	startScheduler(t, store, newFakeBarrier())

	byID := statesByID(store, cfg.DeploymentID)
	for name, id := range map[string]int32{"serving": serving.ID, "standby": standby.ID, "draining": draining.ID} {
		if byID[id] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
			t.Fatalf("%s instance state = %v, want TERMINATE", name, byID[id])
		}
	}
}

var testSchedulers sync.Map

func startScheduler(t *testing.T, store *state.Service, barrier routeBarrier) *Scheduler {
	t.Helper()
	s := New(store, barrier)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	testSchedulers.Store(store, s)
	return s
}

func sweep(t *testing.T, store *state.Service) {
	t.Helper()
	s, ok := testSchedulers.Load(store)
	if !ok {
		t.Fatal("no scheduler started for store")
	}
	if err := s.(*Scheduler).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestRestartHandlesEveryInstanceState pins the startup reconcile against the
// full target-state set. Recovery reads targets and latest reports directly
// from the database before runtime processing begins.
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
			node := nodes.EnsurePrimaryNode(store, "primary", "primary")
			cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
			// Instances are always born runnable; non-runnable targets are reached
			// by transition, so build the fixture the same way.
			initial := tc.state
			if !initial.WantsRunning() {
				initial = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
			}
			inst := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, initial)
			if initial != tc.state {
				statetest.SetScheduledInstanceState(store, inst.ID, tc.state)
			}
			markRunning(t, store, inst.ID, cfg.SpecVersion, tc.status)

			startScheduler(t, store, newFakeBarrier())

			if got := statesByID(store, cfg.DeploymentID)[inst.ID]; got != tc.want {
				t.Fatalf("state after restart = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRestartAdoptsDrainingInstances checks recovery from a real database reopen.
// A persisted drain keeps running until a fresh boot timeout has elapsed, even
// when the new barrier has no acknowledgements and reports vacuous success.
func TestRestartAdoptsDrainingInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(path)
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))

	next := *rolloverSpec("v2")
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)
	// Mid-rollover, as found on disk: the superseded placement draining, its
	// replacement already serving, both containers up.
	drainingInst := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	servingInst := statetest.CreateScheduledInstance(store, updated.DeploymentID, updated.Version, updated.Value.NodeID, 0,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, drainingInst.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	markRunning(t, store, servingInst.ID, updated.SpecVersion, apigen.RunningStatus_RUNNING)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = state.Open(path)
	s := startScheduler(t, store, newFakeBarrier())

	// An adopted wait must not trust the barrier: with no acknowledgements
	// recorded yet, DecisionInForce is vacuously true and would retire the
	// placement instantly, before any secondary has confirmed the flip.
	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID)[drainingInst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("state before the backstop expired = %v, want still RUN_DRAINING: "+
			"an adopted wait must not be satisfied by a barrier that has heard from nobody", got)
	}

	// Repeated sweeps cannot move the boot deadline. Advance a controlled clock
	// beyond the original deadline and prove this persisted drain is recovered.
	boot := s.bootTime
	s.now = func() time.Time { return boot.Add(drainTimeout - time.Millisecond) }
	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID)[drainingInst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("early retirement: %v", got)
	}
	s.now = func() time.Time { return boot.Add(drainTimeout + time.Millisecond) }
	sweep(t, store)
	if got := statesByID(store, cfg.DeploymentID)[drainingInst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after the backstop expired = %v, want TERMINATE", got)
	}
}

func stoppedSpec(version string) *apigen.DeploymentSpec {
	spec := testRunningSpec(version)
	spec.Container1Spec.Running = false
	return spec
}

func updateSpec(t *testing.T, store *state.Service, cfg *apigen.DeploymentEvent, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	t.Helper()
	next := *spec
	updated := statetest.UpdateDeploymentSpec(store, apigen.Context{}, cfg.DeploymentID, &next)
	return updated
}

func TestRestartEventReplacesThePlacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	serving := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, serving.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	startScheduler(t, store, newFakeBarrier())

	restarted := statetest.RestartDeployment(store, apigen.Context{}, cfg.DeploymentID)
	if restarted.Version != cfg.Version+1 || restarted.SpecVersion != cfg.SpecVersion {
		t.Fatalf("restart event version/spec = %d/%d, want %d/%d", restarted.Version, restarted.SpecVersion, cfg.Version+1, cfg.SpecVersion)
	}
	byID := statesByID(store, cfg.DeploymentID)
	if len(byID) != 1 || byID[serving.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("placements after restart = %v, want only the old one terminating", byID)
	}

	markRunning(t, store, serving.ID, cfg.SpecVersion, apigen.RunningStatus_STOPPED)
	active := statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 || active[0].ID == serving.ID {
		t.Fatalf("placements after the old one stopped = %+v, want only the replacement", active)
	}
	replacement := active[0]
	if replacement.DeploymentVersion != restarted.Version || replacement.DeploymentSpecVersion != cfg.SpecVersion || replacement.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatalf("replacement = %+v, want serving at deployment version %d and spec version %d", replacement, restarted.Version, cfg.SpecVersion)
	}
}

func TestRestartEventRollsOverThePlacement(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, rolloverSpec("v1"))
	serving := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, serving.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	startScheduler(t, store, newFakeBarrier())

	restarted := statetest.RestartDeployment(store, apigen.Context{}, cfg.DeploymentID)
	var standby *apigen.ScheduledInstance
	for _, inst := range statetest.NonFinalInstances(store, cfg.DeploymentID) {
		if inst.ID != serving.ID {
			standby = inst
		}
	}
	if standby == nil || standby.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY || standby.DeploymentVersion != restarted.Version || standby.DeploymentSpecVersion != cfg.SpecVersion {
		t.Fatalf("standby after restart = %+v, want RUN_STANDBY at deployment version %d and spec version %d", standby, restarted.Version, cfg.SpecVersion)
	}
	if statesByID(store, cfg.DeploymentID)[serving.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		t.Fatal("old placement stopped serving before the replacement was ready")
	}

	markRunning(t, store, standby.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)
	byID := statesByID(store, cfg.DeploymentID)
	if byID[standby.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING || byID[serving.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		t.Fatalf("states after readiness = %v, want replacement serving and old draining", byID)
	}
}

// TestStoppedInstanceIsFinalized pins the meaning of target state: it says what a
// placement should be doing, and a stopped one should be doing nothing. Leaving
// it scheduled so the UI has something to render made the UI the reason a
// placement stayed live, and left it there forever — finalization is driven by an
// instance's own status updates, and a stopped instance produces no more of them.
func TestStoppedInstanceIsFinalized(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	inst := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, inst.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	startScheduler(t, store, newFakeBarrier())
	stopped := updateSpec(t, store, cfg, stoppedSpec("v1"))

	if got := statesByID(store, cfg.DeploymentID)[inst.ID]; got != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("state after stop = %v, want TERMINATE while the container is still up", got)
	}

	markRunning(t, store, inst.ID, stopped.SpecVersion, apigen.RunningStatus_STOPPED)

	if active := statetest.NonFinalInstances(store, cfg.DeploymentID); len(active) != 0 {
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
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))
	older := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	markRunning(t, store, older.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	startScheduler(t, store, newFakeBarrier())
	stopped := updateSpec(t, store, cfg, stoppedSpec("v1"))
	markRunning(t, store, older.ID, stopped.SpecVersion, apigen.RunningStatus_STOPPED)

	restarted := updateSpec(t, store, stopped, testRunningSpec("v2"))

	active := statetest.NonFinalInstances(store, cfg.DeploymentID)
	if len(active) != 1 {
		t.Fatalf("active after restart = %+v, want only the replacement", active)
	}
	if active[0].ID == older.ID {
		t.Fatal("the stopped placement is still scheduled")
	}
	if active[0].DeploymentSpecVersion != restarted.SpecVersion {
		t.Fatalf("replacement version = %d, want %d", active[0].DeploymentSpecVersion, restarted.SpecVersion)
	}
}

// TestStartupFinalizesStoppedInstances covers placements stranded by a build that
// did not retire them, and by a crash between the node stopping and the primary
// reacting. Nothing else will ever revisit them.
func TestStartupFinalizesStoppedInstances(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")

	cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, nodes.DefaultSpaceID, "app", node.ID, testRunningSpec("v1"))

	stranded := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	statetest.SetScheduledInstanceState(store, stranded.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
	markRunning(t, store, stranded.ID, cfg.SpecVersion, apigen.RunningStatus_STOPPED)

	// A placement still shutting down is not stopped and must survive the sweep.
	shuttingDown := statetest.CreateScheduledInstance(store, cfg.DeploymentID, cfg.Version, cfg.Value.NodeID, 1, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	statetest.SetScheduledInstanceState(store, shuttingDown.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
	markRunning(t, store, shuttingDown.ID, cfg.SpecVersion, apigen.RunningStatus_RUNNING)

	startScheduler(t, store, newFakeBarrier())

	byID := statesByID(store, cfg.DeploymentID)
	if _, still := byID[stranded.ID]; still {
		t.Fatal("startup left a stopped placement scheduled")
	}
	if byID[shuttingDown.ID] != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
		t.Fatalf("shutting-down placement = %v, want still TERMINATE", byID[shuttingDown.ID])
	}
}
