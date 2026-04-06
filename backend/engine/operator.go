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

func (op DeploymentOperator) RunAll(ctx context.Context, machine string) {
	deps, ch := op.Store.MustFetchSnapshotAndSubscribe(ctx, machine)
	subs := &logstore.Subs[apigen.DeploymentWithStatus]{}
	for _, dep := range deps {
		go op.Run(ctx, subs, dep.Config, dep.Status)
	}
	go func() {
		for {
			select {
			case v := <-ch:
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

	sub, unsubFunc := subs.Subscribe(nil)
	//localStatus := op.Store.MustFetchLocalDeploymentStatus(op.ID)
	//if localStatus.StatusSeqNo > status.StatusSeqNo {
	//	// Secondary nodes keep a local copy of the status. In an edge case the primary node's view of the status may be
	//	// out of date, in which case we re-send the correct status
	//	slog.WarnContext(ctx, fmt.Sprintf("the local status seq number is ahead, sending updated status"))
	//	op.Store.MustWriteDeploymentStatus(localStatus)
	//	status = localStatus
	//} else {
	//	slog.InfoContext(ctx, fmt.Sprintf("local status in sync with primary node's view of status"))
	//}

	var currentRunner runner.Runner = runner.ReAttach(ctx, op.Store, config, status.Runner)
	var currentPreparer preparer.Preparer2 = preparer.ReAttach(ctx, op.Store, config, status.Preparer)

	// Reconciliation loop.
	for {
		select {
		case update := <-sub.Ch:
			config := update.Config
			status := update.Status
			switch {
			case config.Deleted:
				slog.InfoContext(ctx, "deployment has been deleted, shutting down")
				currentPreparer.Cancel()
				currentRunner.Stop()
				unsubFunc()
				return
			case !config.DesiredState.Running:
				currentRunner.Stop()
			case config.SeqNo > currentPreparer.SeqNo() && config.DesiredState.Running:
				currentPreparer.Cancel()
				currentPreparer = preparer.StartPrepare(op.Store, config)
			case config.SeqNo > currentRunner.SeqNo() && status.Preparer.DeploymentSeqNo == config.SeqNo && status.Preparer.Status == apigen.PreparationStatus_READY:
				currentRunner.Stop()
				currentRunner = runner.Create(ctx, op.Store, config, status)
			default:
				slog.InfoContext(ctx, fmt.Sprintf("nothing to do on update"))
			}
		case <-ctx.Done():
			slog.InfoContext(ctx, "graceful exit on context end")
			return
		}
	}
}
