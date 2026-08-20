package clusterhandler

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// outboxSize bounds the buffer of pending MsgToWorker messages. It decouples
// the producers (snapshot/update/heartbeat feeder, log requests) from the
// single consumer that yields them onto the response stream; when full,
// producers block, applying backpressure rather than growing unbounded.
const outboxSize = 64

const logStreamBufferSize = 10_000

// heartbeatInterval is how often the primary emits an empty MsgToWorker. With
// HTTP/2 PINGs covering the worker's dead-primary detection, this exists so the
// primary's own writes fail fast against a dead worker.
const heartbeatInterval = 5 * time.Second

// Session represents one connected worker's bidirectional stream. It drains an
// outbox of MsgToWorker frames to the response iterator (send side) and feeds
// incoming MsgToMaster frames into status writes and log streams (receive
// side). Log streams are multiplexed over the one stream by request ID.
type Session struct {
	sessCtx       context.Context
	cancel        context.CancelFunc
	NodeID        int32
	identifier    string
	predicate     storage.ScheduledInstancePredicate
	store         *state.Service
	networkPrefix network.Prefix
	networkMaps   networkMapProvider
	acme          *acmestate.Holder

	// outbox carries frames destined for the worker. It is never closed;
	// senders fall through on sessCtx.Done so they never block past teardown.
	outbox chan *apigen.MsgToWorker

	logMu      sync.Mutex
	logStreams map[string]chan logChunk
	nextLogID  atomic.Uint64
}

type logChunk struct {
	data   []byte
	lines  []*apigen.RawLogLine
	logDir string
	end    bool
}

func newSession(sessCtx context.Context, cancel context.CancelFunc, nodeID int32, identifier string, predicate storage.ScheduledInstancePredicate, store *state.Service, networkMaps networkMapProvider) *Session {
	return &Session{
		sessCtx:     sessCtx,
		cancel:      cancel,
		NodeID:      nodeID,
		identifier:  identifier,
		predicate:   predicate,
		store:       store,
		networkMaps: networkMaps,
		outbox:      make(chan *apigen.MsgToWorker, outboxSize),
		logStreams:  make(map[string]chan logChunk),
	}
}

// send queues a frame for the worker. It returns false if the session is
// tearing down, in which case the frame is dropped.
func (s *Session) send(msg *apigen.MsgToWorker) bool {
	select {
	case s.outbox <- msg:
		return true
	case <-s.sessCtx.Done():
		return false
	}
}

func (s *Session) run(reqs iter.Seq2[*apigen.MsgToMaster, error], yield func(*apigen.MsgToWorker, error) bool) {
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
		// A disconnected worker must stop holding the barrier: it is served a
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
	items := make([]*apigen.ScheduledInstanceState, 0, len(snapshot))
	for i := range snapshot {
		items = append(items, &snapshot[i])
	}
	initial := &apigen.MsgToWorker{
		ScheduledInstancesSnapshot: &apigen.ScheduledInstanceSnapshot{Items: items},
	}

	// Cancelling on return ends the feeder and unblocks the response loop when
	// the worker disconnects.
	go func() {
		defer s.cancel()
		for msg, err := range reqs {
			if err != nil {
				slog.Info("worker stream read error", "machine", s.identifier, "err", err)
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
			case state, ok := <-updatesCh:
				if !ok {
					return
				}
				update := state
				if !s.send(&apigen.MsgToWorker{ScheduledInstanceUpdate: &update}) {
					return
				}
			case <-heartbeat.C:
				if !s.send(&apigen.MsgToWorker{}) {
					return
				}
			}
		}
	}()

	if !yield(&apigen.MsgToWorker{ClusterProtocolVersion: apigen.ClusterProtocolVersion}, nil) {
		return
	}
	// Cluster network parameters precede the snapshot so the worker can program
	// its netproxy before acting on any deployment config.
	if !s.networkPrefix.IsZero() {
		netInfo := &apigen.MsgToWorker{ClusterNetwork: &apigen.ClusterNetworkInfo{UlaPrefix: s.networkPrefix.Bytes()}}
		if !yield(netInfo, nil) {
			return
		}
	}
	if netMap != nil {
		if !yield(&apigen.MsgToWorker{ClusterNetMap: netMap}, nil) {
			return
		}
	}
	if acmeState != nil {
		if !yield(&apigen.MsgToWorker{AcmeState: acmeState}, nil) {
			return
		}
	}
	// Send the snapshot first so the worker's stream call returns promptly.
	if !yield(initial, nil) {
		return
	}

	for {
		select {
		case <-s.sessCtx.Done():
			return
		case msg := <-s.outbox:
			if !yield(msg, nil) {
				return
			}
		case next, ok := <-netMapUpdates:
			if !ok {
				netMapUpdates = nil
				continue
			}
			if next != nil && !yield(&apigen.MsgToWorker{ClusterNetMap: next}, nil) {
				return
			}
		case next, ok := <-acmeUpdates:
			if !ok {
				acmeUpdates = nil
				continue
			}
			if next != nil && !yield(&apigen.MsgToWorker{AcmeState: next}, nil) {
				return
			}
		}
	}
}

