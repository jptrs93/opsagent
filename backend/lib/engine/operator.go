package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/pubsubu"
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
)

type DeploymentOperator struct {
	Store              storage.OperatorStore
	GithubRelease      *githubrelease.Preparer
	NixDocker          *nixdocker.Preparer
	GithubReleaseImage *githubreleaseimage.Preparer
	RuntimeInputs      *runtimeinputs.RuntimeInputs
	ImageReady         func(context.Context, string) error
}

// preparerReady returns true when the preparer has produced a READY artifact
// for the given deployment seq number.
func preparerReady(status *apigen.DeploymentStatus, seqNo int32) bool {
	return status != nil &&
		!status.Preparer.IsZero() &&
		status.Preparer.DeploymentConfigVersion == seqNo &&
		status.Preparer.Status == apigen.PreparationStatus_READY
}

func configName(cfg *apigen.DeploymentConfig) string {
	if !cfg.ConfigID.IsZero() {
		return fmt.Sprintf("%d:%s:%s", cfg.ConfigID.SpaceID, cfg.ConfigID.Machine, cfg.ConfigID.Name)
	}
	return fmt.Sprintf("id=%d", cfg.ID)
}

func (op DeploymentOperator) RunAll(predicate storage.DeploymentPredicate) {
	deps, ch, _ := op.Store.MustFetchSnapshotAndSubscribe(predicate)
	subs := &pubsubu.PubSub[apigen.DeploymentWithStatus]{}

	slog.Info("RunAll: snapshot loaded", "count", len(deps))

	running := map[int32]struct{}{}
	for _, dep := range deps {
		running[dep.Config.ID] = struct{}{}
		slog.Info("RunAll: launching operator from snapshot",
			"id", dep.Config.ID,
			"name", configName(&dep.Config),
			"seqNo", dep.Config.Version,
			"desiredRunning", dep.Config.DesiredState.Running,
			"desiredVersion", dep.Config.DesiredState.Version,
			"hasPreparer", !dep.Status.Preparer.IsZero(),
			"hasRunner", !dep.Status.Runner.IsZero(),
		)
		config := dep.Config
		status := dep.Status
		go op.Run(subs, &config, &status)
	}
	go func() {
		for {
			v, ok := <-ch
			if !ok {
				return
			}
			if _, ok := running[v.Config.ID]; !ok {
				running[v.Config.ID] = struct{}{}
				slog.Info("RunAll: launching operator for new deployment",
					"id", v.Config.ID,
					"name", configName(&v.Config),
					"seqNo", v.Config.Version,
				)
				config := v.Config
				status := v.Status
				go op.Run(subs, &config, &status)
			}
			subs.Notify(v)
		}
	}()

}

