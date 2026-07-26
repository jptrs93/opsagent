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
	"log/slog"
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
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
	// CurrentSequence is the sequence encoding the state derived so far.
	CurrentSequence() int64
	// AppliedEverywhere reports whether every node holding network state has
	// programmed at least the given sequence.
	AppliedEverywhere(sequence int64) bool
	// AckUpdates fires when applied sequences advance.
	AckUpdates() <-chan struct{}
}

type Scheduler struct {
	store   *sqlite.PrimaryStorage
	barrier routeBarrier

	// draining records what each draining placement is waiting for: the sequence
	// that must be applied everywhere, and the deadline after which it stops
	// waiting. It is in-memory only, so a restart finds RUN_DRAINING rows on disk
	// with nothing tracking them; adoptDraining puts them back under a fresh
	// deadline, which only ever waits longer than the original.
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

func New(store *sqlite.PrimaryStorage, barrier routeBarrier) *Scheduler {
	return &Scheduler{store: store, barrier: barrier, draining: make(map[int32]drainWait)}
}

// Run reconciles the current config snapshot, then reacts to config and
// scheduled-instance updates until the process exits.
func (s *Scheduler) Run() {
	configs, configCh, unsubConfigs := s.store.MustFetchDeploymentConfigSnapshotAndSubscribe(nil)
	defer unsubConfigs()
	instances, instanceCh, unsubInstances := s.store.MustFetchScheduledSnapshotAndSubscribe(nil)
	defer unsubInstances()

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

func (s *Scheduler) onConfig(cfg apigen.DeploymentConfig) {
	if cfg.ID == 0 {
		return
	}
	current := s.store.FetchDeploymentConfig(cfg.ID)
	if current == nil || current.Version != cfg.Version {
		return
	}
	cfg = *current
	if cfg.Deleted || !cfg.WorkloadRunning() {
		s.terminateDeployment(cfg.ID)
		return
	}
	if cfg.NodeID <= 0 {
		slog.Warn("scheduler: skipping config without node", "deployment_id", cfg.ID, "version", cfg.Version)
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
		if inst.DeploymentVersion == cfg.Version && inst.NodeID == cfg.NodeID && inst.State.WantsRunning() {
			exact = state
			continue
		}
		if cfg.EffectiveUpgradeStrategy() != apigen.ContainerUpgradeStrategy_RECREATE {
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
	inst, created := s.store.EnsureRunScheduledInstance(cfg.ID, cfg.Version, cfg.NodeID, defaultInstanceOrdinal, initial)
	if !created {
		return
	}
	slog.Info("scheduler: created scheduled instance",
		"scheduled_instance", inst.ID,
		"deployment_id", cfg.ID,
		"version", cfg.Version,
		"node_id", cfg.NodeID,
		"state", initial,
	)
}

func (s *Scheduler) onInstance(state apigen.ScheduledInstanceState) {
	inst := state.Instance
	if inst.ID == 0 {
		return
	}
	switch {
	case inst.State.WantsRunning():
		cfg := s.store.FetchDeploymentConfig(inst.DeploymentID)
		if cfg == nil || cfg.Deleted || !cfg.WorkloadRunning() {
			s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
			return
		}
		if inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			s.adoptDraining(inst.ID)
			s.retireDrainedInstances()
			return
		}
		if cfg.Version == inst.DeploymentVersion && cfg.NodeID == inst.NodeID {
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
		if cfg := s.store.FetchDeploymentConfig(inst.DeploymentID); cfg != nil {
			s.onConfig(*cfg)
		}
		// Keep a stopped assignment visible/reconciled until it is superseded by a
		// newer instance or the deployment itself is deleted.
		if !s.shouldFinalize(inst) {
			return
		}
		s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
		slog.Info("scheduler: finalized scheduled instance",
			"scheduled_instance", inst.ID,
			"deployment_id", inst.DeploymentID,
		)
	}
}

// promoteIfReady hands the instance's inbound address to a standby that has
// reported RUNNING, moving the placement it supersedes into draining. The two
// writes are what a route flip consists of: nothing else moves an address.
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
	if state.Status.Runner.DeploymentConfigVersion != inst.DeploymentVersion ||
		state.Status.Runner.Status != apigen.RunningStatus_RUNNING {
		return
	}
	s.drainSuperseded(inst)
	s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	slog.Info("scheduler: scheduled instance took over serving",
		"scheduled_instance", inst.ID,
		"deployment_id", inst.DeploymentID,
		"node_id", inst.NodeID,
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
		s.beginDraining(failed.ID)
		s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
		slog.Info("scheduler: promoted standby over a failed serving instance",
			"scheduled_instance", inst.ID,
			"failed", failed.ID,
			"deployment_id", failed.DeploymentID,
		)
		return
	}
}

// drainSuperseded moves every older placement of this ordinal into draining.
// Draining keeps the container running and its own routes published, so replies
// to work already in flight still reach it after the address moves.
func (s *Scheduler) drainSuperseded(current apigen.ScheduledInstance) {
	active := s.store.ListNonFinalScheduledInstancesForDeployment(current.DeploymentID)
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })
	for _, inst := range active {
		if inst.ID >= current.ID || inst.InstanceOrdinal != current.InstanceOrdinal {
			continue
		}
		if !inst.State.WantsRunning() ||
			inst.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
			continue
		}
		s.beginDraining(inst.ID)
		slog.Info("scheduler: draining superseded scheduled instance",
			"scheduled_instance", inst.ID,
			"replacement", current.ID,
			"deployment_id", current.DeploymentID,
		)
	}
}

