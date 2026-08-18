package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/testexamples/protostream/gen"
)

func startServer(t *testing.T) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: gen.CreateMux(&handler{}, nil)}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Protocols = protocols
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	clientProtocols := new(http.Protocols)
	clientProtocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: clientProtocols}}
	return "http://" + listener.Addr().String(), client
}

func postProto(t *testing.T, client *http.Client, url string, body []byte) []byte {
	t.Helper()
	resp, err := client.Post(url, "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	return data
}

func fetchReport(t *testing.T, client *http.Client, base, streamID string) *gen.StreamReport {
	t.Helper()
	data := postProto(t, client, base+"/v1/stream-report", (&gen.StreamReportRequest{StreamID: streamID}).Encode())
	report, err := gen.DecodeStreamReport(data)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func waitForDone(t *testing.T, client *http.Client, base, streamID string) *gen.StreamReport {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		report := fetchReport(t, client, base, streamID)
		if report.Done {
			return report
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stream %s never reported done", streamID)
	return nil
}

func TestServerStreamCompletes(t *testing.T) {
	base, client := startServer(t)
	req := &gen.TickRequest{StreamID: "ss-1", Count: 5, IntervalMs: 10, PayloadBytes: 8}
	resp, err := client.Post(base+"/v1/server-stream", "application/x-protobuf", bytes.NewReader(req.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := gen.NewStreamReader(resp.Body, 0)
	var seqs []int32
	for {
		payload, ok, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		tick, err := gen.DecodeTick(payload)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, tick.Seq)
	}
	if len(seqs) != 5 || seqs[0] != 1 || seqs[4] != 5 {
		t.Fatalf("unexpected seqs %v", seqs)
	}
	report := waitForDone(t, client, base, "ss-1")
	if report.Result != "completed" || report.MessagesOut != 5 {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestServerStreamClientCancel(t *testing.T) {
	base, client := startServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := &gen.TickRequest{StreamID: "ss-cancel", Count: 1000, IntervalMs: 10}
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/server-stream", bytes.NewReader(req.Encode()))
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	reader := gen.NewStreamReader(resp.Body, 0)
	for range 3 {
		if _, ok, err := reader.Next(); err != nil || !ok {
			t.Fatalf("expected frame, got ok=%v err=%v", ok, err)
		}
	}
	cancel()
	resp.Body.Close()
	report := waitForDone(t, client, base, "ss-cancel")
	if report.Result != "context-canceled" && report.Result != "write-failed" {
		t.Fatalf("unexpected result %q", report.Result)
	}
	if report.MessagesOut >= 1000 {
		t.Fatalf("expected early termination, sent %d", report.MessagesOut)
	}
}

func TestClientStream(t *testing.T) {
	base, client := startServer(t)
	var body bytes.Buffer
	for seq := int32(1); seq <= 4; seq++ {
		chunk := &gen.Chunk{StreamID: "cs-1", Seq: seq, Data: bytes.Repeat([]byte{byte(seq)}, 100)}
		if err := gen.WriteStreamFrame(&body, chunk.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := client.Post(base+"/v1/client-stream", "application/protobuf-stream", &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	summary, err := gen.DecodeUploadSummary(data)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Frames != 4 || summary.TotalBytes != 400 || summary.Sha256 == "" {
		t.Fatalf("unexpected summary %+v", summary)
	}
	report := waitForDone(t, client, base, "cs-1")
	if report.Result != "completed" || report.MessagesIn != 4 {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestClientStreamAbort(t *testing.T) {
	base, client := startServer(t)
	pr, pw := io.Pipe()
	go func() {
		chunk := &gen.Chunk{StreamID: "cs-abort", Seq: 1, Data: []byte("x")}
		_ = gen.WriteStreamFrame(pw, chunk.Encode())
		time.Sleep(50 * time.Millisecond)
		pw.CloseWithError(fmt.Errorf("client abort"))
	}()
	resp, err := client.Post(base+"/v1/client-stream", "application/protobuf-stream", pr)
	if err == nil {
		resp.Body.Close()
	}
	report := waitForDone(t, client, base, "cs-abort")
	if !strings.HasPrefix(report.Result, "recv-error") {
		t.Fatalf("unexpected result %q", report.Result)
	}
}

// TestBidiInterleaved proves each echo arrives before the next request is
// sent, which only works when neither side buffers the h2c streams.
func TestBidiInterleaved(t *testing.T) {
	base, client := startServer(t)
	pr, pw := io.Pipe()
	httpReq, _ := http.NewRequest(http.MethodPost, base+"/v1/bidi-stream", pr)
	httpReq.Header.Set("Content-Type", "application/protobuf-stream")
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.Do(httpReq)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	send := func(seq int32, closeStream bool) {
		msg := &gen.EchoRequest{StreamID: "bidi-1", Seq: seq, Text: fmt.Sprintf("msg-%d", seq), CloseStream: closeStream}
		if err := gen.WriteStreamFrame(pw, msg.Encode()); err != nil {
			t.Fatal(err)
		}
	}
	send(1, false)
	var resp *http.Response
	select {
	case resp = <-respCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no response headers; request stream is being buffered")
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", resp.Proto)
	}
	reader := gen.NewStreamReader(resp.Body, 0)
	readEcho := func(seq int32) {
		payload, ok, err := reader.Next()
		if err != nil || !ok {
			t.Fatalf("expected echo %d, got ok=%v err=%v", seq, ok, err)
		}
		reply, err := gen.DecodeEchoReply(payload)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Seq != seq {
			t.Fatalf("expected seq %d, got %d", seq, reply.Seq)
		}
	}
	readEcho(1)
	send(2, false)
	readEcho(2)
	send(3, true)
	readEcho(3)
	if _, ok, err := reader.Next(); ok || err != nil {
		t.Fatalf("expected clean end, got ok=%v err=%v", ok, err)
	}
	pw.Close()
	report := waitForDone(t, client, base, "bidi-1")
	if report.Result != "completed" || report.MessagesIn != 3 || report.MessagesOut != 3 {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestBidiClientCancel(t *testing.T) {
	base, client := startServer(t)
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/bidi-stream", pr)
	httpReq.Header.Set("Content-Type", "application/protobuf-stream")
	respCh := make(chan *http.Response, 1)
	go func() {
		if resp, err := client.Do(httpReq); err == nil {
			respCh <- resp
		}
	}()
	msg := &gen.EchoRequest{StreamID: "bidi-cancel", Seq: 1, Text: "hello"}
	if err := gen.WriteStreamFrame(pw, msg.Encode()); err != nil {
		t.Fatal(err)
	}
	select {
	case resp := <-respCh:
		reader := gen.NewStreamReader(resp.Body, 0)
		if _, ok, err := reader.Next(); !ok || err != nil {
			t.Fatalf("expected first echo, got ok=%v err=%v", ok, err)
		}
		cancel()
		resp.Body.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("no response")
	}
	// A cancel races the shutdown of both stream directions, and an RST with
	// NO_ERROR maps to a clean request EOF (RFC 9113 section 8.1), so the
	// result may legitimately read "completed". The invariant is that the
	// handler unblocks promptly and saw only the delivered message.
	report := waitForDone(t, client, base, "bidi-cancel")
	if report.MessagesIn != 1 || report.MessagesOut != 1 {
		t.Fatalf("unexpected report %+v", report)
	}
}