func (s *Session) handleIncoming(msg *apigen.MsgToMaster) {
	switch {
	case msg.ClusterHello != nil:
		s.handleClusterHello(msg.ClusterHello)
	case msg.StatusWrite != nil:
		s.handleStatusWrite(msg.StatusWrite)
	case msg.NetMapStatus != nil:
		slog.Info("worker network map status",
			"node_id", s.NodeID,
			"persisted_seq", msg.NetMapStatus.PersistedSeq,
			"applied_seq", msg.NetMapStatus.AppliedSeq,
			"error", msg.NetMapStatus.ReconciliationError)
		// Only a clean apply counts. A worker reporting a reconciliation error
		// still has whatever its kernel held before, so treating it as caught up
		// would retire a placement that node can still be routing to.
		if s.networkMaps != nil && msg.NetMapStatus.ReconciliationError == "" {
			s.networkMaps.RecordApplied(s.NodeID, msg.NetMapStatus.AppliedSeq)
		}
	case len(msg.LogData) > 0:
		s.routeLogChunk(msg.LogRequestID, logChunk{data: msg.LogData})
	case !msg.LogLines.IsZero():
		s.routeLogChunk(msg.LogRequestID, logChunk{lines: msg.LogLines.Lines, logDir: msg.LogLines.LogDir})
	case msg.LogEnd:
		s.routeLogChunk(msg.LogRequestID, logChunk{end: true})
	}
}

