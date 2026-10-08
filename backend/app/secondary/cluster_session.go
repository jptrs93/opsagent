package secondary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/nixstore"
	"github.com/jptrs93/opsagent/backend/lib/enrollment"
	"github.com/jptrs93/opsagent/backend/lib/netmapstate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/runtimebin"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
	"github.com/jptrs93/opsagent/backend/util/version"
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
			err = runSession(ctx, capi, store, cfg.NodeID, underlayAddress, cfg.WGPublicKey, acme, netMaps, notifySynced, cfg.NodeIdentifier)
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errEvicted) || enrollment.Evicted(err) {
			handleEviction(ctx, cfg, store)
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

func scheduledInstancePredicateForNode(nodeID uint64) storage.ScheduledInstancePredicate {
	return func(state apigen.ScheduledInstanceState) bool {
		return state.Instance.NodeID == nodeID
	}
}

func runSession(ctx context.Context, capi *apigen.OpsagentClusterV1Capi, store *state.Service, nodeID uint64, underlayAddress, wgPublicKey string, acme *acmestate.Holder, netMaps *netmapstate.Holder, notifySynced func(), identifiers ...string) error {
	underlay, err := apigen.ParseAddr(underlayAddress)
	if err != nil {
		return fmt.Errorf("parsing underlay address %q: %w", underlayAddress, err)
	}
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &outbox{ch: make(chan *apigen.MsgToPrimary, 64), ctx: sessCtx}
	// The hello must lead the request stream so the primary can publish an
	// updated network map as soon as this secondary reconnects.
	identifier := ""
	if len(identifiers) > 0 {
		identifier = identifiers[0]
	}
	hostAddresses := currentHostAddresses(sessCtx)
	hello := func(inventory hostAddressInventory) *apigen.MsgToPrimary {
		return &apigen.MsgToPrimary{ClusterHello: apigen.Some(apigen.ClusterHello{
			ClusterProtocolVersion: apigen.ClusterProtocolVersion,
			OpendeployVersion:      version.Version,
			RuntimeVersions:        runtimebin.InstalledSummary(),
			Reported:               apigen.NodeReported{Identifier: identifier, UnderlayAddress: underlay, WgPublicKey: wgPublicKey, HostAddresses: inventory.wire(), HostAddressesUnknown: inventory.unknown},
		})}
	}
	out.Send(hello(hostAddresses))
	go hostAddressPushLoop(sessCtx, out, hostAddresses, hello)
	prefix, _ := network.Default.PrefixValue()
	status, err := cachedClusterNetMapStatus(sessCtx, store, nodeID, prefix, "")
	if err != nil {
		slog.WarnContext(sessCtx, "loading cached network map status failed", "err", err)
	} else if status != nil {
		out.Send(&apigen.MsgToPrimary{NetMapStatus: apigen.Some(*status)})
	}

	go statusPushLoop(sessCtx, out, store, scheduledInstancePredicateForNode(nodeID))

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
		if msg.Evicted.Present && msg.Evicted.Value {
			return errEvicted
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

func dispatchFromPrimary(ctx context.Context, out *outbox, store *state.Service, tracker *logStreamTracker, sess *primarySessionState, msg *apigen.MsgToSecondary, nodeID uint64, acme *acmestate.Holder, netMaps *netmapstate.Holder, notifySynced func()) {
	msgType := "heartbeat"
	switch {
	case msg.NodeSnapshot.Present:
		msgType = "node_snapshot"
	case msg.NodeUpdate.Present:
		msgType = "node_update"
	case msg.DeploymentLogRequest.Present:
		msgType = "deployment_log_request"
	case msg.LogQueryRequest.Present:
		msgType = "log_query_request"
	case msg.MetricsQueryRequest.Present:
		msgType = "metrics_query_request"
	case msg.MetricsLatestRequest.Present:
		msgType = "metrics_latest_request"
	case msg.StopLogRequestID.Present:
		msgType = "stop_log_request"
	case msg.NixStoreResets.Present:
		msgType = "nix_store_resets"
	}
	slog.InfoContext(ctx, fmt.Sprintf("received message from primary type=%s", msgType))

	switch {
	case msg.NodeSnapshot.Present:
		applyProjectionFrame(ctx, out, store, nodeID, frameOf(&msg.NodeSnapshot.Value, true), sess, netMaps, acme, notifySynced)
	case msg.NodeUpdate.Present:
		applyProjectionFrame(ctx, out, store, nodeID, frameOf(&msg.NodeUpdate.Value, false), sess, netMaps, acme, nil)
	case msg.NixStoreResets.Present:
		for _, item := range msg.NixStoreResets.Value.Items {
			nixstore.Default().RequestReset(item.Repo, item.RequestedAt)
		}
	case msg.StopLogRequestID.Present:
		tracker.stop(msg.StopLogRequestID.Value)
	case msg.DeploymentLogRequest.Present:
		req := msg.DeploymentLogRequest.Value
		streamCtx := tracker.start(ctx, req.RequestID)
		go func() {
			defer tracker.remove(req.RequestID)
			streamPrepareOutput(streamCtx, out, store, &req)
		}()
	case msg.LogQueryRequest.Present:
		req := msg.LogQueryRequest.Value
		queryCtx := tracker.start(ctx, req.RequestID)
		go func() {
			defer tracker.remove(req.RequestID)
			runLogQuery(queryCtx, out, &req)
		}()
	case msg.MetricsQueryRequest.Present:
		req := msg.MetricsQueryRequest.Value
		queryCtx := tracker.start(ctx, req.RequestID)
		go func() {
			defer tracker.remove(req.RequestID)
			runMetricsQuery(queryCtx, out, &req)
		}()
	case msg.MetricsLatestRequest.Present:
		runMetricsLatest(ctx, out, &msg.MetricsLatestRequest.Value)
	}
}

// currentHostAddresses enumerates the addresses an ingress listen selector
// can expand to on this node. Unknown survives protobuf encoding separately
// from a successful inventory containing no addresses.
type hostAddressInventory struct {
	addresses []netip.Addr
	unknown   bool
}

func (h hostAddressInventory) wire() []apigen.IpAddress {
	out := make([]apigen.IpAddress, len(h.addresses))
	for i, addr := range h.addresses {
		out[i] = apigen.AddrOf(addr)
	}
	return out
}

func currentHostAddresses(ctx context.Context) hostAddressInventory {
	prefix, hasPrefix := network.Default.PrefixValue()
	addrs, err := network.EnumerateHostAddresses(prefix, hasPrefix)
	if err != nil {
		slog.WarnContext(ctx, "enumerating host addresses failed", "err", err)
		return hostAddressInventory{unknown: true}
	}
	return hostAddressInventory{addresses: addrs}
}

// hostAddressPushLoop re-sends the cluster hello whenever a poll observes a
// changed host address set, so the primary's inventory follows interface
// changes without a reconnect.
func hostAddressPushLoop(ctx context.Context, out *outbox, last hostAddressInventory, hello func(hostAddressInventory) *apigen.MsgToPrimary) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(network.HostAddressPollInterval):
		}
		current := currentHostAddresses(ctx)
		if current.unknown || (!last.unknown && slices.Equal(current.addresses, last.addresses)) {
			continue
		}
		last = current
		if !out.Send(hello(current)) {
			return
		}
	}
}

