package primary

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Session represents a connected worker. It owns both the write side (send
// snapshot + forward deployment config updates) and the read side (ingest
// status writes + log chunks) of the cluster connection for one worker.
type Session struct {
	conn    *cluster.Conn
	machine string
	store   storage.PrimaryLocalStore
	primary *Primary

	// Log streaming: multiple concurrent streams multiplexed by request ID.
	// logStreams maps request IDs to their dedicated data channels.
	logMu      sync.Mutex
	logStreams map[string]chan logChunk
	nextLogID  atomic.Uint64
}

type logChunk struct {
	data []byte
	end  bool
}

func newSession(conn *cluster.Conn, machine string, store storage.PrimaryLocalStore, p *Primary) *Session {
	return &Session{
		conn:       conn,
		machine:    machine,
		store:      store,
		primary:    p,
		logStreams: make(map[string]chan logChunk),
	}
}

// run performs the full session lifecycle: send the initial snapshot, spawn
// a writer that forwards config updates from the store, and run the read
// loop for incoming messages. Returns when the connection drops or the
// context is cancelled.
func (s *Session) run(ctx context.Context) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	snapshot, updatesCh := s.store.MustFetchSnapshotAndSubscribe(sessCtx, s.machine)

	items := make([]*apigen.DeploymentWithStatus, 0, len(snapshot))
	for i := range snapshot {
		items = append(items, &snapshot[i])
	}
	initial := &apigen.MsgToWorker{
		DeploymentsSnapshot: &apigen.DeploymentWithStatusSnapshot{Items: items},
	}
	if err := s.conn.WriteFrame(initial.Encode()); err != nil {
		return fmt.Errorf("sending initial snapshot: %w", err)
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		heartbeat := time.NewTicker(5 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case dws, ok := <-updatesCh:
				if !ok {
					return
				}
				msg := &apigen.MsgToWorker{DeploymentUpdate: dws.Config}
				if err := s.conn.WriteFrame(msg.Encode()); err != nil {
					slog.Warn("forwarding deployment update to worker failed", "machine", s.machine, "err", err)
					return
				}
			case <-heartbeat.C:
				// Empty MsgToWorker as keepalive for slave read-deadline detection.
				if err := s.conn.WriteFrame((&apigen.MsgToWorker{}).Encode()); err != nil {
					return
				}
			}
		}
	}()

	err := s.readLoop(sessCtx)
	cancel() // stop writer immediately
	<-writerDone

	// Close all open log streams so any blocked readers return immediately.
	s.closeAllLogStreams()
	return err
}

// readLoop processes incoming MsgToMaster frames until the connection drops
// or the context is cancelled.
func (s *Session) readLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		payload, err := s.conn.ReadFrame()
		if err != nil {
			return err
		}

		msg, err := apigen.DecodeMsgToMaster(payload)
		if err != nil {
			slog.Warn("failed decoding message from worker", "machine", s.machine, "err", err)
			continue
		}

		switch {
		case msg.StatusWrite != nil:
			s.handleStatusWrite(ctx, msg.StatusWrite)

		case len(msg.LogData) > 0:
			s.routeLogChunk(msg.LogRequestID, logChunk{data: msg.LogData})

		case msg.LogEnd:
			s.routeLogChunk(msg.LogRequestID, logChunk{end: true})
		}
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
// wake up. Called when the session's connection drops.
func (s *Session) closeAllLogStreams() {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	for id, ch := range s.logStreams {
		close(ch)
		delete(s.logStreams, id)
	}
}

// handleStatusWrite persists a status transition reported by a worker using
// the worker's StatusSeqNo as the authoritative identity. Same seq_no →
// idempotent upsert, so reconnect re-pushes do not create duplicate history
// rows. If the primary has drifted above the worker's latest seq_no, the
// extra rows are deleted so the primary converges to the worker's view.
func (s *Session) handleStatusWrite(ctx context.Context, st *apigen.DeploymentStatus) {
	if st == nil || st.DeploymentID == 0 {
		return
	}
	s.store.MustWriteReplicatedDeploymentStatus(ctx, st)
}

// requestLogs sends a log request to the worker and returns a reader that
// yields the streamed data until LogEnd or Close. Multiple requests can be
// in flight concurrently — each gets its own channel keyed by request ID.
func (s *Session) requestLogs(req *apigen.MsgToWorker) (io.ReadCloser, error) {
	// Assign a unique request ID.
	id := fmt.Sprintf("%s-%d", s.machine, s.nextLogID.Add(1))
	req.DeploymentLogRequest.RequestID = id

	ch := make(chan logChunk, 64)
	s.logMu.Lock()
	s.logStreams[id] = ch
	s.logMu.Unlock()

	if err := s.conn.WriteFrame(req.Encode()); err != nil {
		s.logMu.Lock()
		delete(s.logStreams, id)
		s.logMu.Unlock()
		close(ch)
		return nil, fmt.Errorf("sending log request to worker %s: %w", s.machine, err)
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

// Close stops the log stream. It unregisters the channel so the read loop
// stops routing chunks to it, signals the worker to stop tailing, and wakes
// up any blocked Read call.
func (r *logReader) Close() error {
	r.closeOnce.Do(func() {
		r.done = true
		close(r.closeCh)

		// Unregister from the session so the read loop stops delivering chunks.
		r.session.logMu.Lock()
		delete(r.session.logStreams, r.requestID)
		r.session.logMu.Unlock()

		// Tell the worker to stop tailing this stream.
		stop := &apigen.MsgToWorker{StopLogRequestID: r.requestID}
		if err := r.session.conn.WriteFrame(stop.Encode()); err != nil {
			slog.Warn("failed sending stop log request to worker",
				"machine", r.session.machine, "requestID", r.requestID, "err", err)
		}
	})
	return nil
}
