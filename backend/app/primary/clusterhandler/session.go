package clusterhandler

import (
	"context"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"io"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// outboxSize bounds the buffer of pending MsgToSecondary messages. It decouples
// the producers (snapshot/update/heartbeat feeder, log requests) from the
// single consumer that yields them onto the response stream; when full,
// producers block, applying backpressure rather than growing unbounded.
const outboxSize = 64

const logStreamBufferSize = 10_000

// heartbeatInterval is how often the primary emits an empty MsgToSecondary. With
// HTTP/2 PINGs covering the secondary's dead-primary detection, this exists so the
// primary's own writes fail fast against a dead secondary.
const heartbeatInterval = 5 * time.Second

// Session represents one connected secondary's bidirectional stream. It drains an
// outbox of MsgToSecondary frames to the response iterator (send side) and feeds
// incoming MsgToPrimary frames into status writes and log streams (receive
// side). Log streams are multiplexed over the one stream by request ID.
type Session struct {
	sessCtx       context.Context
	cancel        context.CancelFunc
	NodeID        uint64
	identifier    string
	predicate     storage.ScheduledInstancePredicate
	store         *state.Service
	networkPrefix network.Prefix
	networkMaps   networkMapProvider
	acme          *acmestate.Holder
	nixStores     nixStoreResetProvider

	// outbox carries frames destined for the secondary. It is never closed;
	// senders fall through on sessCtx.Done so they never block past teardown.
	outbox chan *apigen.MsgToSecondary

	logMu      sync.Mutex
	logStreams map[string]chan logChunk
	nextLogID  atomic.Uint64
}

type logChunk struct {
	data        []byte
	queryResp   *apigen.LogQueryResponse
	metricsResp *apigen.MetricsQueryResponse
	latestResp  *apigen.MetricsLatestResponse
	errMsg      string
	end         bool
}

func newSession(sessCtx context.Context, cancel context.CancelFunc, nodeID uint64, identifier string, predicate storage.ScheduledInstancePredicate, store *state.Service, networkMaps networkMapProvider) *Session {
	return &Session{
		sessCtx:     sessCtx,
		cancel:      cancel,
		NodeID:      nodeID,
		identifier:  identifier,
		predicate:   predicate,
		store:       store,
		networkMaps: networkMaps,
		outbox:      make(chan *apigen.MsgToSecondary, outboxSize),
		logStreams:  make(map[string]chan logChunk),
	}
}

// send queues a frame for the secondary. It returns false if the session is
// tearing down, in which case the frame is dropped.
func (s *Session) send(msg *apigen.MsgToSecondary) bool {
	select {
	case s.outbox <- msg:
		return true
	case <-s.sessCtx.Done():
		return false
	}
}

func (s *Session) evict() {
	if !s.send(&apigen.MsgToSecondary{Evicted: apigen.Some(true)}) {
		s.cancel()
	}
}

func (s *Session) run(reqs iter.Seq2[*apigen.MsgToPrimary, error], yield func(*apigen.MsgToSecondary, error) bool) {
	defer s.cancel()
	defer s.closeAllLogStreams()

	snapshot, updatesCh, unsubUpdates := s.store.MustFetchScheduledSnapshotAndSubscribe(s.predicate)
	defer unsubUpdates()
	var netMap *apigen.ClusterNetMap
	var netMapUpdates <-chan *apigen.ClusterNetMap
	if s.networkMaps != nil {
		var unsubscribeNetMaps func()
		netMap, netMapUpdates, unsubscribeNetMaps = s.networkMaps.SnapshotAndSubscribe(s.NodeID)
		defer unsubscribeNetMaps()
		// A disconnected secondary must stop holding the barrier: it is served a
		// complete snapshot when it comes back, so it cannot still be acting on
		// routing it never applied.
		defer s.networkMaps.ForgetNode(s.NodeID)
	}
	var acmeState *apigen.AcmeState
	var acmeUpdates <-chan *apigen.AcmeState
	if s.acme != nil {
		var unsubscribeAcme func()
		acmeState, acmeUpdates, unsubscribeAcme = s.acme.SnapshotAndSubscribe()
		defer unsubscribeAcme()
	}
	var nixResets *apigen.NixStoreResets
	var nixResetUpdates <-chan *apigen.NixStoreResets
	if s.nixStores != nil {
		var unsubscribeNixResets func()
		nixResets, nixResetUpdates, unsubscribeNixResets = s.nixStores.SnapshotAndSubscribe()
		defer unsubscribeNixResets()
	}
	initial := &apigen.MsgToSecondary{
		ScheduledInstancesSnapshot: apigen.Some(apigen.ScheduledInstanceSnapshot{Items: snapshot}),
	}

	// Cancelling on return ends the feeder and unblocks the response loop when
	// the secondary disconnects.
	go func() {
		defer s.cancel()
		for msg, err := range reqs {
			if err != nil {
				slog.InfoContext(s.sessCtx, "secondary stream read error", "err", err)
				return
			}
			s.handleIncoming(msg)
		}
	}()

	go func() {
		heartbeat := time.NewTicker(heartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-s.sessCtx.Done():
				return
			case batch, ok := <-updatesCh:
				if !ok {
					slog.WarnContext(s.sessCtx, "scheduled instance subscription closed; ending session so the secondary resyncs")
					return
				}
				for _, state := range batch {
					if !s.send(&apigen.MsgToSecondary{ScheduledInstanceUpdate: apigen.Some(state)}) {
						return
					}
				}
			case <-heartbeat.C:
				if !s.send(&apigen.MsgToSecondary{}) {
					return
				}
			}
		}
	}()

	if !yield(&apigen.MsgToSecondary{ClusterProtocolVersion: apigen.ClusterProtocolVersion}, nil) {
		return
	}
	// Cluster network parameters precede the snapshot so the secondary can program
	// its netproxy before acting on any deployment config.
	if !s.networkPrefix.IsZero() {
		netInfo := &apigen.MsgToSecondary{ClusterNetwork: apigen.Some(apigen.ClusterNetworkInfo{UlaPrefix: s.networkPrefix.Bytes()})}
		if !yield(netInfo, nil) {
			return
		}
	}
	if netMap != nil {
		if !yield(&apigen.MsgToSecondary{ClusterNetMap: apigen.Some(*netMap)}, nil) {
			return
		}
	}
	if acmeState != nil {
		if !yield(&apigen.MsgToSecondary{AcmeState: apigen.Some(*acmeState)}, nil) {
			return
		}
	}
	if nixResets != nil && len(nixResets.Items) > 0 {
		if !yield(&apigen.MsgToSecondary{NixStoreResets: apigen.Some(*nixResets)}, nil) {
			return
		}
	}
	// Send the snapshot first so the secondary's stream call returns promptly.
	if !yield(initial, nil) {
		return
	}

	for {
		select {
		case <-s.sessCtx.Done():
			return
		case msg := <-s.outbox:
			if !yield(msg, nil) || msg.Evicted.Present && msg.Evicted.Value {
				return
			}
		case next, ok := <-netMapUpdates:
			if !ok {
				netMapUpdates = nil
				continue
			}
			if next != nil && !yield(&apigen.MsgToSecondary{ClusterNetMap: apigen.Some(*next)}, nil) {
				return
			}
		case next, ok := <-acmeUpdates:
			if !ok {
				acmeUpdates = nil
				continue
			}
			if next != nil && !yield(&apigen.MsgToSecondary{AcmeState: apigen.Some(*next)}, nil) {
				return
			}
		case next, ok := <-nixResetUpdates:
			if !ok {
				nixResetUpdates = nil
				continue
			}
			if next != nil && !yield(&apigen.MsgToSecondary{NixStoreResets: apigen.Some(*next)}, nil) {
				return
			}
		}
	}
}