func (op DeploymentOperator) Run(
	subs *pubsubu.PubSub[apigen.DeploymentWithStatus],
	config *apigen.DeploymentConfig,
	status *apigen.DeploymentStatus) {
	id := config.ID
	depName := configName(config)
	slog.Info("deployment operator started", "dep", depName)

	sub := subs.Subscribe(func(prev, dws apigen.DeploymentWithStatus) bool {
		return dws.Config.ID == id
	})
	var currentPreparer *prepare.Handle
	var currentRunner runner.Runner
	if config.DesiredState.Running {
		slog.Info("Run: reattaching running preparer",
			"dep", depName,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"configSeqNo", config.Version,
		)
		currentPreparer = op.reAttachPreparer(config, status.Preparer)
		slog.Info("Run: reattaching running runner",
			"dep", depName,
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentRunner = runner.ReAttachRunning(op.Store, op.RuntimeInputs, config, status.Runner)
	} else {
		slog.Info("Run: initializing stopped deployment",
			"dep", depName,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentPreparer = prepare.Finished(config.Version)
		currentRunner = runner.ReAttachStopped(op.Store, op.RuntimeInputs, config, status.Runner)
	}
	var candidate runner.RolloverCandidate
	var candidateReady <-chan rolloverCandidateResult
	artifactRepairPending := false
	artifactRepairStarted := false

	// Reconciliation loop.
	for {
		select {
		case <-currentRunner.ArtifactMissing():
			if !config.DesiredState.Running {
				continue
			}
			slog.Warn("Run: local artifact missing, preparing current config again", "dep", depName, "configSeqNo", config.Version)
			if candidate != nil {
				candidate.Stop()
				candidate = nil
				candidateReady = nil
			}
			currentRunner.Stop()
			currentRunner = runner.Stopped()
			currentPreparer.Cancel()
			currentPreparer = op.startPreparer(config)
			artifactRepairPending = true
			artifactRepairStarted = false
		case result := <-candidateReady:
			if candidate == nil || result.candidate != candidate {
				continue
			}
			if result.err != nil {
				slog.Warn("Run: rollover candidate failed readiness", "dep", depName, "configSeqNo", result.version, "err", result.err)
				artifactMissing := errors.Is(result.err, ctrd.ErrImageUnavailable)
				candidate.Stop()
				candidate = nil
				candidateReady = nil
				if artifactMissing && config.DesiredState.Running {
					currentPreparer.Cancel()
					currentPreparer = op.startPreparer(config)
					artifactRepairPending = true
					artifactRepairStarted = false
				}
				continue
			}
			slog.Info("Run: rollover candidate ready, promoting candidate", "dep", depName, "configSeqNo", result.version)
			if err := candidate.Promote(); err != nil {
				slog.Warn("Run: rollover candidate promotion failed", "dep", depName, "configSeqNo", result.version, "err", err)
				candidate.Stop()
				candidate = nil
				candidateReady = nil
				continue
			}
			slog.Info("Run: rollover candidate promoted, stopping old runner", "dep", depName, "configSeqNo", result.version)
			currentRunner.Stop()
			currentRunner = candidate
			candidate = nil
			candidateReady = nil
			artifactRepairPending = false
			artifactRepairStarted = false
		case update, ok := <-sub.Ch:
			if !ok {
				return
			}
			config = &update.Config
			status = &update.Status
			if artifactRepairPending && status.Preparer.DeploymentConfigVersion == config.Version && status.Preparer.Status != apigen.PreparationStatus_READY {
				artifactRepairStarted = true
			}
			switch {
			case config.Deleted:
				slog.Info("Run: deployment deleted, shutting down", "dep", depName)
				currentPreparer.Cancel()
				if candidate != nil {
					candidate.Stop()
				}
				currentRunner.Stop()
				sub.UnsubscribeFunc()
				return
			case !config.DesiredState.Running:
				slog.Info("Run: desired running=false, stopping runner", "dep", depName)
				if candidate != nil {
					candidate.Stop()
					candidate = nil
					candidateReady = nil
				}
				currentRunner.Stop()
			case config.Version > currentPreparer.Version() && config.DesiredState.Running:
				slog.Info("Run: config ahead of preparer, starting new prepare",
					"dep", depName,
					"configSeqNo", config.Version, "preparerSeqNo", currentPreparer.Version())
				if candidate != nil {
					candidate.Stop()
					candidate = nil
					candidateReady = nil
				}
				currentPreparer.Cancel()
				currentPreparer = op.startPreparer(config)
			case preparerReady(status, config.Version) && config.Version > currentRunner.Version() && candidate == nil && (!artifactRepairPending || artifactRepairStarted):
				slog.Info("Run: preparer ready, creating runner",
					"dep", depName,
					"artifact", status.Preparer.Artifact, "configSeqNo", config.Version)
				if containerUpgradeStrategy(config) == apigen.ContainerUpgradeStrategy_ROLLOVER {
					candidate = runner.CreateRolloverCandidate(op.Store, op.RuntimeInputs, config, status)
					candidateReady = waitForRolloverCandidate(candidate, config.Version)
					continue
				}
				currentRunner.Stop()
				currentRunner = runner.Create(op.Store, op.RuntimeInputs, config, status)
				artifactRepairPending = false
				artifactRepairStarted = false
			default:
				slog.Debug("Run: nothing to do on update", "dep", depName)
			}
		}
	}
}

func (op DeploymentOperator) startPreparer(dep *apigen.DeploymentConfig) *prepare.Handle {
	if dep.DesiredState.Version == "" {
		prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_FAILED)
		return prepare.Finished(dep.Version)
	}
	handle, ctx := prepare.NewHandle(dep.Version)
	go func() {
		defer handle.Complete()
		artifact, status := op.prepare(ctx, dep)
		prepare.WriteStatus(op.Store, dep, artifact, status)
	}()
	return handle
}

func (op DeploymentOperator) prepare(ctx context.Context, dep *apigen.DeploymentConfig) (string, apigen.PreparationStatus) {
	log, logPath, err := preparerlog.New(ctx, dep)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "path", logPath, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	defer log.Close()

	prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_PREPARING)
	log.Write("preparing runtime inputs")
	if err := op.RuntimeInputs.EnsureReady(ctx, dep); err != nil {
		log.Error("preparing runtime inputs: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("runtime inputs ready")

	switch {
	case dep.Spec.Prepare.GithubRelease != nil:
		prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_DOWNLOADING)
		return op.GithubRelease.Prepare(ctx, dep, log)
	case dep.Spec.Prepare.NixDockerBuild != nil:
		prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_PREPARING)
		return op.NixDocker.Prepare(ctx, dep, log)
	case dep.Spec.Prepare.ContainerImage != nil && dep.Spec.Prepare.ContainerImage.Image == internaldeploy.NetproxyImage:
		prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_PREPARING)
		return op.GithubReleaseImage.Prepare(ctx, dep, log)
	case dep.Spec.Prepare.ContainerImage != nil:
		prepare.WriteStatus(op.Store, dep, "", apigen.PreparationStatus_PULLING)
		return containerimage.Prepare(ctx, dep, log)
	default:
		log.Error("no prepare config found")
		return "", apigen.PreparationStatus_FAILED
	}
}

