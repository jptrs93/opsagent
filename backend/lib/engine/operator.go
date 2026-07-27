package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/goutil/timeu"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/containerimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubrelease"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/githubreleaseimage"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixdocker"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/engine/runner"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/util/version"
)

type DeploymentOperator struct {
	Store              storage.OperatorStore
	GithubRelease      *githubrelease.Preparer
	NixDocker          *nixdocker.Preparer
	GithubReleaseImage *githubreleaseimage.Preparer
	RuntimeInputs      *runtimeinputs.RuntimeInputs
	ImageReady         func(context.Context, string) error
}

func preparerReady(status *apigen.ScheduledInstanceStatus, seqNo int32) bool {
	return status != nil &&
		!status.Preparer.IsZero() &&
		status.Preparer.DeploymentConfigVersion == seqNo &&
		status.Preparer.Rollup() == apigen.PreparationStatus_READY
}

func configName(cfg *apigen.DeploymentConfig) string {
	if !cfg.Identity.IsZero() {
		return fmt.Sprintf("%d:%d:%s", cfg.Identity.SpaceID, cfg.NodeID, cfg.Identity.Name)
	}
	return fmt.Sprintf("id=%d", cfg.ID)
}

func (op DeploymentOperator) RunAll(predicate storage.ScheduledInstancePredicate) {
	deps, ch, _ := op.Store.MustFetchScheduledSnapshotAndSubscribe(predicate)
	subs := &pubsubu.PubSub[apigen.ScheduledInstanceState]{}

	slog.Info("RunAll: snapshot loaded", "count", len(deps))

	running := map[int32]struct{}{}
	// Subscribe before launching the operator goroutine so TERMINATE/FINALIZED
	// updates cannot be forwarded before the operator is listening.
	start := func(dep apigen.ScheduledInstanceState) {
		instanceID := dep.Instance.ID
		running[instanceID] = struct{}{}
		sub := subs.Subscribe(func(_, dws apigen.ScheduledInstanceState) bool {
			return dws.Instance.ID == instanceID
		})
		state := dep
		go op.Run(sub, &state)
	}

	for _, dep := range deps {
		slog.InfoContext(logu.ExtendLogContext(context.Background(), "scheduled_instance", dep.Instance.ID), "RunAll: launching operator from snapshot",
			"deployment", dep.Instance.DeploymentID,
			"name", configName(&dep.Config),
			"seqNo", dep.Instance.DeploymentVersion,
			"targetState", dep.Instance.State,
			"hasPreparer", !dep.Status.Preparer.IsZero(),
			"hasRunner", !dep.Status.Runner.IsZero(),
		)
		start(dep)
	}
	go func() {
		for {
			v, ok := <-ch
			if !ok {
				return
			}
			if _, ok := running[v.Instance.ID]; !ok {
				slog.InfoContext(logu.ExtendLogContext(context.Background(), "scheduled_instance", v.Instance.ID), "RunAll: launching operator for new scheduled instance",
					"deployment", v.Instance.DeploymentID,
					"name", configName(&v.Config),
					"seqNo", v.Instance.DeploymentVersion,
				)
				start(v)
				continue
			}
			subs.Notify(v)
		}
	}()
}

