package slave

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"path/filepath"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

// Config holds the configuration for a slave node.
type Config struct {
	TLS         *tls.Config
	PrimaryAddr string
	PrimaryName string // cert CN of the primary (for TLS server name verification)
	MachineName string
	DataDir     string
	GithubToken string
}

// Run boots the local store, seeds the in-memory snapshot, starts the
// deployment operator, and spawns a background goroutine that maintains a
// persistent connection to the primary. It blocks until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	store := sqlite.NewSecondaryStorageAdapter(filepath.Join(cfg.DataDir, "secondary.db"))

	preparer.Nix = preparer.NewNixBuilder(cfg.DataDir, cfg.GithubToken)
	preparer.GHRel = preparer.NewGithubReleaseDownloader(cfg.DataDir, cfg.GithubToken)

	go engine.DeploymentOperator{Store: store}.RunAll(ctx, cfg.MachineName)

	go runPrimaryConnLoop(ctx, cfg, store)

	<-ctx.Done()
	return ctx.Err()
}

// runPrimaryConnLoop dials the primary in a loop, running a session while
// connected and backing off on dial failures. The slave keeps operating
// off local state while disconnected — this loop never returns until ctx
// is done.
func runPrimaryConnLoop(ctx context.Context, cfg Config, store *sqlite.SecondaryStorageAdapter) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := cluster.Dial(cfg.PrimaryAddr, cfg.TLS, cfg.PrimaryName)
		if err != nil {
			slog.Warn("primary dial failed; slave is disconnected",
				"addr", cfg.PrimaryAddr,
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
			continue
		}
		backoff = time.Second

		connectedAt := time.Now()
		slog.Info("slave connected to primary",
			"addr", cfg.PrimaryAddr,
			"peer", conn.PeerName())
		err = runSession(ctx, conn, store, cfg.MachineName)
		if err != nil && ctx.Err() == nil {
			slog.Warn("slave disconnected from primary; reconnecting",
				"addr", cfg.PrimaryAddr,
				"peer", conn.PeerName(),
				"connected_for", time.Since(connectedAt).Round(time.Second),
				"err", err)
		}
		conn.Close()
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

// runSession handles one connected cluster session: read messages from the
// primary (snapshot, config updates, log requests), apply state to the local
// store, and push local status changes back. Returns when the connection
// drops or ctx is done.
func runSession(ctx context.Context, conn *cluster.Conn, store *sqlite.SecondaryStorageAdapter, machine string) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Subscribe to local deployment updates to push status back to primary.
	statusCh, unsub := store.SubscribeDeploymentUpdates(machine)
	defer unsub()

	go statusPushLoop(sessCtx, conn, statusCh)

	tracker := newLogStreamTracker()

	// Read timeout: the primary sends heartbeats every 5s. If we don't
	// receive any frame within 15s, assume the connection is dead.
	const readTimeout = 15 * time.Second

	for {
		select {
		case <-sessCtx.Done():
			return sessCtx.Err()
		default:
		}

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		payload, err := conn.ReadFrame()
		if err != nil {
			return err
		}
		msg, err := apigen.DecodeMsgToWorker(payload)
		if err != nil {
			slog.Warn("failed decoding message from primary", "err", err)
			continue
		}

		msgType := "heartbeat"
		switch {
		case msg.DeploymentsSnapshot != nil:
			msgType = "deployments_snapshot"
		case msg.DeploymentUpdate != nil:
			msgType = "deployment_update"
		case msg.DeploymentLogRequest != nil:
			msgType = "deployment_log_request"
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
			applySnapshot(sessCtx, conn, store, msg.DeploymentsSnapshot)
		case msg.DeploymentUpdate != nil:
			applyConfigUpdate(sessCtx, store, msg.DeploymentUpdate)
		case msg.StopLogRequestID != "":
			tracker.stop(msg.StopLogRequestID)
		case msg.DeploymentLogRequest != nil:
			requestID := msg.DeploymentLogRequest.RequestID
			streamCtx := tracker.start(sessCtx, requestID)
			go func() {
				defer tracker.remove(requestID)
				streamDeploymentLog(streamCtx, conn, store, msg.DeploymentLogRequest)
			}()
		case msg.PrepareLogRequest != nil:
			go streamPrepareLog(sessCtx, conn, msg.PrepareLogRequest)
		case msg.RunLogRequest != nil:
			go streamRunLog(sessCtx, conn, msg.RunLogRequest)
		}
	}
}

