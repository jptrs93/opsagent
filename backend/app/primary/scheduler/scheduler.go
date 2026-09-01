// Package scheduler turns desired deployment configs into scheduled instance
// assignments and advances instance target state from observed status.
//
// Target state is the sole input to cross-node routing, so the transitions here
// are what make network state derivable without consulting status. Exactly one
// placement per (deployment, ordinal) is RUN_SERVING and owns the instance's
// stable inbound address; a replacement warms up as RUN_STANDBY, and the
// placement it supersedes drains as RUN_DRAINING while it still holds its own
// routes. A draining placement is only told to stop once every node has
// programmed the routing that replaced it.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const defaultInstanceOrdinal int32 = 0

// drainTimeout bounds how long a draining placement waits for the cluster to
// apply the routing that replaced it. The barrier is the mechanism; this is the
// backstop for a node that is connected but wedged, so one unhealthy node
// cannot pin a superseded container forever.
const drainTimeout = 30 * time.Second

// drainPollInterval re-examines draining placements when no acknowledgement
// has arrived, so the timeout backstop fires without an external wakeup.
const drainPollInterval = 2 * time.Second

// routeBarrier reports when published network state is in force cluster-wide.
type routeBarrier interface {
	// DecisionInForce reports whether the routing implied by the sequenced
	// write at seq has been rendered and applied by every node holding
	// network state.
	DecisionInForce(seq int64) bool
	// AckUpdates fires when applied stamps or renders advance.
	AckUpdates() <-chan struct{}
}

type Scheduler struct {
	ctx     context.Context
	store   *state.Service
	barrier routeBarrier

	// draining records what each draining placement is waiting for: the global
	// write sequence of the drain decision, which must be in force everywhere,
	// and the deadline after which it stops waiting. It is in-memory only, so a
	// restart finds RUN_DRAINING rows on disk with nothing tracking them;
	// adoptDraining puts them back under a fresh deadline, which only ever waits
	// longer than the original.
	draining map[int32]drainWait
}

type drainWait struct {
	sequence int64
	deadline time.Time
	// adopted marks a wait rebuilt after a restart rather than recorded when the
	// drain was decided. See retireDrainedInstances for why it ignores the
	// barrier.
	adopted bool
}

