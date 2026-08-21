// protostream serves the cleanproto streaming test service used by the e2e
// suite to verify server-, client-, and bidirectional-streaming RPCs through
// the opendeploy-net HTTPS terminating proxy. Every streaming RPC records a
// StreamReport keyed by the client-chosen stream_id, so tests can query what
// the backend actually observed (frame counts, completion, cancellation).
//
// The server speaks HTTP/1.1 and unencrypted HTTP/2 (h2c) on the same port.
// Bidirectional streaming requires the h2c path: Go's HTTP/1.1 server drains
// the request body once the handler starts writing, so interleaved
// read/write only works over HTTP/2.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/testexamples/protostream/gen"

	"github.com/jptrs93/goutil/logu"
)

func main() {
	slog.SetDefault(logu.NewJSONLogger(os.Stdout, slog.LevelInfo))
	addr := "[::]:8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = "[::]:" + port
	}
	server := &http.Server{
		Addr:    addr,
		Handler: gen.CreateMux(&handler{}, nil),
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Protocols = protocols
	logf("protostream listening address=%s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatalf("serve: %v", err)
	}
}

type streamState struct {
	mu          sync.Mutex
	kind        string
	messagesIn  int32
	messagesOut int32
	started     bool
	done        bool
	result      string
}

func (s *streamState) incIn() {
	s.mu.Lock()
	s.messagesIn++
	s.mu.Unlock()
}

func (s *streamState) incOut() {
	s.mu.Lock()
	s.messagesOut++
	s.mu.Unlock()
}

// setResult records the first terminal outcome; later calls are ignored so a
// cancellation reason is not overwritten by cleanup-path errors.
func (s *streamState) setResult(result string) {
	s.mu.Lock()
	if s.result == "" {
		s.result = result
	}
	s.mu.Unlock()
}

func (s *streamState) finish() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

type handler struct {
	streams sync.Map
}

func (h *handler) register(streamID, kind string) *streamState {
	state := &streamState{kind: kind, started: true}
	h.streams.Store(streamID, state)
	return state
}

func (h *handler) PostV1ServerStream(ctx context.Context, req *gen.TickRequest) iter.Seq2[*gen.Tick, error] {
	return func(yield func(*gen.Tick, error) bool) {
		state := h.register(req.StreamID, "server")
		defer state.finish()
		padding := make([]byte, req.PayloadBytes)
		for i := range padding {
			padding[i] = 'x'
		}
		interval := time.Duration(req.IntervalMs) * time.Millisecond
		for seq := int32(1); req.Count == 0 || seq <= req.Count; seq++ {
			select {
			case <-ctx.Done():
				state.setResult("context-canceled")
				return
			case <-time.After(interval):
			}
			if req.FailAfter > 0 && seq > req.FailAfter {
				state.setResult("aborted")
				yield(nil, errors.New("aborted by fail_after"))
				return
			}
			if !yield(&gen.Tick{StreamID: req.StreamID, Seq: seq, Padding: padding}, nil) {
				state.setResult("write-failed")
				return
			}
			state.incOut()
		}
		state.setResult("completed")
	}
}

func (h *handler) PostV1ClientStream(ctx context.Context, seq iter.Seq2[*gen.Chunk, error]) (*gen.UploadSummary, error) {
	summary := &gen.UploadSummary{}
	var state *streamState
	defer func() {
		if state != nil {
			state.finish()
		}
	}()
	hash := sha256.New()
	for chunk, err := range seq {
		if err != nil {
			if state == nil {
				state = h.register("unknown", "client")
			}
			state.setResult(fmt.Sprintf("recv-error: %v", err))
			return nil, err
		}
		if state == nil {
			state = h.register(chunk.StreamID, "client")
			summary.StreamID = chunk.StreamID
		}
		state.incIn()
		summary.Frames++
		summary.TotalBytes += int64(len(chunk.Data))
		hash.Write(chunk.Data)
	}
	if state == nil {
		return nil, errors.New("empty stream")
	}
	state.setResult("completed")
	summary.Sha256 = hex.EncodeToString(hash.Sum(nil))
	return summary, nil
}

func (h *handler) PostV1BidiStream(ctx context.Context, reqSeq iter.Seq2[*gen.EchoRequest, error]) iter.Seq2[*gen.EchoReply, error] {
	return func(yield func(*gen.EchoReply, error) bool) {
		var state *streamState
		defer func() {
			if state != nil {
				state.finish()
			}
		}()
		type item struct {
			reply *gen.EchoReply
			err   error
		}
		ch := make(chan item)
		go func() {
			defer close(ch)
			for req, err := range reqSeq {
				if err != nil {
					select {
					case ch <- item{err: err}:
					case <-ctx.Done():
					}
					return
				}
				stop := req.CloseStream
				select {
				case ch <- item{reply: &gen.EchoReply{StreamID: req.StreamID, Seq: req.Seq, Text: req.Text}}:
				case <-ctx.Done():
					return
				}
				if stop {
					return
				}
			}
		}()
		for it := range ch {
			if it.err != nil {
				if state == nil {
					state = h.register("unknown", "bidi")
				}
				state.setResult(fmt.Sprintf("recv-error: %v", it.err))
				return
			}
			if state == nil {
				state = h.register(it.reply.StreamID, "bidi")
			}
			state.incIn()
			if !yield(it.reply, nil) {
				state.setResult("write-failed")
				return
			}
			state.incOut()
		}
		if state == nil {
			state = h.register("unknown", "bidi")
		}
		state.setResult("completed")
	}
}

func (h *handler) PostV1StreamReport(ctx context.Context, req *gen.StreamReportRequest) (*gen.StreamReport, error) {
	report := &gen.StreamReport{StreamID: req.StreamID}
	if value, ok := h.streams.Load(req.StreamID); ok {
		state := value.(*streamState)
		state.mu.Lock()
		report.Kind = state.kind
		report.MessagesIn = state.messagesIn
		report.MessagesOut = state.messagesOut
		report.Started = state.started
		report.Done = state.done
		report.Result = state.result
		state.mu.Unlock()
	}
	return report, nil
}

func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