func (op DeploymentOperator) Run(
	sub *pubsubu.Sub[apigen.ScheduledInstanceState],
	initial *apigen.ScheduledInstanceState) {
	defer sub.UnsubscribeFunc()

	instanceID := initial.Instance.ID
	config := initial.Config
	status := initial.Status
	target := initial.Instance.State
	depName := configName(&config)
	ctx := logu.ExtendLogContext(context.Background(), "scheduled_instance", instanceID)
	ctx = logu.ExtendLogContext(ctx, "dep", config.ID)
	slog.InfoContext(ctx, "scheduled instance operator started", "name", depName)

	var currentPreparer *prepare.Handle
	var currentRunner runner.Runner
	shouldRun := target.WantsRunning()
	if shouldRun {
		slog.InfoContext(ctx, "Run: reattaching running preparer",
			"name", depName,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"configSeqNo", config.Version,
		)
		currentPreparer = op.reAttachPreparer(instanceID, &config, status.Preparer)
		slog.InfoContext(ctx, "Run: reattaching running runner",
			"name", depName,
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentRunner = runner.ReAttachRunning(op.Store, op.RuntimeInputs, instanceID, &config, status.Runner)
	} else {
		slog.InfoContext(ctx, "Run: initializing stopped/terminating instance",
			"name", depName,
			"targetState", target,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentPreparer = prepare.Finished(config.Version)
		currentRunner = runner.ReAttachStopped(op.Store, op.RuntimeInputs, instanceID, &config, status.Runner)
	}
	// A reattached placement that is already serving keeps its address across an
	// agent restart; one that is standing by or draining must not take it.
	if target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
		if err := currentRunner.Serve(); err != nil {
			slog.WarnContext(ctx, "Run: claiming the instance address on startup failed", "name", depName, "err", err)
		}
	}
	var candidate runner.RolloverCandidate
	var candidateReady <-chan rolloverCandidateResult
	artifactRepairPending := false
	artifactRepairStarted := false
	reconcile := func(update apigen.ScheduledInstanceState) bool {
		config = update.Config
		status = update.Status
		target = update.Instance.State
		if artifactRepairPending && status.Preparer.DeploymentConfigVersion == config.Version && status.Preparer.Rollup() != apigen.PreparationStatus_READY {
			artifactRepairStarted = true
		}
		switch {
		case target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED:
			slog.InfoContext(ctx, "Run: scheduled instance finalized, shutting down", "name", depName)
			currentPreparer.Cancel()
			if candidate != nil {
				candidate.Stop()
			}
			currentRunner.Stop()
			return false
		case target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE:
			slog.InfoContext(ctx, "Run: target terminate, stopping runner", "name", depName)
			if candidate != nil {
				candidate.Stop()
				candidate = nil
				candidateReady = nil
			}
			currentRunner.Stop()
			currentRunner = runner.Stopped()
			writeTerminalStopped(op.Store, instanceID, &config, &status)
		case target.WantsRunning() &&
			config.Version > currentPreparer.Version():
			slog.InfoContext(ctx, "Run: starting prepare",
				"name", depName,
				"configSeqNo", config.Version, "preparerSeqNo", currentPreparer.Version())
			if candidate != nil {
				candidate.Stop()
				candidate = nil
				candidateReady = nil
			}
			currentPreparer.Cancel()
			currentPreparer = op.startPreparer(instanceID, &config)
		case target.WantsRunning() &&
			preparerReady(&status, config.Version) && config.Version > currentRunner.Version() && candidate == nil && (!artifactRepairPending || artifactRepairStarted):
			slog.InfoContext(ctx, "Run: preparer ready, creating runner",
				"name", depName,
				"artifact", status.Preparer.Artifact, "configSeqNo", config.Version)
			if config.EffectiveUpgradeStrategy() == apigen.ContainerUpgradeStrategy_ROLLOVER {
				candidate = runner.CreateRolloverCandidate(op.Store, op.RuntimeInputs, instanceID, &config, &status)
				candidateReady = waitForRolloverCandidate(candidate, config.Version)
				return true
			}
			currentRunner.Stop()
			currentRunner = runner.Create(op.Store, op.RuntimeInputs, instanceID, &config, &status)
			artifactRepairPending = false
			artifactRepairStarted = false
		default:
			slog.DebugContext(ctx, "Run: nothing to do on update", "name", depName)
		}
		// Claiming the instance address is driven by target state, not by local
		// readiness, so that only one node ever holds the route. Serve is
		// idempotent, so running it on every serving update also repairs a claim
		// that failed earlier without needing to track that it did.
		if target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
			if err := currentRunner.Serve(); err != nil {
				slog.WarnContext(ctx, "Run: claiming the instance address failed", "name", depName, "err", err)
			}
		}
		return true
	}

	if !reconcile(*initial) {
		return
	}

	for {
		select {
		case <-currentRunner.ArtifactMissing():
			if !target.WantsRunning() {
				continue
			}
			slog.WarnContext(ctx, "Run: local artifact missing, preparing current config again", "name", depName, "configSeqNo", config.Version)
			if candidate != nil {
				candidate.Stop()
				candidate = nil
				candidateReady = nil
			}
			currentRunner.Stop()
			currentRunner = runner.Stopped()
			currentPreparer.Cancel()
			currentPreparer = op.startPreparer(instanceID, &config)
			artifactRepairPending = true
			artifactRepairStarted = false
		case result := <-candidateReady:
			if candidate == nil || result.candidate != candidate {
				continue
			}
			if result.err != nil {
				slog.WarnContext(ctx, "Run: rollover candidate failed readiness", "name", depName, "configSeqNo", result.version, "err", result.err)
				artifactMissing := errors.Is(result.err, ctrd.ErrImageUnavailable)
				candidate.Stop()
				candidate = nil
				candidateReady = nil
				if artifactMissing && target.WantsRunning() {
					currentPreparer.Cancel()
					currentPreparer = op.startPreparer(instanceID, &config)
					artifactRepairPending = true
					artifactRepairStarted = false
				}
				continue
			}
			slog.InfoContext(ctx, "Run: rollover candidate ready, promoting candidate", "name", depName, "configSeqNo", result.version)
			if err := candidate.Promote(); err != nil {
				slog.WarnContext(ctx, "Run: rollover candidate promotion failed", "name", depName, "configSeqNo", result.version, "err", err)
				candidate.Stop()
				candidate = nil
				candidateReady = nil
				continue
			}
			slog.InfoContext(ctx, "Run: rollover candidate promoted", "name", depName, "configSeqNo", result.version)
			currentRunner = candidate
			candidate = nil
			candidateReady = nil
			artifactRepairPending = false
			artifactRepairStarted = false
		case update, ok := <-sub.Ch:
			if !ok {
				return
			}
			if !reconcile(update) {
				return
			}
		}
	}
}