// statusPushLoop forwards local status changes to the primary. It tracks the
// last StatusSeqNo sent per deployment to avoid sending duplicate updates.
func statusPushLoop(ctx context.Context, conn *cluster.Conn, ch <-chan apigen.DeploymentWithStatus) {
	lastSeq := make(map[int32]int32)
	for {
		select {
		case <-ctx.Done():
			return
		case dws, ok := <-ch:
			if !ok {
				return
			}
			if dws.Status == nil || dws.Config == nil || dws.Config.ID == 0 {
				continue
			}
			id := dws.Config.ID
			if dws.Status.StatusSeqNo <= lastSeq[id] {
				continue
			}
			lastSeq[id] = dws.Status.StatusSeqNo
			msg := &apigen.MsgToMaster{StatusWrite: dws.Status}
			if err := conn.WriteFrame(msg.Encode()); err != nil {
				slog.Warn("failed sending status to primary", "err", err)
				return
			}
		}
	}
}

// applySnapshot writes deployment configs from the primary's snapshot into
// the local store. Status is NOT applied — the secondary is the authority for
// its own deployment status. If the primary's view of a deployment's status
// differs from the local state, the local status is pushed back so the
// primary's mirror is refreshed.
func applySnapshot(ctx context.Context, conn *cluster.Conn, store *sqlite.SecondaryStorageAdapter, snap *apigen.DeploymentWithStatusSnapshot) {
	slog.Info("applying deployments snapshot from primary", "count", len(snap.Items))
	for _, item := range snap.Items {
		if item.Config == nil || item.Config.ID == 0 {
			continue
		}
		store.MustWriteDeploymentConfig(ctx, item.Config)

		// Push local status back if the primary's copy is stale.
		local := store.FetchDeploymentStatus(item.Config.ID)
		if local != nil && statusDiffers(local, item.Status) {
			slog.Info("pushing stale local status to primary", "id", item.Config.ID)
			msg := &apigen.MsgToMaster{StatusWrite: local}
			if err := conn.WriteFrame(msg.Encode()); err != nil {
				slog.Warn("failed pushing stale status to primary", "err", err)
				return
			}
		}
	}
}

// statusDiffers returns true if the local status has meaningful differences
// from the remote status that the primary should know about.
func statusDiffers(local, remote *apigen.DeploymentStatus) bool {
	if remote == nil {
		// Primary has no status at all — push ours if we have anything.
		return local.Preparer != nil || local.Runner != nil
	}
	// Compare preparer state.
	if (local.Preparer == nil) != (remote.Preparer == nil) {
		return true
	}
	if local.Preparer != nil && remote.Preparer != nil {
		if local.Preparer.DeploymentConfigVersion != remote.Preparer.DeploymentConfigVersion ||
			local.Preparer.Status != remote.Preparer.Status ||
			local.Preparer.Artifact != remote.Preparer.Artifact {
			return true
		}
	}
	// Compare runner state.
	if (local.Runner == nil) != (remote.Runner == nil) {
		return true
	}
	if local.Runner != nil && remote.Runner != nil {
		if local.Runner.DeploymentConfigVersion != remote.Runner.DeploymentConfigVersion ||
			local.Runner.Status != remote.Runner.Status ||
			local.Runner.RunningPid != remote.Runner.RunningPid ||
			local.Runner.RunningArtifact != remote.Runner.RunningArtifact {
			return true
		}
	}
	return false
}

