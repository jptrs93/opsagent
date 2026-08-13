package secondary

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
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

func runPrimaryConnLoop(ctx context.Context, cfg runtimeConfig, store *state.Service, primaryHTTPClient *http.Client, acme *acmestate.Holder) {
	capi := apigen.NewOpsagentClusterV1Capi(
		"https://"+cfg.PrimaryClusterAddr,
		apigen.WithOpsagentClusterV1CapiHTTPClient(primaryHTTPClient),
	)

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		connectedAt := time.Now()
		underlayAddress := cfg.UnderlayAddress
		var err error
		if underlayAddress == "" {
			underlayAddress, err = resolveDefaultUnderlayAddress(cfg.PrimaryClusterAddr)
		}
		if err == nil {
			err = runSession(ctx, capi, store, cfg.NodeID, underlayAddress, acme)
		}
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

func scheduledInstancePredicateForNode(nodeID int32) storage.ScheduledInstancePredicate {
	return func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	}
}

// runSession opens one bidirectional stream to the primary: it pushes local
// status changes and requested log data out via the request stream, and reads
// the primary's messages (snapshot, assignment updates, log requests) from the
// response stream, applying them to the local store. Returns when the stream
// ends (error or clean EOF).
func runSession(ctx context.Context, capi *apigen.OpsagentClusterV1Capi, store *state.Service, nodeID int32, underlayAddress string, acme *acmestate.Holder) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &outbox{ch: make(chan *apigen.MsgToMaster, 64), ctx: sessCtx}
	// The hello must lead the request stream so the primary can publish an
	// updated network map as soon as this worker reconnects.
	out.Send(&apigen.MsgToMaster{ClusterHello: &apigen.ClusterHello{
		UnderlayAddress:        underlayAddress,
		ClusterProtocolVersion: apigen.ClusterProtocolVersion,
	}})
	if prefix, ok := network.Default.PrefixValue(); ok {
		status, err := cachedClusterNetMapStatus(store, nodeID, prefix, "")
		if err != nil {
			slog.Warn("loading cached network map status failed", "err", err)
		} else if status != nil {
			out.Send(&apigen.MsgToMaster{NetMapStatus: status})
		}
	}

	statusCh, unsub := store.SubscribeScheduledInstanceUpdates(scheduledInstancePredicateForNode(nodeID))
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
	protocolConfirmed := false
	var sessErr error
	for msg, err := range capi.PostV1ClusterConnect(sessCtx, reqs) {
		if err != nil {
			sessErr = err
			break
		}
		if !protocolConfirmed {
			if msg.ClusterProtocolVersion != apigen.ClusterProtocolVersion {
				return fmt.Errorf("cluster protocol mismatch: primary sent %d, worker requires %d", msg.ClusterProtocolVersion, apigen.ClusterProtocolVersion)
			}
			protocolConfirmed = true
			continue
		}
		if !connected {
			connected = true
			slog.Info("slave connected to primary", "peer", capi.BaseURL)
		}
		dispatchFromPrimary(sessCtx, out, store, tracker, msg, nodeID, acme)
	}
	return sessErr
}