// beginDraining records what the placement is waiting for before writing the
// state, so the sequence captured is never newer than the decision itself.
func (s *Scheduler) beginDraining(instanceID int32) {
	wait := drainWait{deadline: time.Now().Add(drainTimeout)}
	s.setState(instanceID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING)
	if s.barrier != nil {
		// Read the sequence after the write: the state change is what produces the
		// map that must propagate, so the sequence to wait for is the one rendered
		// from it. If the flip changed no routes at all — both placements on one
		// node — no new sequence is minted and the wait is already satisfied.
		wait.sequence = s.barrier.CurrentSequence()
	}
	s.draining[instanceID] = wait
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
	slog.Info("scheduler: adopted an untracked draining scheduled instance",
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
		// AppliedEverywhere is vacuously true — it cannot tell "every node applied
		// the flip" from "no node has reported yet". Trusting it there would retire
		// the placement instantly, which is precisely the case the barrier exists to
		// prevent: a worker that had not yet applied the flip still points the
		// instance prefix at a container we just stopped. Adopted waits sit out the
		// backstop instead, which also gives workers time to reconnect and report.
		applied := s.barrier == nil || (!wait.adopted && s.barrier.AppliedEverywhere(wait.sequence))
		expired := now.After(wait.deadline)
		if !applied && !expired {
			continue
		}
		if expired && !applied && !wait.adopted {
			slog.Warn("scheduler: retiring drained instance before every node acknowledged",
				"scheduled_instance", instanceID,
				"sequence", wait.sequence,
				"waited", drainTimeout)
		}
		delete(s.draining, instanceID)
		s.setState(instanceID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
		slog.Info("scheduler: drained scheduled instance retired",
			"scheduled_instance", instanceID,
			"deployment_id", inst.DeploymentID,
			"sequence", wait.sequence)
	}
}

func (s *Scheduler) setState(instanceID int32, state apigen.ScheduledInstanceTarget) {
	if state != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING {
		delete(s.draining, instanceID)
	}
	s.store.SetScheduledInstanceState(instanceID, state)
}

func (s *Scheduler) scheduledStates(deploymentID int32) []apigen.ScheduledInstanceState {
	return s.store.FetchScheduledSnapshot(storage.ScheduledInstancePredicate(func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.DeploymentID == deploymentID
	}))
}

func terminalRunnerStatus(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_STOPPED || status == apigen.RunningStatus_NO_DEPLOYMENT
}

func (s *Scheduler) shouldFinalize(inst apigen.ScheduledInstance) bool {
	cfg := s.store.FetchDeploymentConfig(inst.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return true
	}
	for _, other := range s.store.ListNonFinalScheduledInstancesForDeployment(inst.DeploymentID) {
		if other.ID > inst.ID && other.InstanceOrdinal == inst.InstanceOrdinal {
			return true
		}
	}
	return false
}

func (s *Scheduler) terminateDeployment(deploymentID int32) {
	active := s.store.ListNonFinalScheduledInstancesForDeployment(deploymentID)
	for _, inst := range active {
		if inst.State.WantsRunning() {
			s.setState(inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE)
			slog.Info("scheduler: terminate scheduled instance (desired stopped)",
				"scheduled_instance", inst.ID,
				"deployment_id", deploymentID,
			)
		}
	}
}