func (s *Session) handleIncoming(msg *apigen.MsgToPrimary) {
	requestID := msg.LogRequestID.Value
	switch {
	case msg.ClusterHello.Present:
		s.handleClusterHello(&msg.ClusterHello.Value)
	case msg.StatusWrite.Present:
		s.handleStatusWrite(&msg.StatusWrite.Value)
	case msg.NetMapStatus.Present:
		status := msg.NetMapStatus.Value
		slog.InfoContext(s.sessCtx, fmt.Sprintf("secondary network map status persistedSeq=%d appliedSeq=%d error=%q",
			status.PersistedSeq, status.AppliedSeq, status.ReconciliationError))
		// Only a clean apply counts. A secondary reporting a reconciliation error
		// still has whatever its kernel held before, so treating it as caught up
		// would retire a placement that node can still be routing to.
		if s.networkMaps != nil && status.ReconciliationError == "" {
			s.networkMaps.RecordApplied(s.NodeID, status.AppliedSeq)
		}
	case msg.LogData.Present && len(msg.LogData.Value) > 0:
		s.routeLogChunk(requestID, logChunk{data: msg.LogData.Value})
	case msg.LogQueryResponse.Present:
		s.routeLogChunk(requestID, logChunk{queryResp: &msg.LogQueryResponse.Value})
	case msg.LogQueryError.Present:
		s.routeLogChunk(requestID, logChunk{errMsg: msg.LogQueryError.Value})
	case msg.MetricsQueryResponse.Present:
		s.routeLogChunk(requestID, logChunk{metricsResp: &msg.MetricsQueryResponse.Value})
	case msg.MetricsLatestResponse.Present:
		s.routeLogChunk(requestID, logChunk{latestResp: &msg.MetricsLatestResponse.Value})
	case msg.LogEnd.Present && msg.LogEnd.Value:
		s.routeLogChunk(requestID, logChunk{end: true})
	}
}