type scheduledInstanceSubscriber interface {
	MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func())
}

func statusPushLoop(ctx context.Context, out *outbox, store scheduledInstanceSubscriber, predicate storage.ScheduledInstancePredicate) {
	lastSent := make(map[uint64]time.Time)
	push := func(state apigen.ScheduledInstanceState) bool {
		if !state.Status.Present {
			return true
		}
		id := state.Instance.ID
		updatedAt := state.Status.Value.UpdatedAt.Value
		if !updatedAt.After(lastSent[id]) {
			return true
		}
		lastSent[id] = updatedAt
		return out.Send(&apigen.MsgToPrimary{StatusWrite: state.Status})
	}
	_, ch, unsub := store.MustFetchScheduledSnapshotAndSubscribe(predicate)
	defer func() { unsub() }()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-ch:
			if !ok {
				unsub()
				slog.WarnContext(ctx, "scheduled instance subscription closed; resubscribing")
				batch, ch, unsub = store.MustFetchScheduledSnapshotAndSubscribe(predicate)
			}
			for _, state := range batch {
				if !push(state) {
					return
				}
			}
		}
	}
}

type projectionFrame struct {
	seq       int64
	instances []apigen.NodeInstance
	snapshot  bool
	netMap    *apigen.ClusterNetMap
	acme      *apigen.AcmeState
}

func frameOf(p *apigen.NodeProjection, snapshot bool) projectionFrame {
	frame := projectionFrame{seq: p.Seq, instances: p.Instances, snapshot: snapshot}
	if p.NetMap.Present {
		frame.netMap = &p.NetMap.Value
	}
	if p.Acme.Present {
		frame.acme = &p.Acme.Value
	}
	return frame
}

