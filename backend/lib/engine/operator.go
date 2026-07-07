package engine

import (
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/lib/engine/preparer"
	"github.com/jptrs93/opsagent/backend/lib/engine/runner"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

type DeploymentOperator struct {
	Store storage.OperatorStore
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

func (op DeploymentOperator) RunAll(machine string) {
	deps, ch, _ := op.Store.MustFetchSnapshotAndSubscribe(machine)
	subs := &pubsubu.PubSub[apigen.DeploymentWithStatus]{}

	slog.Info("RunAll: snapshot loaded", "count", len(deps), "machine", machine)

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
	var currentPreparer preparer.Preparer
	var currentRunner runner.Runner
	if config.DesiredState.Running {
		slog.Info("Run: reattaching running preparer",
			"dep", depName,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"configSeqNo", config.Version,
		)
		currentPreparer = preparer.ReAttach(op.Store, config, status.Preparer)
		slog.Info("Run: reattaching running runner",
			"dep", depName,
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentRunner = runner.ReAttachRunning(op.Store, config, status.Runner)
	} else {
		slog.Info("Run: initializing stopped deployment",
			"dep", depName,
			"preparerStatus", fmtPreparerStatus(status.Preparer),
			"runnerStatus", fmtRunnerStatus(status.Runner),
			"configSeqNo", config.Version,
		)
		currentPreparer = preparer.Idle(config.Version)
		currentRunner = runner.ReAttachStopped(op.Store, config, status.Runner)
	}
	var candidate runner.RolloverCandidate
	var candidateReady <-chan rolloverCandidateResult

	// Reconciliation loop.
	for {
		select {
		case result := <-candidateReady:
			if candidate == nil || result.candidate != candidate {
				continue
			}
			if result.err != nil {
				slog.Warn("Run: rollover candidate failed readiness", "dep", depName, "configSeqNo", result.version, "err", result.err)
				candidate.Stop()
				candidate = nil
				candidateReady = nil
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
		case update, ok := <-sub.Ch:
			if !ok {
				return
			}
			config := update.Config
			status := update.Status
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
				currentPreparer = preparer.StartPrepare(op.Store, &config)
			case preparerReady(&status, config.Version) && config.Version > currentRunner.Version() && candidate == nil:
				slog.Info("Run: preparer ready, creating runner",
					"dep", depName,
					"artifact", status.Preparer.Artifact, "configSeqNo", config.Version)
				if containerUpgradeStrategy(&config) == apigen.ContainerUpgradeStrategy_ROLLOVER {
					candidate = runner.CreateRolloverCandidate(op.Store, &config, &status)
					candidateReady = waitForRolloverCandidate(candidate, config.Version)
					continue
				}
				currentRunner.Stop()
				currentRunner = runner.Create(op.Store, &config, &status)
			default:
				slog.Debug("Run: nothing to do on update", "dep", depName)
			}
		}
	}
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
