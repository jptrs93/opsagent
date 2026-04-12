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
		status.Preparer.DeploymentSeqNo == seqNo &&
		status.Preparer.Status == apigen.PreparationStatus_READY
}

func (op DeploymentOperator) RunAll(ctx context.Context, machine string) {
	deps, ch := op.Store.MustFetchSnapshotAndSubscribe(ctx, machine)
	subs := &logstore.Subs[apigen.DeploymentWithStatus]{}

	slog.InfoContext(ctx, "RunAll: snapshot loaded", "count", len(deps), "machine", machine)

	running := map[apigen.DeploymentIdentifier]struct{}{}
	for _, dep := range deps {
		running[*dep.Config.ID] = struct{}{}
		slog.InfoContext(ctx, "RunAll: launching operator from snapshot",
			"id", fmt.Sprintf("%s:%s:%s", dep.Config.ID.Environment, dep.Config.ID.Machine, dep.Config.ID.Name),
			"seqNo", dep.Config.SeqNo,
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
				if _, ok := running[*v.Config.ID]; !ok {
					running[*v.Config.ID] = struct{}{}
					slog.InfoContext(ctx, "RunAll: launching operator for new deployment",
						"id", fmt.Sprintf("%s:%s:%s", v.Config.ID.Environment, v.Config.ID.Machine, v.Config.ID.Name),
						"seqNo", v.Config.SeqNo,
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
	ctx = logu.ExtendLogContext(ctx, "dep", fmt.Sprintf("%v:%v", id.Environment, id.Name))
	slog.InfoContext(ctx, "deployment operator started")

	sub, unsubFunc := subs.Subscribe(func(dws apigen.DeploymentWithStatus) bool {
		return *dws.Config.ID == *id
	})
	slog.InfoContext(ctx, "Run: reattaching preparer",
		"preparerStatus", fmtPreparerStatus(status.Preparer),
		"configSeqNo", config.SeqNo,
	)
	var currentPreparer preparer.Preparer2 = preparer.ReAttach(ctx, op.Store, config, status.Preparer)
	slog.InfoContext(ctx, "Run: reattaching runner",
		"runnerStatus", fmtRunnerStatus(status.Runner),
		"configSeqNo", config.SeqNo,
	)
	var currentRunner runner.Runner = runner.ReAttach(ctx, op.Store, config, status.Runner)

	// Reconciliation loop.
	for {
		select {
		case update := <-sub.Ch:
			config := update.Config
			status := update.Status
			//slog.InfoContext(ctx, "Run: received update",
			//	"configSeqNo", config.SeqNo,
			//	"deleted", config.Deleted,
			//	"desiredRunning", config.DesiredState.Running,
			//	"desiredVersion", config.DesiredState.Version,
			//	"preparerStatus", fmtPreparerStatus(status.Preparer),
			//	"runnerStatus", fmtRunnerStatus(status.Runner),
			//	"currentPreparerSeqNo", currentPreparer.SeqNo(),
			//	"currentRunnerSeqNo", currentRunner.SeqNo(),
			//)
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
			case config.SeqNo > currentPreparer.SeqNo() && config.DesiredState.Running:
				slog.InfoContext(ctx, "Run: config ahead of preparer, starting new prepare",
					"configSeqNo", config.SeqNo, "preparerSeqNo", currentPreparer.SeqNo())
				currentPreparer.Cancel()
				currentPreparer = preparer.StartPrepare(op.Store, config)
			case preparerReady(status, config.SeqNo) && config.SeqNo > currentRunner.SeqNo():
				slog.InfoContext(ctx, "Run: preparer ready, creating runner",
					"artifact", status.Preparer.Artifact, "configSeqNo", config.SeqNo)
				currentRunner.Stop()
				currentRunner = runner.Create(ctx, op.Store, config, status)
			default:
				slog.DebugContext(ctx, "Run: nothing to do on update")
			}
		case <-ctx.Done():
			slog.InfoContext(ctx, "graceful exit on context end")
			return
		}
	}
}

func fmtPreparerStatus(p *apigen.PreparerStatus) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v artifact=%q", p.DeploymentSeqNo, p.Status, p.Artifact)
}

func fmtRunnerStatus(r *apigen.RunnerStatus) string {
	if r == nil {
		return "<nil>"
	}
	return fmt.Sprintf("seqNo=%d status=%v pid=%d artifact=%q", r.DeploymentSeqNo, r.Status, r.RunningPid, r.RunningArtifact)
}
