package secondary

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/engine/logreader"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/runner"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type Config struct {
	TLS                *tls.Config
	PrimaryClusterAddr string
	PrimaryName        string // cert CN of the primary (for TLS server name verification)
	MachineName        string
	DataDir            string
}
type outbox struct {
	ch  chan *apigen.MsgToMaster
	ctx context.Context
}

func (o *outbox) Send(msg *apigen.MsgToMaster) bool {
	select {
	case o.ch <- msg:
		return true
	case <-o.ctx.Done():
		return false
	}
}

// Run boots the local store, starts the deployment operator, and then maintains
// a persistent connection to the primary. It intentionally runs forever; fatal
// failures should panic and let the service manager restart the process.
func Run(ctx context.Context, cfg Config) {
	store := sqlite.NewSecondaryStorage(filepath.Join(cfg.DataDir, "secondary.db"))
	primaryHTTPClient := newPrimaryHTTPClient(cfg.TLS, cfg.PrimaryName)
	primaryURL := "https://" + cfg.PrimaryClusterAddr
	githubCredentials := NewPrimaryGithubCredentialsProvider(primaryURL, primaryHTTPClient)

	preparer.GHRel = preparer.NewGithubReleaseDownloader(cfg.DataDir, githubCredentials)

	ctrdClient := ctrd.Connect(ainit.StaticConfig.ContainerdAddress, ainit.StaticConfig.ContainerdNamespace)
	preparer.NixDocker = preparer.NewNixDockerBuilder(cfg.DataDir, githubCredentials, ctrdClient)
	preparer.ContainerImg = preparer.NewContainerImagePuller(ctrdClient)
	preparer.Assets = NewPrimaryAssetProvider(primaryURL, primaryHTTPClient)
	secretProvider := NewPrimarySecretProvider(primaryURL, primaryHTTPClient)
	preparer.Secrets = secretProvider
	runner.Secrets = secretProvider
	configProvider := NewPrimaryConfigProvider(primaryURL, primaryHTTPClient)
	preparer.Configs = configProvider
	runner.Configs = configProvider
	runner.Containerd = ctrdClient

	// Log collector is parked while container logging writes merged binary files directly.
	// go logcollector.RunAll(ctx, store, cfg.MachineName)
	go engine.DeploymentOperator{Store: store}.RunAll(cfg.MachineName)

	runPrimaryConnLoop(ctx, cfg, store, primaryHTTPClient)
}

// newPrimaryHTTPClient builds the HTTP/2-only client a worker uses to dial the
// primary's cluster endpoint over mTLS. serverName overrides the TLS SNI/verify
// name, needed when dialing the primary by IP (so verification matches the
// cert's DNS SAN rather than requiring an IP SAN).
//
// No http.Client.Timeout is set: it is a whole-request deadline and would abort
// the long-lived cluster stream. Connection liveness is handled by the HTTP/2
// PING-based health check.
func newPrimaryHTTPClient(tlsConfig *tls.Config, serverName string) *http.Client {
	cfg := tlsConfig.Clone()
	if serverName != "" {
		cfg.ServerName = serverName
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: cfg,
			Protocols:       protocols,
			HTTP2: &http.HTTP2Config{
				SendPingTimeout: 5 * time.Second,  // PING a silent primary after 5s idle
				PingTimeout:     10 * time.Second, // tear down if no ACK within 10s (~15s total to detect a dead primary)
			},
		},
	}
}