// reAttachPreparer restores the preparation side of an operator after process
// startup. Preparations themselves are not resumable: completed artifacts are
// reused after checking runtime inputs, while incomplete work starts again.
func (op DeploymentOperator) reAttachPreparer(dep *apigen.DeploymentConfig, prev apigen.PreparerStatus) *prepare.Handle {
	if prev.DeploymentConfigVersion == dep.Version && prev.Status == apigen.PreparationStatus_READY {
		if err := op.RuntimeInputs.EnsureReady(context.Background(), dep); err != nil {
			slog.Error("reAttachPreparer: prepared runtime inputs unavailable", "configVersion", dep.Version, "err", err)
			return op.startPreparer(dep)
		}
		if dep.Spec.Runner.Systemd.IsZero() {
			imageReady := op.ImageReady
			if imageReady == nil {
				imageReady = ctrd.Default.ImageReady
			}
			if err := imageReady(context.Background(), prev.Artifact); err != nil {
				slog.Warn("reAttachPreparer: prepared image unavailable, preparing current config again", "configVersion", dep.Version, "artifact", prev.Artifact, "err", err)
				return op.startPreparer(dep)
			}
		}
		return prepare.Finished(dep.Version)
	}
	if !dep.Spec.Runner.Systemd.IsZero() && prev.IsZero() && dep.DesiredState.Version != "" {
		slog.Info("reAttachPreparer: systemd deployment already installed", "deploymentConfigVersion", dep.Version)
		return prepare.Finished(dep.Version)
	}
	if dep.DesiredState.Version == "" {
		slog.Info("reAttachPreparer: no version to prepare", "deploymentConfigVersion", dep.Version)
		return prepare.Finished(dep.Version)
	}
	return op.startPreparer(dep)
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
	strategy := config.Spec.Runner.Container.UpgradeStrategy
	if strategy == apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		return apigen.ContainerUpgradeStrategy_RECREATE
	}
	return strategy
}

func fmtPreparerStatus(p apigen.PreparerStatus) string {
	if p.IsZero() {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v artifact=%q", p.DeploymentConfigVersion, p.Status, p.Artifact)
}

func fmtRunnerStatus(r apigen.RunnerStatus) string {
	if r.IsZero() {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v pid=%d artifact=%q", r.DeploymentConfigVersion, r.Status, r.RunningPid, r.RunningArtifact)
}