func New(store *state.Service, barrier routeBarrier) *Scheduler {
	return &Scheduler{
		ctx:      logu.AddTag(context.Background(), "Scheduler"),
		store:    store,
		barrier:  barrier,
		draining: make(map[int32]drainWait),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	s.ctx = logu.AddTag(ctx, "Scheduler")
	configs, configCh, unsubConfigs := s.store.MustFetchDeploymentSnapshotAndSubscribe(nil)
	defer unsubConfigs()
	instances, instanceCh, unsubInstances := s.store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubInstances()

	// Retire anything already stopped before reconciling, so the first pass sees
	// only placements that still mean something. Replaying an instance the sweep
	// finalized is a no-op: onInstance acts on RUN_* and TERMINATE alone.
	s.finalizeStopped()

	// Process existing instance statuses before creating replacements so an
	// older RUNNING snapshot cannot terminate a newly created instance.
	for i := range instances {
		s.onInstance(instances[i])
	}
	for i := range configs {
		s.onConfig(configs[i])
	}

	var acks <-chan struct{}
	if s.barrier != nil {
		acks = s.barrier.AckUpdates()
	}
	poll := time.NewTicker(drainPollInterval)
	defer poll.Stop()

	for {
		select {
		case cfg, ok := <-configCh:
			if !ok {
				return
			}
			s.onConfig(cfg)
		case state, ok := <-instanceCh:
			if !ok {
				return
			}
			s.onInstance(state)
		case <-acks:
			s.retireDrainedInstances()
		case <-poll.C:
			s.retireDrainedInstances()
		}
	}
}

func (s *Scheduler) onConfig(cfg apigen.Deployment) {
	if cfg.ID == 0 {
		return
	}
	current := s.store.FetchDeployment(cfg.ID)
	if current == nil || current.SpecVersion != cfg.SpecVersion {
		return
	}
	cfg = *current
	if cfg.Deleted || !cfg.WorkloadRunning() {
		s.terminateDeployment(cfg.ID)
		return
	}
	if cfg.NodeID <= 0 {
		slog.WarnContext(s.ctx, fmt.Sprintf("scheduler: skipping config without node version=%d", cfg.SpecVersion), "dep", cfg.ID)
		return
	}

	active := s.scheduledStates(cfg.ID)
	var exact *apigen.ScheduledInstanceState
	serving := false
	blocked := false
	for i := range active {
		state := &active[i]
		inst := state.Instance
		if inst.InstanceOrdinal != defaultInstanceOrdinal {
			continue
		}
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
			serving = true
		}
		if inst.DeploymentSpecVersion == cfg.SpecVersion && inst.NodeID == cfg.NodeID &&
			inst.SpaceID == cfg.SpaceID && inst.State.WantsRunning() {
			exact = state
			continue
		}
		if cfg.EffectiveUpgradeStrategy() != apigen.ContainerUpgradeStrategy_RECREATE {
			// A standby has never held the instance's inbound address, so there is
			// nothing in flight to drain: the config has moved on and this
			// placement is simply stale. Retiring it here is what stops a rollout
			// that keeps failing — a prepare that errors, a container that never
			// reports ready — from leaving one warming-up placement per pushed
			// version. Serving and draining placements are left alone; they still
			// own routes, and only a replacement reporting RUNNING may move those.
			if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
				s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
				slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: terminating a standby superseded by a newer config instanceSpecVersion=%d specVersion=%d", inst.DeploymentSpecVersion, cfg.SpecVersion),
					"scheduled_instance", inst.ID,
					"dep", cfg.ID,
				)
			}
			continue
		}
		// RECREATE has no overlap: the superseded placement must be fully stopped
		// before a replacement is created, so it goes straight to TERMINATE
		// rather than draining.
		switch {
		case inst.State.WantsRunning():
			s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
			blocked = true
		case inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE:
			if !terminalRunnerStatus(state.Status.Runner.Status) {
				blocked = true
			}
		}
	}
	if exact != nil {
		s.promoteIfReady(*exact)
		return
	}
	if blocked {
		return
	}

	// A replacement must never be born serving: that would point the instance's
	// inbound route at a node whose container does not exist yet. It warms up as
	// a standby and takes over only once it reports RUNNING.
	initial := apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
	if serving {
		initial = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
	}
	inst, created := s.store.EnsureRunScheduledInstance(cfg.ID, cfg.SpecVersion, cfg.NodeID, defaultInstanceOrdinal, initial)
	if !created {
		return
	}
	slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: created scheduled instance version=%d state=%v", cfg.SpecVersion, initial),
		"scheduled_instance", inst.ID,
		"dep", cfg.ID,
		"node", cfg.NodeID,
	)
}

func (s *Scheduler) onInstance(state apigen.ScheduledInstanceState) {
	inst := state.Instance
	if inst.ID == 0 {
		return
	}
	switch {
	case inst.State.WantsRunning():
		cfg := s.store.FetchDeployment(inst.DeploymentID)
		if cfg == nil || cfg.Deleted || !cfg.WorkloadRunning() {
			s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
			return
		}
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			s.adoptDraining(inst.ID)
			s.retireDrainedInstances()
			return
		}
		if cfg.SpecVersion == inst.DeploymentSpecVersion && cfg.NodeID == inst.NodeID &&
			cfg.SpaceID == inst.SpaceID {
			s.promoteIfReady(state)
			return
		}
		// The serving placement is for a superseded config. If its runner has
		// died there is nothing left to hand over, so any standby takes the
		// address immediately rather than waiting for a readiness signal that a
		// dead container will never produce.
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING &&
			terminalRunnerStatus(state.Status.Runner.Status) {
			s.promoteStandbyForFailedServing(inst)
		}
	case inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE:
		if !terminalRunnerStatus(state.Status.Runner.Status) {
			return
		}
		// Reconcile before finalizing so a RECREATE replacement is created in the
		// same pass: onConfig treats a terminal TERMINATE as no longer blocking.
		if cfg := s.store.FetchDeployment(inst.DeploymentID); cfg != nil {
			s.onConfig(*cfg)
		}
		s.finalize(inst)
	}
}