// applyConfigUpdate writes a single config update from the primary into the
// local store.
func applyConfigUpdate(ctx context.Context, store *sqlite.SecondaryStorageAdapter, cfg *apigen.DeploymentConfig) {
	if cfg == nil || cfg.ID == 0 {
		return
	}
	slog.Info("applying deployment config update from primary", "id", cfg.ID, "seqNo", cfg.Version)
	store.MustWriteDeploymentConfig(ctx, cfg)
}

// streamDeploymentLog resolves seqNo=0 to latest from local status, then
// streams the appropriate log file back to the primary. All chunks and the
// final LogEnd are tagged with the request ID for multiplexing.
func streamDeploymentLog(ctx context.Context, conn *cluster.Conn, store *sqlite.SecondaryStorageAdapter, req *apigen.DeploymentLogRequest) {
	requestID := req.RequestID
	if req.RunnerOutput != nil {
		r := req.RunnerOutput
		if r.Version == 0 && r.DeploymentID != 0 {
			st := store.FetchDeploymentStatus(r.DeploymentID)
			if st != nil && st.Runner != nil {
				r.Version = st.Runner.DeploymentConfigVersion
			}
		}
		streamFile(ctx, conn, store, r.OutputPath(), requestID, func() bool {
			st := store.FetchDeploymentStatus(r.DeploymentID)
			return st != nil && st.Runner != nil && isRunnerActive(st.Runner.Status)
		})
		return
	}
	if req.PreparerOutput != nil {
		p := req.PreparerOutput
		if p.Version == 0 && p.DeploymentID != 0 {
			st := store.FetchDeploymentStatus(p.DeploymentID)
			if st != nil && st.Preparer != nil {
				p.Version = st.Preparer.DeploymentConfigVersion
			}
		}
		streamFile(ctx, conn, store, p.OutputPath(), requestID, func() bool {
			st := store.FetchDeploymentStatus(p.DeploymentID)
			return st != nil && st.Preparer != nil && isPrepareInProgress(st.Preparer.Status)
		})
		return
	}
	end := &apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID}
	_ = conn.WriteFrame(end.Encode())
}

// streamPrepareLog reads a prepare output file and sends it back to the primary
// as a series of LogData frames followed by a LogEnd frame.
func streamPrepareLog(ctx context.Context, conn *cluster.Conn, req *apigen.PrepareOutputRequest) {
	streamFile(ctx, conn, nil, req.OutputPath(), "", nil)
}

// streamRunLog reads a run output file and sends it back to the primary.
func streamRunLog(ctx context.Context, conn *cluster.Conn, req *apigen.RunOutputRequest) {
	streamFile(ctx, conn, nil, req.OutputPath(), "", nil)
}

// streamFile reads a file and sends its contents as LogData frames, followed
// by a LogEnd frame. When keepTailing is non-nil, it polls for new content
// while the process is still active instead of ending at the first EOF.
// All frames are tagged with requestID for multiplexing.
// Always sends LogEnd, even on write failure.
func streamFile(ctx context.Context, conn *cluster.Conn, store *sqlite.SecondaryStorageAdapter, path string, requestID string, keepTailing func() bool) {
	defer func() {
		end := &apigen.MsgToMaster{LogEnd: true, LogRequestID: requestID}
		_ = conn.WriteFrame(end.Encode())
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
				msg := &apigen.MsgToMaster{LogData: chunk, LogRequestID: requestID}
				if werr := conn.WriteFrame(msg.Encode()); werr != nil {
					return werr
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
				// Process finished — do a final drain to capture any
				// remaining output written before the status changed.
				_ = drain()
				return
			}
		}
	}
}

// waitForLogFile polls for a log file to appear on disk, matching the
// primary handler's waitForFile behavior.
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
