package secondary

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare"
	"github.com/jptrs93/opsagent/backend/lib/log/logmanager"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

// deploymentStatuses returns the observed status of every non-final scheduled
// instance for a deployment, newest instance first. A deployment mid-rollover
// has more than one, and the newest is not necessarily the one serving.
func deploymentStatuses(store *state.Service, deploymentID int32) []apigen.ScheduledInstanceStatus {
	states := make([]apigen.ScheduledInstanceState, 0, 2)
	for _, state := range store.FetchScheduledSnapshot(nil) {
		if state.Instance.DeploymentID != deploymentID ||
			state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			continue
		}
		states = append(states, state)
	}
	slices.SortFunc(states, func(a, b apigen.ScheduledInstanceState) int {
		return cmp.Compare(b.Instance.ID, a.Instance.ID)
	})
	out := make([]apigen.ScheduledInstanceStatus, 0, len(states))
	for _, state := range states {
		out = append(out, state.Status)
	}
	return out
}

func preparerOutputVersion(statuses []apigen.ScheduledInstanceStatus) int32 {
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() && prepare.InProgress(p) {
			return p.DeploymentSpecVersion
		}
	}
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() {
			return p.DeploymentSpecVersion
		}
	}
	return 0
}

func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if !p.IsZero() && p.DeploymentSpecVersion == version && prepare.InProgress(p) {
			return true
		}
	}
	return false
}

func streamPrepareOutput(ctx context.Context, out *outbox, store *state.Service, req *apigen.DeploymentLogRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	requestID := req.RequestID
	if req.PreparerOutput != nil {
		p := req.PreparerOutput
		if p.SpecVersion == 0 && p.DeploymentID != 0 {
			p.SpecVersion = preparerOutputVersion(deploymentStatuses(store, p.DeploymentID))
		}
		streamFile(ctx, out, p.OutputPath(), requestID, func() bool {
			return preparingVersion(deploymentStatuses(store, p.DeploymentID), p.SpecVersion)
		})
		return
	}
	out.Send(&apigen.MsgToPrimary{LogEnd: true, LogRequestID: requestID})
}

var logManager *logmanager.Manager

func runLogQuery(ctx context.Context, out *outbox, req *apigen.LogQueryRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	if logManager == nil {
		slog.ErrorContext(ctx, "log query requested before log manager started", "dep", req.DeploymentID)
		out.Send(&apigen.MsgToPrimary{LogQueryError: "log manager is not running", LogRequestID: req.RequestID})
		return
	}
	resp, err := logManager.Query(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.ErrorContext(ctx, "log query failed", "dep", req.DeploymentID, "err", err)
		out.Send(&apigen.MsgToPrimary{LogQueryError: err.Error(), LogRequestID: req.RequestID})
		return
	}
	out.Send(&apigen.MsgToPrimary{LogQueryResponse: resp, LogRequestID: req.RequestID})
}

// streamFile always sends LogEnd, even on failure. When keepTailing is non-nil,
// it polls for new content while the process is still active instead of ending
// at the first EOF.
func streamFile(ctx context.Context, out *outbox, path string, requestID string, keepTailing func() bool) {
	defer func() {
		out.Send(&apigen.MsgToPrimary{LogEnd: true, LogRequestID: requestID})
	}()

	f, err := waitForLogFile(ctx, path)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("log file not found for streaming %s", path), "err", err)
		return
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	drain := func() error {
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if !out.Send(&apigen.MsgToPrimary{LogData: chunk, LogRequestID: requestID}) {
					return context.Canceled
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}

	if err := drain(); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("failed streaming log file %s", path), "err", err)
		return
	}
	if keepTailing == nil {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := drain(); err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed streaming log file %s", path), "err", err)
				return
			}
			if !keepTailing() {
				// Process finished — do a final drain to capture any remaining
				// output written before the status changed.
				_ = drain()
				return
			}
		}
	}
}

// waitForLogFile polls for a log file to appear on disk, matching the primary
// handler's waitForFile behavior.
func waitForLogFile(ctx context.Context, path string) (*os.File, error) {
	f, err := os.Open(path)
	if err == nil {
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, os.ErrNotExist
		case <-ticker.C:
			f, err = os.Open(path)
			if err == nil {
				return f, nil
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
}
