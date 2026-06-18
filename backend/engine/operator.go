package engine

import (
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/runner"
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

	sub := subs.Subscribe(func(dws apigen.DeploymentWithStatus) bool {
		return dws.Config.ID == id
	})
	slog.Info("Run: reattaching preparer",
		"dep", depName,
		"preparerStatus", fmtPreparerStatus(status.Preparer),
		"configSeqNo", config.Version,
	)
	var currentPreparer preparer.Preparer = preparer.ReAttach(op.Store, config, status.Preparer)
	slog.Info("Run: reattaching runner",
		"dep", depName,
		"runnerStatus", fmtRunnerStatus(status.Runner),
		"configSeqNo", config.Version,
	)
	var currentRunner runner.Runner = runner.ReAttach(op.Store, config, status.Runner)

	// Reconciliation loop.
	for {
		update, ok := <-sub.Ch
		if !ok {
			return
		}
		config := update.Config
		status := update.Status
		switch {
		case config.Deleted:
			slog.Info("Run: deployment deleted, shutting down", "dep", depName)
			currentPreparer.Cancel()
			currentRunner.Stop()
			sub.UnsubscribeFunc()
			return
		case !config.DesiredState.Running:
			slog.Info("Run: desired running=false, stopping runner", "dep", depName)
			currentRunner.Stop()
		case config.Version > currentPreparer.Version() && config.DesiredState.Running:
			slog.Info("Run: config ahead of preparer, starting new prepare",
				"dep", depName,
				"configSeqNo", config.Version, "preparerSeqNo", currentPreparer.Version())
			currentPreparer.Cancel()
			currentPreparer = preparer.StartPrepare(op.Store, &config)
		case preparerReady(&status, config.Version) && config.Version > currentRunner.Version():
			slog.Info("Run: preparer ready, creating runner",
				"dep", depName,
				"artifact", status.Preparer.Artifact, "configSeqNo", config.Version)
			currentRunner.Stop()
			currentRunner = runner.Create(op.Store, &config, &status)
		default:
			slog.Debug("Run: nothing to do on update", "dep", depName)
		}
	}
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