func (op DeploymentOperator) startPreparer(instanceID int32, dep *apigen.DeploymentConfig) *prepare.Handle {
	if dep.WorkloadVersion() == "" {
		prepare.WriteStatus(op.Store, instanceID, dep, prepare.StatusUpdate{Inputs: apigen.InputsStatus_INPUTS_FAILED})
		return prepare.Finished(dep.Version)
	}
	handle, ctx := prepare.NewHandle(dep.Version)
	ctx = logu.ExtendLogContext(ctx, "scheduled_instance", instanceID)
	ctx = logu.ExtendLogContext(ctx, "dep", dep.ID)
	go func() {
		defer handle.Complete()
		prepare.WriteStatus(op.Store, instanceID, dep, op.prepare(ctx, instanceID, dep))
	}()
	return handle
}

// prepare runs the two preparation stages in order and returns the terminal
// status. Each stage publishes its own progress as it goes; the caller publishes
// what comes back here.
//
// Inputs run first so a cheap, commonly-failing check (a secret the primary has
// not distributed yet) fails before committing to a build that can take minutes.
// The stages are otherwise independent.
func (op DeploymentOperator) prepare(ctx context.Context, instanceID int32, dep *apigen.DeploymentConfig) prepare.StatusUpdate {
	ctx = logu.ExtendLogContext(ctx, "dep", dep.ID)
	log, logPath, err := preparerlog.New(ctx, dep)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "path", logPath, "err", err)
		return prepare.StatusUpdate{Inputs: apigen.InputsStatus_INPUTS_FAILED}
	}
	defer log.Close()

	prepare.WriteStatus(op.Store, instanceID, dep, prepare.StatusUpdate{Inputs: apigen.InputsStatus_INPUTS_RESOLVING})
	log.Write("resolving runtime inputs")
	if err := op.RuntimeInputs.EnsureReady(ctx, dep); err != nil {
		log.Error("resolving runtime inputs: %v", err)
		return prepare.StatusUpdate{Inputs: apigen.InputsStatus_INPUTS_FAILED}
	}
	log.Write("runtime inputs ready")

	return op.prepareImage(ctx, instanceID, dep, log)
}

// prepareImage runs stage 2. Inputs are ready by the time it is called, so every
// status it publishes carries INPUTS_READY alongside the image progress.
func (op DeploymentOperator) prepareImage(ctx context.Context, instanceID int32, dep *apigen.DeploymentConfig, log *preparerlog.Log) prepare.StatusUpdate {
	type imagePreparer func(context.Context, *apigen.DeploymentConfig, *preparerlog.Log) (string, apigen.ImageStatus)

	var (
		started apigen.ImageStatus
		run     imagePreparer
	)
	container := dep.Spec.Container()
	switch {
	case dep.Spec.SystemdSpec != nil && dep.Spec.SystemdSpec.Source != nil:
		started, run = apigen.ImageStatus_IMAGE_DOWNLOADING, op.GithubRelease.Prepare
	case container != nil && container.Source.NixDockerBuild != nil:
		started, run = apigen.ImageStatus_IMAGE_BUILDING, op.NixDocker.Prepare
	case container != nil && container.Source.RemoteImage != nil && container.Source.RemoteImage.Image == internaldeploy.NetproxyImage:
		started, run = apigen.ImageStatus_IMAGE_DOWNLOADING, op.GithubReleaseImage.Prepare
	case container != nil && container.Source.RemoteImage != nil:
		started, run = apigen.ImageStatus_IMAGE_PULLING, containerimage.Prepare
	default:
		log.Error("no prepare config found")
		return prepare.StatusUpdate{
			Inputs: apigen.InputsStatus_INPUTS_READY,
			Image:  apigen.ImageStatus_IMAGE_FAILED,
		}
	}

	prepare.WriteStatus(op.Store, instanceID, dep, prepare.StatusUpdate{
		Inputs: apigen.InputsStatus_INPUTS_READY,
		Image:  started,
	})
	artifact, status := run(ctx, dep, log)
	return prepare.StatusUpdate{
		Artifact: artifact,
		Inputs:   apigen.InputsStatus_INPUTS_READY,
		Image:    status,
	}
}