func (s *Session) handleClusterHello(hello *apigen.ClusterHello) {
	if hello.ClusterProtocolVersion != apigen.ClusterProtocolVersion {
		slog.Warn("worker cluster protocol mismatch", "node_id", s.NodeID, "got", hello.ClusterProtocolVersion, "want", apigen.ClusterProtocolVersion)
		s.cancel()
		return
	}
	underlayAddress, err := s.store.NormalizeNodeUnderlay(s.identifier, hello.UnderlayAddress)
	if err != nil {
		slog.Warn("worker sent invalid underlay address", "node_id", s.NodeID, "underlay_address", hello.UnderlayAddress, "err", err)
		return
	}
	for _, node := range s.store.ListNodes() {
		if node == nil || node.ID != s.NodeID {
			continue
		}
		if len(node.Addresses) == 1 && node.Addresses[0] == underlayAddress {
			return
		}
		s.store.MustSetNodeAddresses(s.NodeID, []string{underlayAddress})
		return
	}
	slog.Warn("worker cluster hello references unknown node", "node_id", s.NodeID)
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
		slog.Warn("log data dropped (channel full)", "machine", s.identifier, "requestID", requestID)
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

// handleStatusWrite persists a status transition reported by a worker using the
// worker's UpdatedAt clock as the authoritative identity. Same clock →
// idempotent upsert, so reconnect re-pushes do not create duplicate history
// rows. If the primary has drifted above the worker's latest clock, the extra
// rows are deleted so the primary converges to the worker's view.
func (s *Session) handleStatusWrite(st *apigen.ScheduledInstanceStatus) {
	if st == nil || st.ScheduledInstanceID == 0 {
		return
	}
	if !buildAllowedRefs(s.store.FetchScheduledSnapshot(s.predicate)).scheduledInstanceAllowed(st.ScheduledInstanceID) {
		slog.Warn("rejecting cross-machine worker status write", "machine", s.identifier, "scheduled_instance_id", st.ScheduledInstanceID)
		return
	}
	s.store.MustWriteReplicatedScheduledInstanceStatus(st)
}

// requestLogs sends a log request to the worker and returns a reader that yields
// the streamed data until LogEnd or Close. Multiple requests can be in flight
// concurrently — each gets its own channel keyed by request ID.
func (s *Session) requestLogs(req *apigen.MsgToWorker) (io.ReadCloser, error) {
	id := fmt.Sprintf("%s-%d", s.identifier, s.nextLogID.Add(1))
	if req.DeploymentLogRequest != nil {
		req.DeploymentLogRequest.RequestID = id
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
		return nil, fmt.Errorf("worker %s is not connected", s.identifier)
	}

	return &logReader{session: s, requestID: id, ch: ch, closeCh: make(chan struct{})}, nil
}

func (s *Session) requestLogSearch(req *apigen.MsgToWorker) (*LogSearchStream, error) {
	id := fmt.Sprintf("%s-%d", s.identifier, s.nextLogID.Add(1))
	if req.LogSearchRequest == nil {
		return nil, fmt.Errorf("log search request is nil")
	}
	req.LogSearchRequest.RequestID = id

	ch := make(chan logChunk, logStreamBufferSize)
	s.logMu.Lock()
	s.logStreams[id] = ch
	s.logMu.Unlock()

	if !s.send(req) {
		s.logMu.Lock()
		delete(s.logStreams, id)
		s.logMu.Unlock()
		close(ch)
		return nil, fmt.Errorf("worker %s is not connected", s.identifier)
	}

	return &LogSearchStream{session: s, requestID: id, ch: ch, closeCh: make(chan struct{})}, nil
}

type LogSearchStream struct {
	session   *Session
	requestID string
	ch        chan logChunk
	done      bool
	closeCh   chan struct{}
	closeOnce sync.Once
}

func (r *LogSearchStream) Seq() iter.Seq2[*apigen.LogLineBatch, error] {
	return func(yield func(*apigen.LogLineBatch, error) bool) {
		for {
			select {
			case chunk, ok := <-r.ch:
				if !ok || chunk.end {
					r.done = true
					return
				}
				batch := r.drainBatch(chunk)
				if !batch.IsZero() && !yield(batch, nil) {
					return
				}
				if r.done {
					return
				}
			case <-r.closeCh:
				r.done = true
				return
			case <-time.After(30 * time.Second):
				r.done = true
				return
			}
		}
	}
}

func (r *LogSearchStream) drainBatch(first logChunk) *apigen.LogLineBatch {
	batch := &apigen.LogLineBatch{}
	batch.Lines = append(batch.Lines, first.lines...)
	batch.LogDir = first.logDir
	for {
		select {
		case chunk, ok := <-r.ch:
			if !ok || chunk.end {
				r.done = true
				return batch
			}
			batch.Lines = append(batch.Lines, chunk.lines...)
			if batch.LogDir == "" {
				batch.LogDir = chunk.logDir
			}
		default:
			return batch
		}
	}
}

func (r *LogSearchStream) Close() error {
	r.closeOnce.Do(func() {
		r.done = true
		close(r.closeCh)

		r.session.logMu.Lock()
		delete(r.session.logStreams, r.requestID)
		r.session.logMu.Unlock()

		stop := &apigen.MsgToWorker{StopLogRequestID: r.requestID}
		if !r.session.send(stop) {
			slog.Warn("failed sending stop log request to worker (session ended)",
				"machine", r.session.identifier, "requestID", r.requestID)
		}
	})
	return nil
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

		stop := &apigen.MsgToWorker{StopLogRequestID: r.requestID}
		if !r.session.send(stop) {
			slog.Warn("failed sending stop log request to worker (session ended)",
				"machine", r.session.identifier, "requestID", r.requestID)
		}
	})
	return nil
}