// dispatchFromPrimary applies one MsgToWorker received from the primary.
func dispatchFromPrimary(ctx context.Context, out *outbox, store *state.Service, tracker *logStreamTracker, msg *apigen.MsgToWorker, nodeID int32, acme *acmestate.Holder) {
	msgType := "heartbeat"
	switch {
	case msg.ScheduledInstancesSnapshot != nil:
		msgType = "scheduled_instances_snapshot"
	case msg.ScheduledInstanceUpdate != nil:
		msgType = "scheduled_instance_update"
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
	case msg.ClusterNetMap != nil:
		msgType = "cluster_net_map"
	case msg.AcmeState != nil:
		msgType = "acme_state"
	}
	slog.Info("received message from primary", "type", msgType)

	switch {
	case msg.ScheduledInstancesSnapshot != nil:
		applySnapshot(out, store, msg.ScheduledInstancesSnapshot, nodeID)
	case msg.ScheduledInstanceUpdate != nil:
		applyInstanceUpdate(store, msg.ScheduledInstanceUpdate, nodeID)
	case msg.ClusterNetwork != nil:
		if err := applyClusterNetwork(store, msg.ClusterNetwork); err != nil {
			slog.Warn("installing cluster network failed", "err", err)
		}
	case msg.AcmeState != nil:
		store.MustSetLocalKV(storage.LocalKVAcmeState, msg.AcmeState.Encode())
		if acme != nil {
			acme.Set(msg.AcmeState)
		}
	case msg.ClusterNetMap != nil:
		expectedPrefix, _ := network.Default.PrefixValue()
		status, err := acceptClusterNetMap(store, msg.ClusterNetMap, nodeID, expectedPrefix)
		if err != nil {
			slog.Warn("accepting cluster network map failed", "err", err)
			status, _ = cachedClusterNetMapStatus(store, nodeID, expectedPrefix, err.Error())
			if status == nil {
				status = &apigen.NetMapStatus{ReconciliationError: err.Error()}
			}
		} else if err := reconcileClusterNetMap(msg.ClusterNetMap, nodeID, expectedPrefix); err != nil {
			slog.Warn("reconciling cluster network map failed", "err", err)
			status.ReconciliationError = err.Error()
		} else {
			status.AppliedSequence = status.PersistedSequence
		}
		if status != nil {
			out.Send(&apigen.MsgToMaster{NetMapStatus: status})
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

func applyClusterNetwork(store *state.Service, info *apigen.ClusterNetworkInfo) error {
	if info == nil {
		return nil
	}
	p, err := network.ParsePrefix(info.UlaPrefix)
	if err != nil {
		return err
	}
	network.Default.SetPrefix(p)
	store.MustSetLocalKV(storage.LocalKVClusterNetwork, info.Encode())
	return nil
}

// statusPushLoop forwards local status changes to the primary. It tracks the
// last UpdatedAt clock sent per scheduled instance to avoid sending duplicate updates.
func statusPushLoop(ctx context.Context, out *outbox, ch <-chan apigen.ScheduledInstanceState) {
	lastSent := make(map[int32]time.Time)
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-ch:
			if !ok {
				return
			}
			if state.Status.IsZero() || state.Instance.ID == 0 {
				continue
			}
			id := state.Instance.ID
			if !state.Status.UpdatedAt.After(lastSent[id]) {
				continue
			}
			lastSent[id] = state.Status.UpdatedAt
			status := state.Status
			if !out.Send(&apigen.MsgToMaster{StatusWrite: &status}) {
				return
			}
		}
	}
}

// applySnapshot writes scheduled instance assignments from the primary's snapshot
// into the local store, drops any the primary no longer knows about, and replays
// any status history the primary is missing. Each snapshot item carries the
// primary's last-known UpdatedAt clock for that instance; the secondary scans its
// local history for rows above that value and streams them back as individual
// StatusWrites so the primary can insert each one at its canonical clock.
func applySnapshot(out *outbox, store *state.Service, snap *apigen.ScheduledInstanceSnapshot, nodeID int32) {
	slog.Info("applying scheduled instances snapshot from primary", "count", len(snap.Items))
	present := make(map[int32]struct{}, len(snap.Items))
	for _, item := range snap.Items {
		if item == nil || item.Instance.ID == 0 || item.Instance.NodeID != nodeID {
			continue
		}
		present[item.Instance.ID] = struct{}{}
		store.MustWriteScheduledInstanceAssignment(item)
	}

	// The snapshot is the full set of assignments for this node, so anything held
	// locally and missing from it has been dropped by the primary. Prune before the
	// replay below, which returns early once the session's outbox closes.
	if pruned := store.MustFinalizeScheduledInstancesAbsent(present); len(pruned) > 0 {
		slog.Info("finalizing scheduled instances absent from the primary snapshot",
			"scheduled_instance_ids", pruned)
	}

	for _, item := range snap.Items {
		if item == nil || item.Instance.ID == 0 || item.Instance.NodeID != nodeID {
			continue
		}
		var primaryClock time.Time
		if !item.Status.IsZero() {
			primaryClock = item.Status.UpdatedAt
		}
		backlog := store.FetchScheduledInstanceStatusHistorySince(item.Instance.ID, primaryClock)
		if len(backlog) == 0 {
			continue
		}
		slog.Info("replaying status history to primary",
			"scheduled_instance_id", item.Instance.ID, "from", primaryClock, "count", len(backlog))
		for _, st := range backlog {
			if !out.Send(&apigen.MsgToMaster{StatusWrite: st}) {
				return
			}
		}
	}
}

// applyInstanceUpdate writes a single scheduled instance assignment from the primary.
func applyInstanceUpdate(store *state.Service, state *apigen.ScheduledInstanceState, nodeID int32) {
	if state == nil || state.Instance.ID == 0 || state.Instance.NodeID != nodeID {
		return
	}
	slog.Info("applying scheduled instance update from primary",
		"scheduled_instance_id", state.Instance.ID,
		"deployment_id", state.Instance.DeploymentID,
		"deployment_version", state.Instance.DeploymentVersion,
		"target_state", state.Instance.State)
	store.MustWriteScheduledInstanceAssignment(state)
}