func runPrimaryConnLoop(ctx context.Context, cfg Config, store *sqlite.SecondaryStorage, primaryHTTPClient *http.Client) {
	capi := apigen.NewOpsagentClusterV1Capi(
		"https://"+cfg.PrimaryClusterAddr,
		apigen.WithOpsagentClusterV1CapiHTTPClient(primaryHTTPClient),
	)

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		connectedAt := time.Now()
		err := runSession(ctx, capi, store, cfg.MachineName)
		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) > maxBackoff {
			// A long-lived session that just dropped: reset backoff so a
			// transient blip reconnects promptly.
			backoff = time.Second
		}
		slog.Warn("slave disconnected from primary; reconnecting",
			"addr", cfg.PrimaryClusterAddr,
			"peer", cfg.PrimaryName,
			"connected_for", time.Since(connectedAt).Round(time.Second),
			"retry_in", backoff,
			"err", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// logStreamTracker manages cancellable log stream goroutines keyed by request ID.
type logStreamTracker struct {
	mu      sync.Mutex
	streams map[string]context.CancelFunc
}

func newLogStreamTracker() *logStreamTracker {
	return &logStreamTracker{streams: make(map[string]context.CancelFunc)}
}

func (t *logStreamTracker) start(parent context.Context, requestID string) context.Context {
	ctx, cancel := context.WithCancel(parent)
	t.mu.Lock()
	t.streams[requestID] = cancel
	t.mu.Unlock()
	return ctx
}

func (t *logStreamTracker) stop(requestID string) {
	t.mu.Lock()
	cancel, ok := t.streams[requestID]
	if ok {
		delete(t.streams, requestID)
	}
	t.mu.Unlock()
	if ok {
		cancel()
	}
}

func (t *logStreamTracker) remove(requestID string) {
	t.mu.Lock()
	delete(t.streams, requestID)
	t.mu.Unlock()
}

// runSession opens one bidirectional stream to the primary: it pushes local
// status changes and requested log data out via the request stream, and reads
// the primary's messages (snapshot, config updates, log requests) from the
// response stream, applying them to the local store. Returns when the stream
// ends (error or clean EOF).
func runSession(ctx context.Context, capi *apigen.OpsagentClusterV1Capi, store *sqlite.SecondaryStorage, machine string) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &outbox{ch: make(chan *apigen.MsgToMaster, 64), ctx: sessCtx}

	// Subscribe to local deployment updates to push status back to primary.
	statusCh, unsub := store.SubscribeDeploymentUpdates(machine)
	defer unsub()
	go statusPushLoop(sessCtx, out, statusCh)

	tracker := newLogStreamTracker()

	// reqs drains the outbox into the request stream until teardown. The
	// closure is assignable to iter.Seq2[*MsgToMaster, error].
	reqs := func(yield func(*apigen.MsgToMaster, error) bool) {
		for {
			select {
			case <-sessCtx.Done():
				return
			case msg := <-out.ch:
				if !yield(msg, nil) {
					return
				}
			}
		}
	}

	connected := false
	var sessErr error
	for msg, err := range capi.PostV1ClusterConnect(sessCtx, reqs) {
		if err != nil {
			sessErr = err
			break
		}
		if !connected {
			connected = true
			slog.Info("slave connected to primary", "peer", capi.BaseURL)
		}
		dispatchFromPrimary(sessCtx, out, store, tracker, msg)
	}
	return sessErr
}

// dispatchFromPrimary applies one MsgToWorker received from the primary.
func dispatchFromPrimary(ctx context.Context, out *outbox, store *sqlite.SecondaryStorage, tracker *logStreamTracker, msg *apigen.MsgToWorker) {
	msgType := "heartbeat"
	switch {
	case msg.DeploymentsSnapshot != nil:
		msgType = "deployments_snapshot"
	case msg.DeploymentUpdate != nil:
		msgType = "deployment_update"
	case msg.DeploymentLogRequest != nil:
		msgType = "deployment_log_request"
	case msg.LogSearchRequest != nil:
		msgType = "log_search_request"
	case msg.StopLogRequestID != "":
		msgType = "stop_log_request"
	case msg.PrepareLogRequest != nil:
		msgType = "prepare_log_request"
	case msg.RunLogRequest != nil:
		msgType = "run_log_request"
	}
	slog.Info("received message from primary", "type", msgType)

	switch {
	case msg.DeploymentsSnapshot != nil:
		applySnapshot(out, store, msg.DeploymentsSnapshot)
	case msg.DeploymentUpdate != nil:
		applyConfigUpdate(store, msg.DeploymentUpdate)
	case msg.StopLogRequestID != "":
		tracker.stop(msg.StopLogRequestID)
	case msg.DeploymentLogRequest != nil:
		requestID := msg.DeploymentLogRequest.RequestID
		streamCtx := tracker.start(ctx, requestID)
		go func() {
			defer tracker.remove(requestID)
			streamDeploymentLog(streamCtx, out, store, msg.DeploymentLogRequest)
		}()
	case msg.LogSearchRequest != nil:
		requestID := msg.LogSearchRequest.RequestID
		streamCtx := tracker.start(ctx, requestID)
		go func() {
			defer tracker.remove(requestID)
			streamLogSearch(streamCtx, out, msg.LogSearchRequest)
		}()
	case msg.PrepareLogRequest != nil:
		go streamPrepareLog(ctx, out, msg.PrepareLogRequest)
	case msg.RunLogRequest != nil:
		go streamRunLog(ctx, out, msg.RunLogRequest)
	}
}

