package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/runner"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/logstore"
)

type DeploymentOperator struct {
	Store storage.OperatorStore
}

// preparerReady returns true when the preparer has produced a READY artifact
// for the given deployment seq number.
func preparerReady(status *apigen.DeploymentStatus, seqNo int32) bool {
	return status.Preparer != nil &&
		status.Preparer.DeploymentConfigVersion == seqNo &&
		status.Preparer.Status == apigen.PreparationStatus_READY
}

func configName(cfg *apigen.DeploymentConfig) string {
	if cfg.ConfigID != nil {
		return fmt.Sprintf("%s:%s:%s", cfg.ConfigID.Environment, cfg.ConfigID.Machine, cfg.ConfigID.Name)
	}
	return fmt.Sprintf("id=%d", cfg.ID)
}

func (op DeploymentOperator) RunAll(ctx context.Context, machine string) {
	deps, ch := op.Store.MustFetchSnapshotAndSubscribe(ctx, machine)
	subs := &logstore.Subs[apigen.DeploymentWithStatus]{}

	slog.InfoContext(ctx, "RunAll: snapshot loaded", "count", len(deps), "machine", machine)

	running := map[int32]struct{}{}
	for _, dep := range deps {
		running[dep.Config.ID] = struct{}{}
		slog.InfoContext(ctx, "RunAll: launching operator from snapshot",
			"id", dep.Config.ID,
			"name", configName(dep.Config),
			"seqNo", dep.Config.Version,
			"desiredRunning", dep.Config.DesiredState.Running,
			"desiredVersion", dep.Config.DesiredState.Version,
			"hasPreparer", dep.Status.Preparer != nil,
			"hasRunner", dep.Status.Runner != nil,
		)
		go op.Run(ctx, subs, dep.Config, dep.Status)
	}
	go func() {
		for {
			select {
			case v := <-ch:
				if _, ok := running[v.Config.ID]; !ok {
					running[v.Config.ID] = struct{}{}
					slog.InfoContext(ctx, "RunAll: launching operator for new deployment",
						"id", v.Config.ID,
						"name", configName(v.Config),
						"seqNo", v.Config.Version,
					)
					go op.Run(ctx, subs, v.Config, v.Status)
				}
				subs.Notify(v)
			case <-ctx.Done():
				return
			}
		}
	}()

}

func (op DeploymentOperator) Run(
	ctx context.Context,
	subs *logstore.Subs[apigen.DeploymentWithStatus],
	config *apigen.DeploymentConfig,
	status *apigen.DeploymentStatus) {
	id := config.ID
	ctx = logu.ExtendLogContext(ctx, "dep", configName(config))
	slog.InfoContext(ctx, "deployment operator started")

	sub, unsubFunc := subs.Subscribe(func(dws apigen.DeploymentWithStatus) bool {
		return dws.Config.ID == id
	})
	slog.InfoContext(ctx, "Run: reattaching preparer",
		"preparerStatus", fmtPreparerStatus(status.Preparer),
		"configSeqNo", config.Version,
	)
	var currentPreparer preparer.Preparer = preparer.ReAttach(ctx, op.Store, config, status.Preparer)
	slog.InfoContext(ctx, "Run: reattaching runner",
		"runnerStatus", fmtRunnerStatus(status.Runner),
		"configSeqNo", config.Version,
	)
	var currentRunner runner.Runner = runner.ReAttach(ctx, op.Store, config, status.Runner)

	// Reconciliation loop.
	for {
		select {
		case update := <-sub.Ch:
			config := update.Config
			status := update.Status
			switch {
			case config.Deleted:
				slog.InfoContext(ctx, "Run: deployment deleted, shutting down")
				currentPreparer.Cancel()
				currentRunner.Stop()
				unsubFunc()
				return
			case !config.DesiredState.Running:
				slog.InfoContext(ctx, "Run: desired running=false, stopping runner")
				currentRunner.Stop()
			case config.Version > currentPreparer.Version() && config.DesiredState.Running:
				slog.InfoContext(ctx, "Run: config ahead of preparer, starting new prepare",
					"configSeqNo", config.Version, "preparerSeqNo", currentPreparer.Version())
				currentPreparer.Cancel()
				currentPreparer = preparer.StartPrepare(op.Store, config)
			case preparerReady(status, config.Version) && config.Version > currentRunner.Version():
				slog.InfoContext(ctx, "Run: preparer ready, creating runner",
					"artifact", status.Preparer.Artifact, "configSeqNo", config.Version)
				currentRunner.Stop()
				currentRunner = runner.Create(ctx, op.Store, config, status)
			default:
				slog.DebugContext(ctx, "Run: nothing to do on update")
			}
		case <-ctx.Done():
			slog.InfoContext(ctx, "graceful exit on context end")
			unsubFunc()
			return
		}
	}
}

func fmtPreparerStatus(p *apigen.PreparerStatus) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v artifact=%q", p.DeploymentConfigVersion, p.Status, p.Artifact)
}

func fmtRunnerStatus(r *apigen.RunnerStatus) string {
	if r == nil {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v pid=%d artifact=%q", r.DeploymentConfigVersion, r.Status, r.RunningPid, r.RunningArtifact)
}