// applyProjection writes a frame's rows, map and ACME subset in one
// transaction. A map the secondary cannot accept fails the whole frame and
// leaves the rows unwritten. The map decision and its commit happen under
// clusterNetMapMu.
func applyProjection(ctx context.Context, store *state.Service, nodeID uint64, frame projectionFrame, expectedPrefix network.Prefix, sessionSnapshot bool, netMaps *netmapstate.Holder, acme *acmestate.Holder) (*apigen.NetMapStatus, []uint64, error) {
	rows := make([]apigen.ScheduledInstanceState, len(frame.instances))
	for i := range frame.instances {
		rows[i] = apigen.ScheduledInstanceState{Instance: frame.instances[i].Instance, Config: frame.instances[i].Config}
	}
	var present map[uint64]struct{}
	if frame.snapshot {
		present = make(map[uint64]struct{}, len(rows))
		for i := range rows {
			present[rows[i].Instance.ID] = struct{}{}
		}
	}
	kvs := make(map[string][]byte)
	var decision netMapDecision
	if frame.netMap != nil {
		clusterNetMapMu.Lock()
		defer clusterNetMapMu.Unlock()
		var err error
		decision, err = decideClusterNetMap(ctx, store, frame.netMap, nodeID, expectedPrefix, sessionSnapshot)
		if err != nil {
			return nil, nil, err
		}
		if decision.persist {
			kvs[storage.LocalKVClusterNetMap] = decision.next.Encode()
		}
	}
	if frame.acme != nil {
		kvs[storage.LocalKVAcmeState] = frame.acme.Encode()
	}
	pruned := store.MustApplyAssignments(rows, present, kvs)
	var status *apigen.NetMapStatus
	if frame.netMap != nil {
		decision.commit(netMaps)
		status = decision.status()
	}
	if frame.acme != nil && acme != nil {
		acme.Set(frame.acme)
	}
	return status, pruned, nil
}

func applyProjectionFrame(ctx context.Context, out *outbox, store *state.Service, nodeID uint64, frame projectionFrame, sess *primarySessionState, netMaps *netmapstate.Holder, acme *acmestate.Holder, notifySynced func()) {
	if frame.snapshot {
		slog.InfoContext(ctx, fmt.Sprintf("applying node snapshot from primary seq=%d instances=%d", frame.seq, len(frame.instances)))
	} else {
		for i := range frame.instances {
			item := &frame.instances[i]
			slog.InfoContext(ctx, fmt.Sprintf("applying scheduled instance update from primary seq=%d deploymentSpecVersion=%d targetState=%v",
				frame.seq, item.Config.Meta.SpecVersion, item.Instance.State),
				"scheduled_instance", item.Instance.ID, "dep", item.Instance.Deployment.DeploymentID)
		}
	}
	expectedPrefix, _ := network.Default.PrefixValue()
	status, pruned, err := applyProjection(ctx, store, nodeID, frame, expectedPrefix, sess.netMapSnapshotPending, netMaps, acme)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("applying node projection seq=%d failed; nothing from it was applied", frame.seq), "err", err)
		if frame.netMap != nil {
			status, _ = cachedClusterNetMapStatus(ctx, store, nodeID, expectedPrefix, err.Error())
			if status == nil {
				status = &apigen.NetMapStatus{ReconciliationError: err.Error()}
			}
			out.Send(&apigen.MsgToPrimary{NetMapStatus: apigen.Some(*status)})
		}
		return
	}
	if len(pruned) > 0 {
		slog.InfoContext(ctx, fmt.Sprintf("finalizing scheduled instances absent from the primary snapshot ids=%v", pruned))
	}
	if frame.netMap != nil {
		sess.netMapSnapshotPending = false
		if err := reconcileClusterNetMap(frame.netMap, nodeID, expectedPrefix); err != nil {
			slog.WarnContext(ctx, "reconciling cluster network map failed", "err", err)
			status.ReconciliationError = err.Error()
		} else {
			status.AppliedSeq = status.PersistedSeq
		}
		out.Send(&apigen.MsgToPrimary{NetMapStatus: apigen.Some(*status)})
	}
	if !frame.snapshot {
		return
	}
	if notifySynced != nil {
		notifySynced()
	}
	replayStatusHistory(ctx, out, store, frame.instances)
}

func replayStatusHistory(ctx context.Context, out *outbox, store *state.Service, items []apigen.NodeInstance) {
	for i := range items {
		id := items[i].Instance.ID
		primaryClock := items[i].StatusWatermark.Value
		backlog := store.FetchScheduledInstanceStatusHistorySince(id, primaryClock)
		if len(backlog) == 0 {
			continue
		}
		slog.InfoContext(ctx, fmt.Sprintf("replaying %d status history entries to primary from %s", len(backlog), primaryClock), "scheduled_instance", id)
		for _, st := range backlog {
			if !out.Send(&apigen.MsgToPrimary{StatusWrite: apigen.Some(*st)}) {
				return
			}
		}
	}
}
