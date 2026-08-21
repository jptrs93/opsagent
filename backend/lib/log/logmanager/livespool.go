package logmanager

import (
	"sync"
	"time"
)

// The live spool manages the current live segment of the logs that is being pooled ready to make the next compacted chunk

type spoolRange struct {
	start StreamMarker
	end   StreamMarker
	size  int64
	count int
}

type LiveSegmentSpool struct {
	ranges    []spoolRange
	committed StreamMarker

	mu sync.Mutex
}

func newLiveSegmentSpool() *LiveSegmentSpool {
	return &LiveSegmentSpool{}
}

func (s *LiveSegmentSpool) Add(r WrappedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ranges) == 0 || s.ranges[len(s.ranges)-1].end.day != r.m.day {
		s.ranges = append(s.ranges, spoolRange{start: r.m, end: r.m, size: r.size, count: 1})
		return
	}
	cur := &s.ranges[len(s.ranges)-1]
	cur.end = r.m
	cur.size += r.size
	cur.count++
	// todo: future column stats for shredding
}

func (s *LiveSegmentSpool) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ranges)
}

func (s *LiveSegmentSpool) Reset(committed StreamMarker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ranges = nil
	s.committed = committed
}

func (s *LiveSegmentSpool) dropFirstLocked() {
	if len(s.ranges) == 0 {
		return
	}
	s.ranges = append(s.ranges[:0], s.ranges[1:]...)
}

func dayCommitDeadline(day int32) time.Time {
	return time.Unix(int64(day+1)*daySeconds, 0).UTC().Add(reorderGraceWindow)
}