// promoteIfReady hands the instance's inbound address to a standby that has
// reported RUNNING, moving the placement it supersedes into draining. The flip
// is a single store commit: nothing else moves an address.
func (s *Scheduler) promoteIfReady(state apigen.ScheduledInstanceState) {
	inst := state.Instance
	if inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
		// Already serving: still drain anything older it has superseded, which
		// covers a promotion whose second write did not land.
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING &&
			state.Status.Runner.Status == apigen.RunningStatus_RUNNING {
			s.drainSuperseded(inst)
		}
		return
	}
	if state.Status.Runner.DeploymentSpecVersion != inst.DeploymentSpecVersion ||
		state.Status.Runner.Status != apigen.RunningStatus_RUNNING {
		return
	}
	drains := s.supersededInstanceIDs(inst)
	s.flipServing(drains, inst.ID)
	for _, id := range drains {
		slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: draining superseded scheduled instance replacement=%d", inst.ID),
			"scheduled_instance", id,
			"dep", inst.DeploymentID,
		)
	}
	slog.InfoContext(s.ctx, "scheduler: scheduled instance took over serving",
		"scheduled_instance", inst.ID,
		"dep", inst.DeploymentID,
		"node", inst.NodeID,
	)
}

// promoteStandbyForFailedServing promotes the newest standby when the serving
// placement has stopped for good. Without this a deployment whose serving
// container dies mid-rollover would keep its inbound address pointed at a node
// running nothing.
func (s *Scheduler) promoteStandbyForFailedServing(failed apigen.ScheduledInstance) {
	active := s.store.ListNonFinalScheduledInstancesForDeployment(failed.DeploymentID)
	sort.Slice(active, func(i, j int) bool { return active[i].ID > active[j].ID })
	for _, inst := range active {
		if inst.InstanceOrdinal != failed.InstanceOrdinal || inst.ID == failed.ID ||
			inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY {
			continue
		}
		s.flipServing([]int32{failed.ID}, inst.ID)
		slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: promoted standby over a failed serving instance failed=%d", failed.ID),
			"scheduled_instance", inst.ID,
			"dep", failed.DeploymentID,
		)
		return
	}
}

// drainSuperseded moves every older placement of this ordinal into draining.
// Draining keeps the container running and its own routes published, so replies
// to work already in flight still reach it after the address moves.
func (s *Scheduler) drainSuperseded(current apigen.ScheduledInstance) {
	for _, id := range s.supersededInstanceIDs(current) {
		s.beginDraining(id)
		slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: draining superseded scheduled instance replacement=%d", current.ID),
			"scheduled_instance", id,
			"dep", current.DeploymentID,
		)
	}
}

func (s *Scheduler) supersededInstanceIDs(current apigen.ScheduledInstance) []int32 {
	active := s.store.ListNonFinalScheduledInstancesForDeployment(current.DeploymentID)
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })
	ids := make([]int32, 0, len(active))
	for _, inst := range active {
		if inst.ID >= current.ID || inst.InstanceOrdinal != current.InstanceOrdinal {
			continue
		}
		if !inst.State.WantsRunning() ||
			inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			continue
		}
		ids = append(ids, inst.ID)
	}
	return ids
}

func (s *Scheduler) flipServing(drainIDs []int32, serveID int32) {
	delete(s.draining, serveID)
	seq := s.store.FlipScheduledInstanceServing(drainIDs, serveID)
	deadline := time.Now().Add(drainTimeout)
	for _, id := range drainIDs {
		s.draining[id] = drainWait{sequence: seq, deadline: deadline}
	}
}