func (s *Session) handleClusterHello(hello *apigen.ClusterHello) {
	if hello.ClusterProtocolVersion != apigen.ClusterProtocolVersion {
		slog.WarnContext(s.sessCtx, fmt.Sprintf("secondary cluster protocol mismatch got=%v want=%v", hello.ClusterProtocolVersion, apigen.ClusterProtocolVersion))
		s.cancel()
		return
	}
	reported := hello.Reported
	if reported.Identifier == "" {
		reported.Identifier = s.identifier
	}
	if reported.Identifier != s.identifier {
		s.cancel()
		return
	}
	rawUnderlay := ""
	if addr := reported.UnderlayAddress.Addr(); addr.IsValid() {
		rawUnderlay = addr.String()
	}
	underlay, err := nodes.NormalizeNodeUnderlay(s.store.Queries(), s.identifier, rawUnderlay)
	if err != nil {
		slog.WarnContext(s.sessCtx, "secondary sent invalid underlay address", "err", err)
		return
	}
	key, err := wgkey.ValidatePublic(reported.WgPublicKey)
	if err != nil {
		slog.WarnContext(s.sessCtx, "secondary sent invalid WireGuard public key", "err", err)
		return
	}
	reported.UnderlayAddress, reported.WgPublicKey = underlay, key
	nodes.ReportNode(s.store, s.identifier, reported)
	remoteAddress, _ := s.sessCtx.Value(remoteAddressCtxKey{}).(string)
	nodes.UpdateNodeObservedMeta(s.store, s.identifier, remoteAddress, hello.OpendeployVersion, hello.RuntimeVersions)
}

func (s *Session) routeLogChunk(requestID string, chunk logChunk) {
	s.logMu.Lock()
	ch, ok := s.logStreams[requestID]
	s.logMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- chunk:
	default:
		slog.WarnContext(s.sessCtx, fmt.Sprintf("log data dropped (channel full) requestID=%s", requestID))
	}
}

func (s *Session) closeAllLogStreams() {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	for id, ch := range s.logStreams {
		close(ch)
		delete(s.logStreams, id)
	}
}

// handleStatusWrite persists a status transition reported by a secondary using the
// secondary's UpdatedAt clock as the authoritative identity. Same clock →
// idempotent upsert, so reconnect re-pushes do not create duplicate history
// rows. If the primary has drifted above the secondary's latest clock, the extra
// rows are deleted so the primary converges to the secondary's view.
func (s *Session) handleStatusWrite(st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 {
		return
	}
	if !buildAllowedRefs(s.store.FetchScheduledSnapshot(s.predicate)).scheduledInstanceAllowed(st.ScheduledInstanceID) {
		slog.WarnContext(s.sessCtx, "rejecting cross-machine secondary status write", "scheduled_instance", st.ScheduledInstanceID)
		return
	}
	scheduledinstances.WriteReplicatedStatus(s.store, st)
}

// requestLogs sends a log request to the secondary and returns a reader that yields
// the streamed data until LogEnd or Close. Multiple requests can be in flight
// concurrently — each gets its own channel keyed by request ID.
func (s *Session) requestLogs(req *apigen.MsgToSecondary) (io.ReadCloser, error) {
	id := fmt.Sprintf("%s-%d", s.identifier, s.nextLogID.Add(1))
	if req.DeploymentLogRequest.Present {
		req.DeploymentLogRequest.Value.RequestID = id
	}

	ch := make(chan logChunk, logStreamBufferSize)
	s.logMu.Lock()
	s.logStreams[id] = ch
	s.logMu.Unlock()

	if !s.send(req) {
		s.logMu.Lock()
		delete(s.logStreams, id)
		s.logMu.Unlock()
		close(ch)
		return nil, fmt.Errorf("secondary %s is not connected", s.identifier)
	}

	return &logReader{session: s, requestID: id, ch: ch, closeCh: make(chan struct{})}, nil
}

// logQueryTimeout bounds a one-shot log query round trip to a secondary. The
// secondary scans its full requested range before replying, so this is a whole
// -query budget, not an idle timeout.
const logQueryTimeout = 60 * time.Second

