package secondary

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/netmapstate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

type outbox struct {
	ch  chan *apigen.MsgToPrimary
	ctx context.Context
}

func (o *outbox) Send(msg *apigen.MsgToPrimary) bool {
	select {
	case o.ch <- msg:
		return true
	case <-o.ctx.Done():
		return false
	}
}

// notifySynced (optional) is signalled once the first snapshot from the
// primary has been applied to the store; the boot sync gate releases the
// deployment operator on it.
func runPrimaryConnLoop(ctx context.Context, cfg runtimeConfig, store *state.Service, primaryHTTPClient *http.Client, acme *acmestate.Holder, netMaps *netmapstate.Holder, notifySynced func()) {
	ctx = logu.AddTag(ctx, "ClusterSession")
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
			err = runSession(ctx, capi, store, cfg.NodeID, underlayAddress, cfg.WGPublicKey, acme, netMaps, notifySynced)
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) > maxBackoff {
			// A long-lived session that just dropped: reset backoff so a
			// transient blip reconnects promptly.
			backoff = time.Second
		}
		slog.WarnContext(ctx, fmt.Sprintf("slave disconnected from primary; reconnecting addr=%s peer=%s connected_for=%s retry_in=%s",
			cfg.PrimaryClusterAddr, cfg.PrimaryName, time.Since(connectedAt).Round(time.Second), backoff), "err", err)

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

func runSession(ctx context.Context, capi *apigen.OpsagentClusterV1Capi, store *state.Service, nodeID int32, underlayAddress, wgPublicKey string, acme *acmestate.Holder, netMaps *netmapstate.Holder, notifySynced func()) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &outbox{ch: make(chan *apigen.MsgToPrimary, 64), ctx: sessCtx}
	// The hello must lead the request stream so the primary can publish an
	// updated network map as soon as this secondary reconnects.
	out.Send(&apigen.MsgToPrimary{ClusterHello: &apigen.ClusterHello{
		UnderlayAddress:        underlayAddress,
		ClusterProtocolVersion: apigen.ClusterProtocolVersion,
		WgPublicKey:            wgPublicKey,
	}})
	if prefix, ok := network.Default.PrefixValue(); ok {
		status, err := cachedClusterNetMapStatus(sessCtx, store, nodeID, prefix, "")
		if err != nil {
			slog.WarnContext(sessCtx, "loading cached network map status failed", "err", err)
		} else if status != nil {
			out.Send(&apigen.MsgToPrimary{NetMapStatus: status})
		}
	}

	statusCh, unsub := store.SubscribeScheduledInstanceUpdates(scheduledInstancePredicateForNode(nodeID))
	defer unsub()
	go statusPushLoop(sessCtx, out, statusCh)

	tracker := newLogStreamTracker()

	// reqs drains the outbox into the request stream until teardown. The
	// closure is assignable to iter.Seq2[*MsgToPrimary, error].
	reqs := func(yield func(*apigen.MsgToPrimary, error) bool) {
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
	sess := &primarySessionState{netMapSnapshotPending: true}
	var sessErr error
	for msg, err := range capi.PostV1ClusterConnect(sessCtx, reqs) {
		if err != nil {
			sessErr = err
			break
		}
		if !protocolConfirmed {
			if msg.ClusterProtocolVersion != apigen.ClusterProtocolVersion {
				return fmt.Errorf("cluster protocol mismatch: primary sent %d, secondary requires %d", msg.ClusterProtocolVersion, apigen.ClusterProtocolVersion)
			}
			protocolConfirmed = true
			continue
		}
		if !connected {
			connected = true
			slog.InfoContext(sessCtx, fmt.Sprintf("slave connected to primary %s", capi.BaseURL))
		}
		dispatchFromPrimary(sessCtx, out, store, tracker, sess, msg, nodeID, acme, netMaps, notifySynced)
	}
	return sessErr
}

// The first map accepted after connect is the session's snapshot and replaces
// the secondary's cache unconditionally.
type primarySessionState struct {
	netMapSnapshotPending bool
}

