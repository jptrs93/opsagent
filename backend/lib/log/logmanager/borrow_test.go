package logmanager

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

// TestRetainedLinesSurviveBufferReuse writes a WAL several times larger than
// the reader's buffer, retains a spread of records through the retain heap
// while the stream is still running, and checks each kept line afterwards.
// Lines borrow the reader's buffer, so a missing copy shows up as a kept line
// whose content belongs to a later record.
func TestRetainedLinesSurviveBufferReuse(t *testing.T) {
	streamTiming(t, 0, 0, 0)
	dir := walEnv(t)
	const n = 4000 // ~250 bytes each, well past the 128 KB buffer
	var chunks [][]byte
	for i := range n {
		line := fmt.Sprintf(`{"level":"INFO","seq":%d,"pad":"%s"}`+"\n", i, strings.Repeat("x", 200))
		chunks = append(chunks, record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, line))
	}
	writeBucket(t, dir, "20260615_1430", chunks...)

	ret := &retainHeap{capacity: 50, newest: false}
	var i, kept int64
	for r, err := range streamRecords(deadProducer(), testDeploymentID, StreamMarker{}, false) {
		if err != nil {
			t.Fatal(err)
		}
		if i%80 == 0 {
			ret.offer(retainedRec{rec: r.record, fileIdx: -1, rowIdx: i})
			kept++
		}
		i++
	}
	if kept != n/80 {
		t.Fatalf("offered %d, want %d", kept, n/80)
	}
	for _, rr := range ret.sorted() {
		want := fmt.Sprintf(`"seq":%d,`, rr.rowIdx)
		if !strings.Contains(string(rr.rec.Line), want) {
			t.Fatalf("retained record %d holds line %q", rr.rowIdx, truncate(rr.rec.Line, 60))
		}
	}

	// sortedByTime buffers and so must own what it holds
	var last apigen.RawLogLine
	var count int
	for r, err := range sortedByTime(streamRecords(deadProducer(), testDeploymentID, StreamMarker{}, false)) {
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			last = r.record
		}
		count++
	}
	if count != n || !strings.Contains(string(last.Line), `"seq":0,`) {
		t.Fatalf("sorted stream: count %d, first line %q", count, truncate(last.Line, 60))
	}
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
