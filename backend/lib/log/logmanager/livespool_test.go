package logmanager

import (
	"fmt"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func spoolRec(minute int64, offset int64, line string) WrappedRecord {
	at := minute*int64(time.Minute) + offset*int64(time.Second)
	return WrappedRecord{
		m:      StreamMarker{day: 1, bucket: 1, byteOffset: offset, time: at},
		record: apigen.RawLogLine{Time: at, Line: []byte(line)},
		size:   int64(len(line)),
	}
}

func TestSpoolMinuteAggregates(t *testing.T) {
	s := newLiveSegmentSpool()
	s.Add(spoolRec(100, 1, `{"level":"info","msg":"a"}`+"\n"))
	s.Add(spoolRec(100, 2, `{"level":"error","msg":"b"}`+"\n"))
	s.Add(spoolRec(102, 3, `{"level":"info","msg":"c"}`+"\n"))
	s.Add(spoolRec(101, 4, "plain\n"))
	snap, ok := s.aggSnapshot()
	if !ok {
		t.Fatal("unexpected overflow")
	}
	if len(snap.minutes) != 3 || snap.minutes[0].minute != 100 || snap.minutes[1].minute != 101 || snap.minutes[2].minute != 102 {
		t.Fatalf("minutes = %+v", snap.minutes)
	}
	if snap.minutes[0].count != 2 || snap.minutes[1].count != 1 || snap.minutes[2].count != 1 {
		t.Fatalf("counts = %+v", snap.minutes)
	}
	if snap.maxAdded != 102*int64(time.Minute)+3*int64(time.Second) {
		t.Fatalf("maxAdded = %d", snap.maxAdded)
	}
	idx := map[string]int{}
	for id, l := range snap.levels {
		idx[l] = id
	}
	if snap.minutes[0].levelCounts[idx["INFO"]] != 1 || snap.minutes[0].levelCounts[idx["ERROR"]] != 1 {
		t.Fatalf("minute 100 = %+v, levels = %v", snap.minutes[0], snap.levels)
	}
	if snap.minutes[1].levelCounts[idx[""]] != 1 {
		t.Fatalf("minute 101 = %+v, levels = %v", snap.minutes[1], snap.levels)
	}
	if snap.minutes[0].start.byteOffset != 1 || snap.minutes[1].start.byteOffset != 4 || snap.minutes[2].start.byteOffset != 3 {
		t.Fatalf("start markers = %+v", snap.minutes)
	}
	before := snap.minutes[0].count
	s.Add(spoolRec(100, 9, `{"level":"info","msg":"z"}`+"\n"))
	if snap.minutes[0].count != before {
		t.Fatal("snapshot shares state with live spool")
	}
}

func TestSpoolAggregatePrune(t *testing.T) {
	s := newLiveSegmentSpool()
	for m := int64(100); m < 106; m++ {
		s.Add(spoolRec(m, 5, "x\n"))
	}
	s.pruneAggregatesLocked(StreamMarker{day: 1, bucket: 1, byteOffset: 1, time: 102*int64(time.Minute) + 5*int64(time.Second)})
	snap, ok := s.aggSnapshot()
	if !ok {
		t.Fatal("unexpected overflow")
	}
	if len(snap.minutes) != 2 || snap.minutes[0].minute != 104 || snap.minutes[1].minute != 105 {
		t.Fatalf("minutes = %+v", snap.minutes)
	}
}

func TestSpoolLevelOverflowDisablesAggregates(t *testing.T) {
	s := newLiveSegmentSpool()
	for i := 0; i < maxSpoolLevels+1; i++ {
		s.Add(spoolRec(100, int64(i+1), fmt.Sprintf(`{"level":"L%d","msg":"x"}`, i)+"\n"))
	}
	if _, ok := s.aggSnapshot(); ok {
		t.Fatal("expected overflow to disable aggregates")
	}
	s.Reset(StreamMarker{})
	s.Add(spoolRec(100, 1, `{"level":"info","msg":"x"}`+"\n"))
	snap, ok := s.aggSnapshot()
	if !ok || len(snap.minutes) != 1 {
		t.Fatalf("ok = %v, minutes = %+v", ok, snap.minutes)
	}
}