// beginDraining waits on the drain decision's own write sequence: the state
// change is what produces the map that must propagate, and the sequence its
// transaction allocated identifies it exactly. If the flip changed no routes at
// all — both placements on one node — the published map never changes and the
// wait is satisfied as soon as a render has seen the write.
func (s *Scheduler) beginDraining(instanceID int32) {
	seq := s.setState(instanceID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	s.draining[instanceID] = drainWait{sequence: seq, deadline: time.Now().Add(drainTimeout)}
}

// adoptDraining puts a RUN_DRAINING placement the scheduler is not tracking
// back under a wait. This is what makes a restart mid-rollover recoverable:
// drainSuperseded deliberately skips anything already draining so repeated calls
// cannot reset a deadline, which means nothing else would ever pick these up and
// the placement would keep running, and keep its routes published, forever.
func (s *Scheduler) adoptDraining(instanceID int32) {
	if _, tracked := s.draining[instanceID]; tracked {
		return
	}
	s.draining[instanceID] = drainWait{deadline: time.Now().Add(drainTimeout), adopted: true}
	slog.InfoContext(s.ctx, "scheduler: adopted an untracked draining scheduled instance",
		"scheduled_instance", instanceID)
}

// retireDrainedInstances tells draining placements to stop once the routing
// that replaced them is in force everywhere, or once they have waited long
// enough that a wedged node should not hold them any longer.
func (s *Scheduler) retireDrainedInstances() {
	if len(s.draining) == 0 {
		return
	}
	now := time.Now()
	for instanceID, wait := range s.draining {
		inst := s.store.FetchScheduledInstance(instanceID)
		if inst == nil || inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			delete(s.draining, instanceID)
			continue
		}
		// An adopted wait cannot use the barrier. Acknowledgements live only in
		// memory, so straight after a restart the barrier knows of no node and
		// DecisionInForce is vacuously true — it cannot tell "every node applied
		// the flip" from "no node has reported yet". Trusting it there would retire
		// the placement instantly, which is precisely the case the barrier exists to
		// prevent: a secondary that had not yet applied the flip still points the
		// instance prefix at a container we just stopped. Adopted waits sit out the
		// backstop instead, which also gives secondaries time to reconnect and report.
		applied := s.barrier == nil || (!wait.adopted && s.barrier.DecisionInForce(wait.sequence))
		expired := now.After(wait.deadline)
		if !applied && !expired {
			continue
		}
		if expired && !applied && !wait.adopted {
			slog.WarnContext(s.ctx, fmt.Sprintf("scheduler: retiring drained instance before every node acknowledged sequence=%d waited=%v", wait.sequence, drainTimeout),
				"scheduled_instance", instanceID)
		}
		delete(s.draining, instanceID)
		s.setState(instanceID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
		slog.InfoContext(s.ctx, fmt.Sprintf("scheduler: drained scheduled instance retired sequence=%d", wait.sequence),
			"scheduled_instance", instanceID,
			"dep", inst.DeploymentID)
	}
}

func (s *Scheduler) setState(instanceID int32, state apigen.ScheduledInstanceTarget) int64 {
	if state != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		delete(s.draining, instanceID)
	}
	return s.store.SetScheduledInstanceState(instanceID, state)
}

func (s *Scheduler) scheduledStates(deploymentID int32) []apigen.ScheduledInstanceState {
	return s.store.FetchScheduledSnapshot(storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.DeploymentID == deploymentID
	}))
}

func terminalRunnerStatus(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_STOPPED || status == apigen.RunningStatus_NO_DEPLOYMENT
}

// finalize retires a placement that has stopped. Target state describes what a
// placement should be doing, and a stopped one should be doing nothing, so this
// is unconditional: whether anything replaced it, and whether a user still wants
// to look at how it ended, are not questions about its schedule. The storage
// layer retains the last incarnation of each ordinal for display.
func (s *Scheduler) finalize(inst apigen.ScheduledInstance) {
	s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	slog.InfoContext(s.ctx, "scheduler: finalized scheduled instance",
		"scheduled_instance", inst.ID,
		"dep", inst.DeploymentID,
	)
}

// finalizeStopped retires every placement already sitting in TERMINATE with a
// terminal runner status. Finalization is otherwise driven by an instance's own
// status updates, and a stopped instance produces no more of them, so anything
// that reached that state while the scheduler was not running — or under a build
// that declined to retire it — would stay scheduled forever without this sweep.
func (s *Scheduler) finalizeStopped() {
	for _, state := range s.store.FetchScheduledSnapshot(nil) {
		inst := state.Instance
		if inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE {
			continue
		}
		if !terminalRunnerStatus(state.Status.Runner.Status) {
			continue
		}
		s.finalize(inst)
	}
}

func (s *Scheduler) terminateDeployment(deploymentID int32) {
	active := s.store.ListNonFinalScheduledInstancesForDeployment(deploymentID)
	for _, inst := range active {
		if inst.State.WantsRunning() {
			s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
			slog.InfoContext(s.ctx, "scheduler: terminate scheduled instance (desired stopped)",
				"scheduled_instance", inst.ID,
				"dep", deploymentID,
			)
		}
	}
}
