package logmanager

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// The live spool manages the current live segment of the logs that is being pooled ready to make the next compacted chunk

const maxSpoolLevels = 64

type spoolRange struct {
	start StreamMarker
	end   StreamMarker
	size  int64
	count int
}

type MinuteAggregate struct {
	minute      int64
	start       StreamMarker
	count       int64
	levelCounts []int64
}

type LiveSegmentSpool struct {
	ranges     []spoolRange
	committed  StreamMarker
	aggregates []MinuteAggregate
	levels     []string
	levelIDs   map[string]int
	overflow   bool
	maxAdded   int64
	sc         lineScanner // level extraction scratch, guarded by mu

	mu sync.Mutex
}

type aggSnapshot struct {
	minutes   []MinuteAggregate
	levels    []string
	committed StreamMarker
	maxAdded  int64
}

func newLiveSegmentSpool() *LiveSegmentSpool {
	return &LiveSegmentSpool{}
}

func (s *LiveSegmentSpool) Add(r WrappedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addAggregateLocked(r)
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

func (s *LiveSegmentSpool) addAggregateLocked(r WrappedRecord) {
	if r.record.Time > s.maxAdded {
		s.maxAdded = r.record.Time
	}
	if s.overflow {
		return
	}
	level := s.sc.levelOf(r.record.Line)
	id, ok := s.levelIDs[level]
	if !ok {
		if len(s.levels) >= maxSpoolLevels {
			s.overflow = true
			s.aggregates = nil
			return
		}
		if s.levelIDs == nil {
			s.levelIDs = map[string]int{}
		}
		id = len(s.levels)
		s.levels = append(s.levels, level)
		s.levelIDs[level] = id
	}
	a := s.aggregateForLocked(r.record.Time/int64(time.Minute), r.m)
	a.count++
	for len(a.levelCounts) <= id {
		a.levelCounts = append(a.levelCounts, 0)
	}
	a.levelCounts[id]++
}

func (s *LiveSegmentSpool) aggregateForLocked(minute int64, m StreamMarker) *MinuteAggregate {
	n := len(s.aggregates)
	if n > 0 && s.aggregates[n-1].minute == minute {
		return &s.aggregates[n-1]
	}
	if n == 0 || minute > s.aggregates[n-1].minute {
		s.aggregates = append(s.aggregates, MinuteAggregate{minute: minute, start: m})
		return &s.aggregates[len(s.aggregates)-1]
	}
	i, found := slices.BinarySearchFunc(s.aggregates, minute, func(a MinuteAggregate, target int64) int {
		return cmp.Compare(a.minute, target)
	})
	if found {
		return &s.aggregates[i]
	}
	s.aggregates = slices.Insert(s.aggregates, i, MinuteAggregate{minute: minute, start: m})
	return &s.aggregates[i]
}

func (s *LiveSegmentSpool) pruneAggregatesLocked(committed StreamMarker) {
	if committed.isZero() {
		return
	}
	limit := committed.time/int64(time.Minute) + 1
	i := 0
	for i < len(s.aggregates) && s.aggregates[i].minute <= limit {
		i++
	}
	if i > 0 {
		s.aggregates = append(s.aggregates[:0], s.aggregates[i:]...)
	}
}

func (s *LiveSegmentSpool) aggSnapshot() (aggSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow {
		return aggSnapshot{}, false
	}
	minutes := make([]MinuteAggregate, len(s.aggregates))
	for i, a := range s.aggregates {
		a.levelCounts = slices.Clone(a.levelCounts)
		minutes[i] = a
	}
	return aggSnapshot{
		minutes:   minutes,
		levels:    slices.Clone(s.levels),
		committed: s.committed,
		maxAdded:  s.maxAdded,
	}, true
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
	s.aggregates = nil
	s.levels = nil
	s.levelIDs = nil
	s.overflow = false
	s.maxAdded = 0
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
