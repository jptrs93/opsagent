package primary

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
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

// outboxSize bounds the buffer of pending MsgToWorker messages. It decouples
// the producers (snapshot/update/heartbeat feeder, log requests) from the
// single consumer that yields them onto the response stream; when full,
// producers block, applying backpressure rather than growing unbounded.
const outboxSize = 64

// heartbeatInterval is how often the primary emits an empty MsgToWorker. With
// HTTP/2 PINGs covering the worker's dead-primary detection, this exists so the
// primary's own writes fail fast against a dead worker.
const heartbeatInterval = 5 * time.Second

// Session represents one connected worker's bidirectional stream. It drains an
// outbox of MsgToWorker frames to the response iterator (send side) and feeds
// incoming MsgToMaster frames into status writes and log streams (receive
// side). Log streams are multiplexed over the one stream by request ID.
type Session struct {
	sessCtx context.Context
	cancel  context.CancelFunc
	machine string
	store   *sqlite.PrimaryStorage

	// outbox carries frames destined for the worker. It is never closed;
	// senders fall through on sessCtx.Done so they never block past teardown.
	outbox chan *apigen.MsgToWorker

	// Log streaming: multiple concurrent streams multiplexed by request ID.
	logMu      sync.Mutex
	logStreams map[string]chan logChunk
	nextLogID  atomic.Uint64
}

type logChunk struct {
	data []byte
	end  bool
}

func newSession(sessCtx context.Context, cancel context.CancelFunc, machine string, store *sqlite.PrimaryStorage) *Session {
	return &Session{
		sessCtx:    sessCtx,
		cancel:     cancel,
		machine:    machine,
		store:      store,
		outbox:     make(chan *apigen.MsgToWorker, outboxSize),
		logStreams: make(map[string]chan logChunk),
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

// run drives the session: it yields the initial snapshot, spawns a reader that
// ingests incoming frames and a feeder that forwards store updates plus
// keepalives, then drains the outbox to the response iterator until the worker
// disconnects or the context is cancelled.
func (s *Session) run(reqs iter.Seq2[*apigen.MsgToMaster, error], yield func(*apigen.MsgToWorker, error) bool) {
	defer s.cancel()
	defer s.closeAllLogStreams()

	snapshot, updatesCh, unsubUpdates := s.store.MustFetchSnapshotAndSubscribe(s.machine)
	defer unsubUpdates()
	items := make([]*apigen.DeploymentWithStatus, 0, len(snapshot))
	for i := range snapshot {
		items = append(items, &snapshot[i])
	}
	initial := &apigen.MsgToWorker{
		DeploymentsSnapshot: &apigen.DeploymentWithStatusSnapshot{Items: items},
	}

	// Reader: ingest incoming MsgToMaster frames. Cancelling on return ends the
	// feeder and unblocks the response loop when the worker disconnects.
	go func() {
		defer s.cancel()
		for msg, err := range reqs {
			if err != nil {
				slog.Info("worker stream read error", "machine", s.machine, "err", err)
				return
			}
			s.handleIncoming(msg)
		}
	}()

	// Feeder: forward per-machine config updates and emit periodic keepalives.
	go func() {
		heartbeat := time.NewTicker(heartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-s.sessCtx.Done():
				return
			case dws, ok := <-updatesCh:
				if !ok {
					return
				}
				cfg := dws.Config
				if !s.send(&apigen.MsgToWorker{DeploymentUpdate: &cfg}) {
					return
				}
			case <-heartbeat.C:
				if !s.send(&apigen.MsgToWorker{}) {
					return
				}
			}
		}
	}()

	// Send the snapshot first so the worker's stream call returns promptly.
	if !yield(initial, nil) {
		return
	}

	// Response loop: drain the outbox to the worker.
	for {
		select {
		case <-s.sessCtx.Done():
			return
		case msg := <-s.outbox:
			if !yield(msg, nil) {
				return
			}
		}
	}
}

// handleIncoming dispatches one frame from the worker.
func (s *Session) handleIncoming(msg *apigen.MsgToMaster) {
	switch {
	case msg.StatusWrite != nil:
		s.handleStatusWrite(msg.StatusWrite)
	case len(msg.LogData) > 0:
		s.routeLogChunk(msg.LogRequestID, logChunk{data: msg.LogData})
	case msg.LogEnd:
		s.routeLogChunk(msg.LogRequestID, logChunk{end: true})
	}
}

// routeLogChunk sends a log chunk to the channel for the given request ID.
func (s *Session) routeLogChunk(requestID string, chunk logChunk) {
	s.logMu.Lock()
	ch, ok := s.logStreams[requestID]
	s.logMu.Unlock()
	if !ok {
		// No active reader for this request (already closed or unknown ID).
		// Also try the legacy empty-ID path for backwards compatibility with
		// workers that haven't been updated yet.
		if requestID != "" {
			s.logMu.Lock()
			ch, ok = s.logStreams[""]
			s.logMu.Unlock()
		}
		if !ok {
			return
		}
	}
	select {
	case ch <- chunk:
	default:
		slog.Warn("log data dropped (channel full)", "machine", s.machine, "requestID", requestID)
	}
}

// closeAllLogStreams closes every open log stream channel so blocked readers
// wake up. Called when the session ends.
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
func (s *Session) handleStatusWrite(st *apigen.DeploymentStatus) {
	if st == nil || st.DeploymentID == 0 {
		return
	}
	s.store.MustWriteReplicatedDeploymentStatus(st)
}

// requestLogs sends a log request to the worker and returns a reader that yields
// the streamed data until LogEnd or Close. Multiple requests can be in flight
// concurrently — each gets its own channel keyed by request ID.
func (s *Session) requestLogs(req *apigen.MsgToWorker) (io.ReadCloser, error) {
	// Assign a unique request ID.
	id := fmt.Sprintf("%s-%d", s.machine, s.nextLogID.Add(1))
	req.DeploymentLogRequest.RequestID = id

	ch := make(chan logChunk, 64)
	s.logMu.Lock()
	s.logStreams[id] = ch
	s.logMu.Unlock()

	if !s.send(req) {
		s.logMu.Lock()
		delete(s.logStreams, id)
		s.logMu.Unlock()
		close(ch)
		return nil, fmt.Errorf("worker %s is not connected", s.machine)
	}

	return &logReader{session: s, requestID: id, ch: ch, closeCh: make(chan struct{})}, nil
}

// logReader implements io.ReadCloser over the streamed log chunks for one
// request ID.
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

// Close stops the log stream. It unregisters the channel so the reader stops
// routing chunks to it, signals the worker to stop tailing, and wakes up any
// blocked Read call.
func (r *logReader) Close() error {
	r.closeOnce.Do(func() {
		r.done = true
		close(r.closeCh)

		// Unregister from the session so the reader stops delivering chunks.
		r.session.logMu.Lock()
		delete(r.session.logStreams, r.requestID)
		r.session.logMu.Unlock()

		// Tell the worker to stop tailing this stream.
		stop := &apigen.MsgToWorker{StopLogRequestID: r.requestID}
		if !r.session.send(stop) {
			slog.Warn("failed sending stop log request to worker (session ended)",
				"machine", r.session.machine, "requestID", r.requestID)
		}
	})
	return nil
}
