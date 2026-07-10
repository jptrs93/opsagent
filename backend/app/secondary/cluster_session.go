package secondary

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

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

func runPrimaryConnLoop(ctx context.Context, cfg runtimeConfig, store *sqlite.SecondaryStorage, primaryHTTPClient *http.Client) {
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
	case msg.ClusterNetwork != nil:
		msgType = "cluster_network"
	}
	slog.Info("received message from primary", "type", msgType)

	switch {
	case msg.DeploymentsSnapshot != nil:
		applySnapshot(out, store, msg.DeploymentsSnapshot)
	case msg.DeploymentUpdate != nil:
		applyConfigUpdate(store, msg.DeploymentUpdate)
	case msg.ClusterNetwork != nil:
		if err := applyClusterNetwork(store, msg.ClusterNetwork); err != nil {
			slog.Warn("installing cluster network failed", "err", err)
		}
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

func applyClusterNetwork(store *sqlite.SecondaryStorage, info *apigen.ClusterNetworkInfo) error {
	if info == nil {
		return nil
	}
	p, err := network.ParsePrefix(info.UlaPrefix)
	if err != nil {
		return err
	}
	network.Default.SetPrefix(p)
	store.MustSetLocalKV(sqlite.LocalKVClusterNetwork, info.Encode())
	return nil
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
