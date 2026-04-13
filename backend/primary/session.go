package primary

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
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

	// Log streaming: one log request at a time per session. logMu serializes
	// requests; logCh receives data chunks from the read loop when a log
	// stream is in flight.
	logMu sync.Mutex
	logCh chan logChunk
}

type logChunk struct {
	data []byte
	end  bool
}

func newSession(conn *cluster.Conn, machine string, store storage.PrimaryLocalStore, p *Primary) *Session {
	return &Session{
		conn:    conn,
		machine: machine,
		store:   store,
		primary: p,
		logCh:   make(chan logChunk, 64),
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
			select {
			case s.logCh <- logChunk{data: msg.LogData}:
			default:
				slog.Warn("log data dropped (no active log request)", "machine", s.machine)
			}

		case msg.LogEnd:
			select {
			case s.logCh <- logChunk{end: true}:
			default:
			}
		}
	}
}

// handleStatusWrite persists a status transition reported by a worker. The
// worker has already applied it locally; this write publishes it to the
// primary store so other subscribers (UI, cluster broadcast) see it.
// The primary bumps its own StatusSeqNo rather than using the worker's value,
// since the primary and worker maintain independent sequence counters.
func (s *Session) handleStatusWrite(ctx context.Context, st *apigen.DeploymentStatus) {
	if st == nil || st.DeploymentID == 0 {
		return
	}
	s.store.MustWriteDeploymentStatus(ctx, st.DeploymentID, func(dst *apigen.DeploymentStatus) {
		seqNo := dst.StatusSeqNo + 1
		*dst = *st
		dst.StatusSeqNo = seqNo
	})
}

// requestLogs sends a log request to the worker and returns a reader that
// yields the streamed data until LogEnd.
func (s *Session) requestLogs(req *apigen.MsgToWorker) (io.ReadCloser, error) {
	s.logMu.Lock()

	// Drain any leftover chunks from a previous request.
	for {
		select {
		case <-s.logCh:
		default:
			goto drained
		}
	}
drained:

	if err := s.conn.WriteFrame(req.Encode()); err != nil {
		s.logMu.Unlock()
		return nil, fmt.Errorf("sending log request to worker %s: %w", s.machine, err)
	}

	return &logReader{session: s}, nil
}

// logReader implements io.ReadCloser over the streamed log chunks.
type logReader struct {
	session *Session
	buf     []byte
	done    bool
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

	chunk, ok := <-r.session.logCh
	if !ok || chunk.end {
		r.done = true
		return 0, io.EOF
	}

	n := copy(p, chunk.data)
	if n < len(chunk.data) {
		r.buf = chunk.data[n:]
	}
	return n, nil
}

func (r *logReader) Close() error {
	r.done = true
	r.session.logMu.Unlock()
	return nil
}