// statusPushLoop forwards local status changes to the primary. It tracks the
// last UpdatedAt clock sent per deployment to avoid sending duplicate updates.
func statusPushLoop(ctx context.Context, out *outbox, ch <-chan apigen.DeploymentWithStatus) {
	lastSent := make(map[int32]time.Time)
	for {
		select {
		case <-ctx.Done():
			return
		case dws, ok := <-ch:
			if !ok {
				return
			}
			if dws.Status.IsZero() || dws.Config.ID == 0 {
				continue
			}
			id := dws.Config.ID
			if !dws.Status.UpdatedAt.After(lastSent[id]) {
				continue
			}
			lastSent[id] = dws.Status.UpdatedAt
			status := dws.Status
			if !out.Send(&apigen.MsgToMaster{StatusWrite: &status}) {
				return
			}
		}
	}
}

// applySnapshot writes deployment configs from the primary's snapshot into the
// local store and replays any status history the primary is missing. Each
// snapshot item carries the primary's last-known UpdatedAt clock for that
// deployment; the secondary scans its local history for rows above that value
// and streams them back as individual StatusWrites so the primary can insert
// each one at its canonical clock.
func applySnapshot(out *outbox, store *sqlite.SecondaryStorage, snap *apigen.DeploymentWithStatusSnapshot) {
	slog.Info("applying deployments snapshot from primary", "count", len(snap.Items))
	for _, item := range snap.Items {
		if item == nil || item.Config.ID == 0 {
			continue
		}
		cfg := item.Config
		store.MustWriteDeploymentConfig(&cfg)

		var primaryClock time.Time
		if !item.Status.IsZero() {
			primaryClock = item.Status.UpdatedAt
		}
		backlog := store.FetchDeploymentStatusHistorySince(item.Config.ID, primaryClock)
		if len(backlog) == 0 {
			continue
		}
		slog.Info("replaying status history to primary",
			"id", item.Config.ID, "from", primaryClock, "count", len(backlog))
		for _, st := range backlog {
			if !out.Send(&apigen.MsgToMaster{StatusWrite: st}) {
				return
			}
		}
	}
}

// applyConfigUpdate writes a single config update from the primary into the
// local store.
func applyConfigUpdate(store *sqlite.SecondaryStorage, cfg *apigen.DeploymentConfig) {
	if cfg == nil || cfg.ID == 0 {
		return
	}
	slog.Info("applying deployment config update from primary", "id", cfg.ID, "seqNo", cfg.Version)
	store.MustWriteDeploymentConfig(cfg)
}

// streamDeploymentLog resolves seqNo=0 to latest from local status, then
// streams the appropriate log file back to the primary. All chunks and the
// final LogEnd are tagged with the request ID for multiplexing.
func streamDeploymentLog(ctx context.Context, out *outbox, store *sqlite.SecondaryStorage, req *apigen.DeploymentLogRequest) {
	requestID := req.RequestID
	if req.RunnerOutput != nil {
		r := req.RunnerOutput
		if r.Version == 0 && r.DeploymentID != 0 {
			st := store.FetchDeploymentStatus(r.DeploymentID)
			if st != nil && !st.Runner.IsZero() {
				r.Version = st.Runner.DeploymentConfigVersion
			}
		}
		streamLatestRunLog(ctx, out, r, requestID, func() bool {
			st := store.FetchDeploymentStatus(r.DeploymentID)
			return st != nil && !st.Runner.IsZero() && isRunnerActive(st.Runner.Status)
		})
		return
	}
	if req.PreparerOutput != nil {
		p := req.PreparerOutput
		if p.Version == 0 && p.DeploymentID != 0 {
			st := store.FetchDeploymentStatus(p.DeploymentID)
			if st != nil && !st.Preparer.IsZero() {
				p.Version = st.Preparer.DeploymentConfigVersion
			}
		}
		streamFile(ctx, out, p.OutputPath(), requestID, func() bool {
			st := store.FetchDeploymentStatus(p.DeploymentID)
			return st != nil && !st.Preparer.IsZero() && isPrepareInProgress(st.Preparer.Status)
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

func isPrepareInProgress(status apigen.PreparationStatus) bool {
	return status == apigen.PreparationStatus_PREPARING ||
		status == apigen.PreparationStatus_DOWNLOADING
}

func isRunnerActive(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_RUNNING ||
		status == apigen.RunningStatus_STARTING
}
