package secondary

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// runnerOutputVersion picks the config version a caller asking for the "latest"
// run output wants: a live runner if there is one, else the newest instance that
// has run anything. Mid-rollover the newest instance may not have started yet
// while the older one is still producing the logs the user is watching.
func runnerOutputVersion(statuses []apigen.ScheduledInstanceStatus) int32 {
	for i := range statuses {
		if r := statuses[i].Runner; !r.IsZero() && isRunnerActive(r.Status) {
			return r.DeploymentConfigVersion
		}
	}
	for i := range statuses {
		if r := statuses[i].Runner; !r.IsZero() {
			return r.DeploymentConfigVersion
		}
	}
	return 0
}

// runnerActiveForVersion reports whether any live instance is still running the
// given config version. The stream follows one version, not one instance.
func runnerActiveForVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		r := statuses[i].Runner
		if !r.IsZero() && r.DeploymentConfigVersion == version && isRunnerActive(r.Status) {
			return true
		}
	}
	return false
}

// preparerOutputVersion mirrors runnerOutputVersion for prepare output.
func preparerOutputVersion(statuses []apigen.ScheduledInstanceStatus) int32 {
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() && prepare.InProgress(p) {
			return p.DeploymentConfigVersion
		}
	}
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() {
			return p.DeploymentConfigVersion
		}
	}
	return 0
}

func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if !p.IsZero() && p.DeploymentConfigVersion == version && prepare.InProgress(p) {
			return true
		}
	}
	return false
}

func streamDeploymentLog(ctx context.Context, out *outbox, store *state.Service, req *apigen.DeploymentLogRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	requestID := req.RequestID
	if req.RunnerOutput != nil {
		r := req.RunnerOutput
		if r.Version == 0 && r.DeploymentID != 0 {
			r.Version = runnerOutputVersion(deploymentStatuses(store, r.DeploymentID))
		}
		streamLatestRunLog(ctx, out, r, requestID, func() bool {
			return runnerActiveForVersion(deploymentStatuses(store, r.DeploymentID), r.Version)
		})
		return
	}
	if req.PreparerOutput != nil {
		p := req.PreparerOutput
		if p.Version == 0 && p.DeploymentID != 0 {
			p.Version = preparerOutputVersion(deploymentStatuses(store, p.DeploymentID))
		}
		streamFile(ctx, out, p.OutputPath(), requestID, func() bool {
			return preparingVersion(deploymentStatuses(store, p.DeploymentID), p.Version)
		})
		return
	}
	out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
}

var logManager *logmanager.Manager

func runLogQuery(ctx context.Context, out *outbox, req *apigen.LogQueryRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	if logManager == nil {
		slog.ErrorContext(ctx, "log query requested before log manager started", "dep", req.DeploymentID)
		out.Send(&apigen.MsgToMaster{LogQueryError: "log manager is not running", LogRequestID: req.RequestID})
		return
	}
	resp, err := logManager.Query(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.ErrorContext(ctx, "log query failed", "dep", req.DeploymentID, "err", err)
		out.Send(&apigen.MsgToMaster{LogQueryError: err.Error(), LogRequestID: req.RequestID})
		return
	}
	out.Send(&apigen.MsgToMaster{LogQueryResponse: resp, LogRequestID: req.RequestID})
}

func streamPrepareLog(ctx context.Context, out *outbox, req *apigen.PrepareOutputRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	streamFile(ctx, out, req.OutputPath(), "", nil)
}

func streamRunLog(ctx context.Context, out *outbox, req *apigen.RunOutputRequest) {
	ctx = logu.AddTag(ctx, "LogShipper")
	streamLatestRunLog(ctx, out, req, "", nil)
}

func streamLatestRunLog(ctx context.Context, out *outbox, req *apigen.RunOutputRequest, requestID string, keepTailing func() bool) {
	defer func() {
		out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
	}()

	path, f, err := waitForLatestRunLogFile(ctx, req)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("run log file not found for streaming version=%d", req.Version), "dep", req.DeploymentID, "err", err)
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
				if !out.Send(&apigen.MsgToMaster{LogData: chunk, LogRequestID: requestID}) {
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
		slog.ErrorContext(ctx, fmt.Sprintf("failed streaming run log file %s", path), "err", err)
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
				slog.ErrorContext(ctx, fmt.Sprintf("failed streaming run log file %s", path), "err", err)
				return
			}
			if latest, err := latestRunLogFile(req.DeploymentID, req.Version); err == nil && latest != path {
				if nf, err := os.Open(latest); err == nil {
					_ = f.Close()
					path = latest
					f = nf
					_ = drain()
				}
			}
			if !keepTailing() {
				_ = drain()
				return
			}
		}
	}
}

func waitForLatestRunLogFile(ctx context.Context, req *apigen.RunOutputRequest) (string, *os.File, error) {
	openLatest := func() (string, *os.File, error) {
		path, err := latestRunLogFile(req.DeploymentID, req.Version)
		if err != nil {
			return "", nil, err
		}
		f, err := os.Open(path)
		return path, f, err
	}
	path, f, err := openLatest()
	if err == nil {
		return path, f, nil
	}
	if !os.IsNotExist(err) && err != filepath.ErrBadPattern {
		return "", nil, err
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-deadline:
			return "", nil, os.ErrNotExist
		case <-ticker.C:
			path, f, err = openLatest()
			if err == nil {
				return path, f, nil
			}
			if !os.IsNotExist(err) && err != filepath.ErrBadPattern {
				return "", nil, err
			}
		}
	}
}

func latestRunLogFile(deploymentID int32, version int32) (string, error) {
	pattern := filepath.Join(apigen.RunOutputDeploymentDir(deploymentID), fmt.Sprintf("*_%d_*.logbin", version))
	return latestMatchingLogFile(pattern)
}

func latestMatchingLogFile(pattern string) (string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	latest := matches[0]
	latestInfo, err := os.Stat(latest)
	if err != nil {
		return "", err
	}
	for _, match := range matches[1:] {
		info, err := os.Stat(match)
		if err != nil {
			return "", err
		}
		if info.ModTime().After(latestInfo.ModTime()) {
			latest = match
			latestInfo = info
		}
	}
	return latest, nil
}

// streamFile always sends LogEnd, even on failure. When keepTailing is non-nil,
// it polls for new content while the process is still active instead of ending
// at the first EOF.
func streamFile(ctx context.Context, out *outbox, path string, requestID string, keepTailing func() bool) {
	defer func() {
		out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
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
				if !out.Send(&apigen.MsgToMaster{LogData: chunk, LogRequestID: requestID}) {
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

func isRunnerActive(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_RUNNING ||
		status == apigen.RunningStatus_STARTING
}