// requestOneShot sends the frame build returns for a fresh request id and
// waits for the single reply chunk routed back under that id.
func (s *Session) requestOneShot(ctx context.Context, build func(id string) *apigen.MsgToSecondary) (logChunk, error) {
	id := fmt.Sprintf("%s-%d", s.identifier, s.nextLogID.Add(1))
	req := build(id)
	ch := make(chan logChunk, 1)
	s.logMu.Lock()
	s.logStreams[id] = ch
	s.logMu.Unlock()
	cleanup := func() {
		s.logMu.Lock()
		delete(s.logStreams, id)
		s.logMu.Unlock()
	}
	if !s.send(req) {
		cleanup()
		return logChunk{}, fmt.Errorf("secondary %s is not connected", s.identifier)
	}
	select {
	case chunk, ok := <-ch:
		cleanup()
		if !ok {
			return logChunk{}, fmt.Errorf("secondary %s disconnected", s.identifier)
		}
		return chunk, nil
	case <-ctx.Done():
		cleanup()
		s.send(&apigen.MsgToSecondary{StopLogRequestID: apigen.Some(id)})
		return logChunk{}, ctx.Err()
	case <-time.After(logQueryTimeout):
		cleanup()
		s.send(&apigen.MsgToSecondary{StopLogRequestID: apigen.Some(id)})
		return logChunk{}, fmt.Errorf("log query to secondary %s timed out", s.identifier)
	}
}

func (s *Session) requestLogQuery(ctx context.Context, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	start := time.Now()
	chunk, err := s.requestOneShot(ctx, func(id string) *apigen.MsgToSecondary {
		req.RequestID = id
		return &apigen.MsgToSecondary{LogQueryRequest: apigen.Some(*req)}
	})
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		slog.InfoContext(s.sessCtx, fmt.Sprintf("secondary log query failed after %s requestID=%s", elapsed, req.RequestID), "dep", req.DeploymentID, "err", err)
		return nil, err
	}
	slog.InfoContext(s.sessCtx, fmt.Sprintf("secondary log query round trip %s requestID=%s", elapsed, req.RequestID), "dep", req.DeploymentID)
	if chunk.errMsg != "" {
		return nil, fmt.Errorf("%s", chunk.errMsg)
	}
	if chunk.queryResp == nil {
		return nil, fmt.Errorf("secondary %s sent an unexpected log query reply", s.identifier)
	}
	return chunk.queryResp, nil
}

func (s *Session) requestMetricsQuery(ctx context.Context, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	start := time.Now()
	chunk, err := s.requestOneShot(ctx, func(id string) *apigen.MsgToSecondary {
		req.RequestID = id
		return &apigen.MsgToSecondary{MetricsQueryRequest: apigen.Some(*req)}
	})
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		slog.InfoContext(s.sessCtx, fmt.Sprintf("secondary metrics query failed after %s requestID=%s", elapsed, req.RequestID), "dep", req.DeploymentID, "err", err)
		return nil, err
	}
	slog.DebugContext(s.sessCtx, fmt.Sprintf("secondary metrics query round trip %s requestID=%s", elapsed, req.RequestID), "dep", req.DeploymentID)
	if chunk.errMsg != "" {
		return nil, fmt.Errorf("%s", chunk.errMsg)
	}
	if chunk.metricsResp == nil {
		return nil, fmt.Errorf("secondary %s sent an unexpected metrics query reply", s.identifier)
	}
	return chunk.metricsResp, nil
}

func (s *Session) requestMetricsLatest(ctx context.Context) (*apigen.MetricsLatestResponse, error) {
	chunk, err := s.requestOneShot(ctx, func(id string) *apigen.MsgToSecondary {
		return &apigen.MsgToSecondary{MetricsLatestRequest: apigen.Some(apigen.MetricsLatestRequest{RequestID: id})}
	})
	if err != nil {
		return nil, err
	}
	if chunk.errMsg != "" {
		return nil, fmt.Errorf("%s", chunk.errMsg)
	}
	if chunk.latestResp == nil {
		return nil, fmt.Errorf("secondary %s sent an unexpected metrics reply", s.identifier)
	}
	return chunk.latestResp, nil
}

type logReader struct {
	session   *Session
	requestID string
	ch        chan logChunk
	buf       []byte
	done      bool
	closeCh   chan struct{}
	closeOnce sync.Once
}

func (r *logReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}

	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}

	select {
	case chunk, ok := <-r.ch:
		if !ok || chunk.end {
			r.done = true
			return 0, io.EOF
		}
		n := copy(p, chunk.data)
		if n < len(chunk.data) {
			r.buf = chunk.data[n:]
		}
		return n, nil
	case <-r.closeCh:
		r.done = true
		return 0, io.EOF
	case <-time.After(30 * time.Second):
		r.done = true
		return 0, io.EOF
	}
}

func (r *logReader) Close() error {
	r.closeOnce.Do(func() {
		r.done = true
		close(r.closeCh)

		r.session.logMu.Lock()
		delete(r.session.logStreams, r.requestID)
		r.session.logMu.Unlock()

		stop := &apigen.MsgToSecondary{StopLogRequestID: apigen.Some(r.requestID)}
		if !r.session.send(stop) {
			slog.WarnContext(r.session.sessCtx, fmt.Sprintf("failed sending stop log request to secondary (session ended) requestID=%s", r.requestID))
		}
	})
	return nil
}
