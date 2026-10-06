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
	if prefix, ok := network.Default.PrefixValue(); ok {
		status, err := cachedClusterNetMapStatus(sessCtx, store, nodeID, prefix, "")
		if err != nil {
			slog.WarnContext(sessCtx, "loading cached network map status failed", "err", err)
		} else if status != nil {
			out.Send(&apigen.MsgToPrimary{NetMapStatus: apigen.Some(*status)})
		}
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
	case msg.ScheduledInstancesSnapshot.Present:
		msgType = "scheduled_instances_snapshot"
	case msg.ScheduledInstanceUpdate.Present:
		msgType = "scheduled_instance_update"
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
	case msg.ClusterNetwork.Present:
		msgType = "cluster_network"
	case msg.ClusterNetMap.Present:
		msgType = "cluster_net_map"
	case msg.AcmeState.Present:
		msgType = "acme_state"
	case msg.NixStoreResets.Present:
		msgType = "nix_store_resets"
	}
	slog.InfoContext(ctx, fmt.Sprintf("received message from primary type=%s", msgType))

	switch {
	case msg.ScheduledInstancesSnapshot.Present:
		applySnapshot(ctx, out, store, &msg.ScheduledInstancesSnapshot.Value, nodeID)
		if notifySynced != nil {
			notifySynced()
		}
	case msg.ScheduledInstanceUpdate.Present:
		applyInstanceUpdate(ctx, store, &msg.ScheduledInstanceUpdate.Value, nodeID)
	case msg.ClusterNetwork.Present:
		if err := applyClusterNetwork(store, &msg.ClusterNetwork.Value); err != nil {
			slog.WarnContext(ctx, "installing cluster network failed", "err", err)
		}
	case msg.AcmeState.Present:
		store.MustSetLocalKV(storage.LocalKVAcmeState, msg.AcmeState.Value.Encode())
		if acme != nil {
			acme.Set(&msg.AcmeState.Value)
		}
	case msg.NixStoreResets.Present:
		for _, item := range msg.NixStoreResets.Value.Items {
			nixstore.Default().RequestReset(item.Repo, item.RequestedAt)
		}
	case msg.ClusterNetMap.Present:
		expectedPrefix, _ := network.Default.PrefixValue()
		status, err := acceptClusterNetMap(ctx, store, &msg.ClusterNetMap.Value, nodeID, expectedPrefix, sess.netMapSnapshotPending, netMaps)
		if err != nil {
			slog.WarnContext(ctx, "accepting cluster network map failed", "err", err)
			status, _ = cachedClusterNetMapStatus(ctx, store, nodeID, expectedPrefix, err.Error())
			if status == nil {
				status = &apigen.NetMapStatus{ReconciliationError: err.Error()}
			}
		} else {
			sess.netMapSnapshotPending = false
			if err := reconcileClusterNetMap(&msg.ClusterNetMap.Value, nodeID, expectedPrefix); err != nil {
				slog.WarnContext(ctx, "reconciling cluster network map failed", "err", err)
				status.ReconciliationError = err.Error()
			} else {
				status.AppliedSeq = status.PersistedSeq
			}
		}
		if status != nil {
			out.Send(&apigen.MsgToPrimary{NetMapStatus: apigen.Some(*status)})
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
		if !state.Status.Present || state.Instance.ID == 0 {
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

// Each snapshot item carries the primary's last-known UpdatedAt clock for that
// instance; the secondary scans its local history for rows above that value and
// streams them back as individual StatusWrites so the primary can insert each
// one at its canonical clock.
func applySnapshot(ctx context.Context, out *outbox, store *state.Service, snap *apigen.ScheduledInstanceSnapshot, nodeID uint64) {
	slog.InfoContext(ctx, fmt.Sprintf("applying scheduled instances snapshot from primary count=%d", len(snap.Items)))
	present := make(map[uint64]struct{}, len(snap.Items))
	for i := range snap.Items {
		item := &snap.Items[i]
		if item.Instance.ID == 0 || item.Instance.NodeID != nodeID {
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

	for i := range snap.Items {
		item := &snap.Items[i]
		if item.Instance.ID == 0 || item.Instance.NodeID != nodeID {
			continue
		}
		var primaryClock time.Time
		if item.Status.Present {
			primaryClock = item.Status.Value.UpdatedAt.Value
		}
		backlog := store.FetchScheduledInstanceStatusHistorySince(item.Instance.ID, primaryClock)
		if len(backlog) == 0 {
			continue
		}
		slog.InfoContext(ctx, fmt.Sprintf("replaying %d status history entries to primary from %s", len(backlog), primaryClock),
			"scheduled_instance", item.Instance.ID)
		for _, st := range backlog {
			if !out.Send(&apigen.MsgToPrimary{StatusWrite: apigen.Some(*st)}) {
				return
			}
		}
	}
}

func applyInstanceUpdate(ctx context.Context, store *state.Service, state *apigen.ScheduledInstanceState, nodeID uint64) {
	if state == nil || state.Instance.ID == 0 || state.Instance.NodeID != nodeID {
		return
	}
	slog.InfoContext(ctx, fmt.Sprintf("applying scheduled instance update from primary deploymentSpecVersion=%d targetState=%v",
		state.Config.Meta.SpecVersion, state.Instance.State),
		"scheduled_instance", state.Instance.ID, "dep", state.Instance.Deployment.DeploymentID)
	store.MustWriteScheduledInstanceAssignment(state)
}