func (op DeploymentOperator) reAttachPreparer(instanceID int32, dep *apigen.DeploymentConfig, prev apigen.PreparerStatus) *prepare.Handle {
	ctx := logu.ExtendLogContext(context.Background(), "scheduled_instance", instanceID)
	ctx = logu.ExtendLogContext(ctx, "dep", dep.ID)
	if prev.DeploymentConfigVersion == dep.Version && prev.Rollup() == apigen.PreparationStatus_READY {
		// The image check comes first because it is local and decisive: a missing
		// image genuinely needs the artifact rebuilt. Runtime inputs are checked
		// after, so that a primary that is briefly unreachable cannot mask it.
		if dep.Spec.SystemdSpec == nil {
			imageReady := op.ImageReady
			if imageReady == nil {
				imageReady = ctrd.Default.ImageReady
			}
			if err := imageReady(ctx, prev.Artifact); err != nil {
				slog.WarnContext(ctx, "reAttachPreparer: prepared image unavailable, preparing current config again", "configVersion", dep.Version, "artifact", prev.Artifact, "err", err)
				return op.startPreparer(instanceID, dep)
			}
		}
		if err := op.RuntimeInputs.EnsureReady(ctx, dep); err != nil {
			slog.WarnContext(ctx, "reAttachPreparer: prepared runtime inputs unavailable, retrying in the background", "configVersion", dep.Version, "err", err)
			return op.retryRuntimeInputs(instanceID, dep, prev)
		}
		return prepare.Finished(dep.Version)
	}
	if dep.WorkloadVersion() == "" {
		slog.InfoContext(ctx, "reAttachPreparer: no version to prepare", "deploymentConfigVersion", dep.Version)
		return prepare.Finished(dep.Version)
	}
	if dep.Spec.SystemdSpec != nil && prev.IsZero() && dep.WorkloadVersion() == version.Version {
		slog.InfoContext(ctx, "reAttachPreparer: current systemd build already installed", "deploymentConfigVersion", dep.Version, "workloadVersion", dep.WorkloadVersion())
		return prepare.Finished(dep.Version)
	}
	// Empty/mismatched preparer status means this scheduled instance has not
	// completed prepare yet. Always prepare — including systemd/OpenDeploy
	// self-upgrades — so a new instance cannot ack RUNNING without install.
	return op.startPreparer(instanceID, dep)
}

// newRuntimeInputsBackoff paces runtime input retries: 2s, doubling to a minute.
// No reset duration, so the interval keeps growing for as long as the failure
// lasts rather than snapping back between attempts. A var so tests can shrink it.
var newRuntimeInputsBackoff = func() *timeu.Backoff {
	return timeu.NewExpBackoff(time.Minute, 0)
}