func dispatchFromPrimary(ctx context.Context, out *outbox, store *state.Service, tracker *logStreamTracker, sess *primarySessionState, msg *apigen.MsgToSecondary, nodeID int32, acme *acmestate.Holder, netMaps *netmapstate.Holder, notifySynced func()) {
	msgType := "heartbeat"
	switch {
	case msg.ScheduledInstancesSnapshot != nil:
		msgType = "scheduled_instances_snapshot"
	case msg.ScheduledInstanceUpdate != nil:
		msgType = "scheduled_instance_update"
	case msg.DeploymentLogRequest != nil:
		msgType = "deployment_log_request"
	case msg.LogQueryRequest != nil:
		msgType = "log_query_request"
	case msg.StopLogRequestID != "":
		msgType = "stop_log_request"
	case msg.ClusterNetwork != nil:
		msgType = "cluster_network"
	case msg.ClusterNetMap != nil:
		msgType = "cluster_net_map"
	case msg.AcmeState != nil:
		msgType = "acme_state"
	}
	slog.InfoContext(ctx, fmt.Sprintf("received message from primary type=%s", msgType))

	switch {
	case msg.ScheduledInstancesSnapshot != nil:
		applySnapshot(ctx, out, store, msg.ScheduledInstancesSnapshot, nodeID)
		if notifySynced != nil {
			notifySynced()
		}
	case msg.ScheduledInstanceUpdate != nil:
		applyInstanceUpdate(ctx, store, msg.ScheduledInstanceUpdate, nodeID)
	case msg.ClusterNetwork != nil:
		if err := applyClusterNetwork(store, msg.ClusterNetwork); err != nil {
			slog.WarnContext(ctx, "installing cluster network failed", "err", err)
		}
	case msg.AcmeState != nil:
		store.MustSetLocalKV(storage.LocalKVAcmeState, msg.AcmeState.Encode())
		if acme != nil {
			acme.Set(msg.AcmeState)
		}
	case msg.ClusterNetMap != nil:
		expectedPrefix, _ := network.Default.PrefixValue()
		status, err := acceptClusterNetMap(ctx, store, msg.ClusterNetMap, nodeID, expectedPrefix, sess.netMapSnapshotPending, netMaps)
		if err != nil {
			slog.WarnContext(ctx, "accepting cluster network map failed", "err", err)
			status, _ = cachedClusterNetMapStatus(ctx, store, nodeID, expectedPrefix, err.Error())
			if status == nil {
				status = &apigen.NetMapStatus{ReconciliationError: err.Error()}
			}
		} else {
			sess.netMapSnapshotPending = false
			if err := reconcileClusterNetMap(msg.ClusterNetMap, nodeID, expectedPrefix); err != nil {
				slog.WarnContext(ctx, "reconciling cluster network map failed", "err", err)
				status.ReconciliationError = err.Error()
			} else {
				status.AppliedSeq = status.PersistedSeq
			}
		}
		if status != nil {
			out.Send(&apigen.MsgToPrimary{NetMapStatus: status})
		}
	case msg.StopLogRequestID != "":
		tracker.stop(msg.StopLogRequestID)
	case msg.DeploymentLogRequest != nil:
		requestID := msg.DeploymentLogRequest.RequestID
		streamCtx := tracker.start(ctx, requestID)
		go func() {
			defer tracker.remove(requestID)
			streamPrepareOutput(streamCtx, out, store, msg.DeploymentLogRequest)
		}()
	case msg.LogQueryRequest != nil:
		requestID := msg.LogQueryRequest.RequestID
		queryCtx := tracker.start(ctx, requestID)
		go func() {
			defer tracker.remove(requestID)
			runLogQuery(queryCtx, out, msg.LogQueryRequest)
		}()
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
			if !out.Send(&apigen.MsgToPrimary{StatusWrite: &status}) {
				return
			}
		}
	}
}

// Each snapshot item carries the primary's last-known UpdatedAt clock for that
// instance; the secondary scans its local history for rows above that value and
// streams them back as individual StatusWrites so the primary can insert each
// one at its canonical clock.
func applySnapshot(ctx context.Context, out *outbox, store *state.Service, snap *apigen.ScheduledInstanceSnapshot, nodeID int32) {
	slog.InfoContext(ctx, fmt.Sprintf("applying scheduled instances snapshot from primary count=%d", len(snap.Items)))
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
		slog.InfoContext(ctx, fmt.Sprintf("finalizing scheduled instances absent from the primary snapshot ids=%v", pruned))
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
		slog.InfoContext(ctx, fmt.Sprintf("replaying %d status history entries to primary from %s", len(backlog), primaryClock),
			"scheduled_instance", item.Instance.ID)
		for _, st := range backlog {
			if !out.Send(&apigen.MsgToPrimary{StatusWrite: st}) {
				return
			}
		}
	}
}

func applyInstanceUpdate(ctx context.Context, store *state.Service, state *apigen.ScheduledInstanceState, nodeID int32) {
	if state == nil || state.Instance.ID == 0 || state.Instance.NodeID != nodeID {
		return
	}
	slog.InfoContext(ctx, fmt.Sprintf("applying scheduled instance update from primary deploymentVersion=%d targetState=%v",
		state.Instance.DeploymentVersion, state.Instance.State),
		"scheduled_instance", state.Instance.ID, "dep", state.Instance.DeploymentID)
	store.MustWriteScheduledInstanceAssignment(state)
}
