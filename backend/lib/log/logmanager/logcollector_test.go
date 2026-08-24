package logmanager

import (
	"testing"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const testDeploymentID int32 = 42
const testNodeID int32 = 7

func record(t *testing.T, at string, version int32, run int32, stream int8, line string) []byte {
	t.Helper()
	meta := logv2.RecordMeta{Version: version, Run: run, Deployment: testDeploymentID, Node: testNodeID, Stream: stream}
	return logv2.EncodeRecord(mustTime(t, at), meta, 0, []byte(line))
}

func lines(records []WrappedRecord) []string {
	var out []string
	for _, r := range records {
		out = append(out, string(r.record.Line))
	}
	return out
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSpoolTracksRangesPerDay(t *testing.T) {
	s := newLiveSegmentSpool()
	s.Add(WrappedRecord{m: StreamMarker{day: 10, byteOffset: 0}, size: 5})
	s.Add(WrappedRecord{m: StreamMarker{day: 10, byteOffset: 5}, size: 7})
	s.Add(WrappedRecord{m: StreamMarker{day: 11, byteOffset: 0}, size: 3})

	if s.Len() != 2 {
		t.Fatalf("ranges = %d, want 2", s.Len())
	}
	first := s.ranges[0]
	if first.size != 12 || first.count != 2 {
		t.Fatalf("first range size/count = %d/%d, want 12/2", first.size, first.count)
	}
	if first.start.byteOffset != 0 || first.end.byteOffset != 5 {
		t.Fatalf("first range = %d..%d, want 0..5", first.start.byteOffset, first.end.byteOffset)
	}
	if s.ranges[1].size != 3 || s.ranges[1].count != 1 {
		t.Fatalf("second range size/count = %d/%d, want 3/1", s.ranges[1].size, s.ranges[1].count)
	}
}

func TestSpoolCommitIfNeedOnEmptySpool(t *testing.T) {
	c := NewLogStreamCollector(testDeploymentID, nil)
	if err := c.CommitIfNeed(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitAll(); err != nil {
		t.Fatal(err)
	}
}