// retryRuntimeInputs keeps fetching an already-prepared instance's runtime
// inputs until they are available, leaving its READY preparer status alone.
//
// The artifact is built and recorded; what failed is input distribution, which
// on a worker is a request to the primary. Re-preparing cannot help — prepare
// fetches the same inputs from the same place — and it would publish PREPARING
// and then FAILED, a state nothing retries out of, so a worker that restarted
// while the primary was down stayed failed until someone edited the config. That
// is the ordinary shape of a rollout: the operator starts before the primary
// connection is established, so every instance holding a secret or config ref
// fetches against a primary that may not be listening yet.
//
// The retry is load-bearing rather than cosmetic. Nothing else refills the
// in-memory secret and config cache — EnsureReady is the only writer, and the
// container runner resolves env references from it on every respawn — so an
// instance that crashed after a failed fetch would otherwise loop on
// unresolvable references indefinitely.
//
// The handle carries the current config version, so the operator treats this
// instance as prepared and cancels the loop the moment the config moves on or
// the instance is finalized.
//
// The retry publishes the inputs stage, which is why the rollup ranks a READY
// image above a resolving inputs stage: the recorded artifact and the READY
// rollup that gates the runner both survive, and the reason the instance is
// stuck is finally visible instead of being invisible as it was when the
// preparer had only one status to write.
func (op DeploymentOperator) retryRuntimeInputs(instanceID int32, dep *apigen.DeploymentConfig, prev apigen.PreparerStatus) *prepare.Handle {
	handle, ctx := prepare.NewHandle(dep.Version)
	ctx = logu.ExtendLogContext(ctx, "scheduled_instance", instanceID)
	ctx = logu.ExtendLogContext(ctx, "dep", dep.ID)
	// Callers reach here only for an instance whose rollup is READY and whose
	// artifact is present, so the image stage is asserted rather than copied from
	// prev — that also backfills statuses persisted before the stages existed.
	inputsStage := func(inputs apigen.InputsStatus) prepare.StatusUpdate {
		return prepare.StatusUpdate{
			Artifact: prev.Artifact,
			Inputs:   inputs,
			Image:    apigen.ImageStatus_IMAGE_READY,
		}
	}
	go func() {
		defer handle.Complete()
		prepare.WriteStatus(op.Store, instanceID, dep, inputsStage(apigen.InputsStatus_INPUTS_RESOLVING))
		backoff := newRuntimeInputsBackoff()
		for {
			backoff.WaitWithContext(ctx)
			if ctx.Err() != nil {
				return
			}
			if err := op.RuntimeInputs.EnsureReady(ctx, dep); err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.WarnContext(ctx, "retryRuntimeInputs: still unavailable", "configVersion", dep.Version, "waited", backoff.CurrentDuration, "err", err)
				continue
			}
			slog.InfoContext(ctx, "retryRuntimeInputs: runtime inputs recovered", "configVersion", dep.Version)
			prepare.WriteStatus(op.Store, instanceID, dep, inputsStage(apigen.InputsStatus_INPUTS_READY))
			return
		}
	}()
	return handle
}

type rolloverCandidateResult struct {
	version   int32
	candidate runner.RolloverCandidate
	err       error
}

func waitForRolloverCandidate(candidate runner.RolloverCandidate, version int32) <-chan rolloverCandidateResult {
	ch := make(chan rolloverCandidateResult, 1)
	go func() {
		ch <- rolloverCandidateResult{version: version, candidate: candidate, err: candidate.WaitReady()}
	}()
	return ch
}

func containerUpgradeStrategy(config *apigen.DeploymentConfig) apigen.ContainerUpgradeStrategy {
	if config == nil {
		return apigen.ContainerUpgradeStrategy_RECREATE
	}
	return config.EffectiveUpgradeStrategy()
}

func writeTerminalStopped(store storage.OperatorStore, instanceID int32, dep *apigen.DeploymentConfig, status *apigen.ScheduledInstanceStatus) {
	if status != nil && status.Runner.Status == apigen.RunningStatus_STOPPED {
		return
	}
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		if s.Runner.Status == apigen.RunningStatus_STOPPED {
			return false
		}
		s.BumpUpdatedAt()
		s.ScheduledInstanceID = instanceID
		s.DeploymentID = dep.ID
		runnerStatus := s.Runner
		if runnerStatus.IsZero() && status != nil {
			runnerStatus = status.Runner
		}
		if runnerStatus.DeploymentConfigVersion == 0 {
			runnerStatus.DeploymentConfigVersion = dep.Version
		}
		runnerStatus.Status = apigen.RunningStatus_STOPPED
		runnerStatus.RunningPid = 0
		s.Runner = runnerStatus
		return true
	})
}

func fmtPreparerStatus(p apigen.PreparerStatus) string {
	if p.IsZero() {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v inputs=%v image=%v artifact=%q", p.DeploymentConfigVersion, p.Rollup(), p.Inputs, p.Image, p.Artifact)
}

func fmtRunnerStatus(r apigen.RunnerStatus) string {
	if r.IsZero() {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v pid=%d artifact=%q", r.DeploymentConfigVersion, r.Status, r.RunningPid, r.RunningArtifact)
}
