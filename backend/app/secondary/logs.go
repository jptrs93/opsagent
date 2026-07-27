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

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare"
	"github.com/jptrs93/opsagent/backend/lib/log/logfilter"
	"github.com/jptrs93/opsagent/backend/lib/log/logreader"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

// deploymentStatuses returns the observed status of every non-final scheduled
// instance for a deployment, newest instance first. A deployment mid-rollover
// has more than one, and the newest is not necessarily the one serving.
func deploymentStatuses(store *sqlite.SecondaryStorage, deploymentID int32) []apigen.ScheduledInstanceStatus {
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

// preparingVersion reports whether any live instance is still preparing the
// given config version.
func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if !p.IsZero() && p.DeploymentConfigVersion == version && prepare.InProgress(p) {
			return true
		}
	}
	return false
}

// streamDeploymentLog resolves seqNo=0 to latest from local status, then
// streams the appropriate log file back to the primary. All chunks and the
// final LogEnd are tagged with the request ID for multiplexing.
func streamDeploymentLog(ctx context.Context, out *outbox, store *sqlite.SecondaryStorage, req *apigen.DeploymentLogRequest) {
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

func streamLogSearch(ctx context.Context, out *outbox, req *apigen.LogSearchRequest) {
	requestID := req.RequestID
	defer func() {
		out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
	}()
	if req.TimeStart.IsZero() {
		return
	}
	if !out.Send(&apigen.MsgToMaster{LogLines: apigen.LogLineBatch{LogDir: apigen.RunOutputDeploymentDir(req.DeploymentID)}, LogRequestID: requestID}) {
		return
	}
	var till *time.Time
	if !req.TimeEnd.IsZero() {
		till = &req.TimeEnd
	}
	count := 0
	limit := logSearchLimit(req)
	batch := make([]*apigen.LogLine, 0, logSearchBatchSize)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		lines := batch
		batch = make([]*apigen.LogLine, 0, logSearchBatchSize)
		return out.Send(&apigen.MsgToMaster{LogLines: apigen.LogLineBatch{Lines: lines}, LogRequestID: requestID})
	}
	for line, err := range logreader.StreamLogs(int(req.DeploymentID), int(req.ConfigVersion), req.TimeStart, till) {
		if err != nil {
			slog.Error("failed searching run logs", "deploymentID", req.DeploymentID, "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !logfilter.Match(line.Line, req.SearchStr, req.LevelMin) {
			continue
		}
		batch = append(batch, &apigen.LogLine{Time: line.Time, Version: line.Version, Run: line.Run, Stream: int32(line.Stream), Line: line.Line})
		if len(batch) >= logSearchBatchSize && !flush() {
			return
		}
		count++
		if limit > 0 && count >= limit {
			flush()
			return
		}
	}
	flush()
}

const logSearchBatchSize = 256

func logSearchLimit(req *apigen.LogSearchRequest) int {
	if req == nil || req.LogLineLimit <= 0 {
		return 0
	}
	return int(req.LogLineLimit)
}

// streamPrepareLog reads a prepare output file and sends it back to the primary
// as a series of LogData frames followed by a LogEnd frame.
func streamPrepareLog(ctx context.Context, out *outbox, req *apigen.PrepareOutputRequest) {
	streamFile(ctx, out, req.OutputPath(), "", nil)
}

// streamRunLog reads a run output file and sends it back to the primary.
func streamRunLog(ctx context.Context, out *outbox, req *apigen.RunOutputRequest) {
	streamLatestRunLog(ctx, out, req, "", nil)
}

func streamLatestRunLog(ctx context.Context, out *outbox, req *apigen.RunOutputRequest, requestID string, keepTailing func() bool) {
	defer func() {
		out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
	}()

	path, f, err := waitForLatestRunLogFile(ctx, req)
	if err != nil {
		slog.Error("run log file not found for streaming", "deploymentID", req.DeploymentID, "version", req.Version, "err", err)
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
		slog.Error("failed streaming run log file", "path", path, "err", err)
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
				slog.Error("failed streaming run log file", "path", path, "err", err)
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

// streamFile reads a file and sends its contents as LogData frames, followed by
// a LogEnd frame. When keepTailing is non-nil, it polls for new content while
// the process is still active instead of ending at the first EOF. All frames
// are tagged with requestID for multiplexing. Always sends LogEnd, even on
// failure.
func streamFile(ctx context.Context, out *outbox, path string, requestID string, keepTailing func() bool) {
	defer func() {
		out.Send(&apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID})
	}()

	f, err := waitForLogFile(ctx, path)
	if err != nil {
		slog.Error("log file not found for streaming", "path", path, "err", err)
		return
	}
	defer f.Close()

	buf := make([]byte, 32*1024)

	// drain reads all currently available data and sends it as LogData frames.
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

	// Initial drain of existing content.
	if err := drain(); err != nil {
		slog.Error("failed streaming log file", "path", path, "err", err)
		return
	}

	// If no tailing callback, just send what we have and finish.
	if keepTailing == nil {
		return
	}

	// Poll for new content while the process is active.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := drain(); err != nil {
				slog.Error("failed streaming log file", "path", path, "err", err)
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
