package metricstore

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
)

const (
	queueDepth          = 64
	maintenanceInterval = time.Hour
)

var Default *Store

type Store struct {
	dir    string
	nodeID int32
	ch     chan []*apigen.MetricsSample
	done   chan struct{}

	mu      sync.Mutex
	latest  map[metrics.TargetKey]*apigen.MetricsSample
	prev    map[metrics.TargetKey]*apigen.MetricsSample
	dropped int
}

type LatestPair struct {
	Prev *apigen.MetricsSample
	Cur  *apigen.MetricsSample
}

func Start(ctx context.Context, dir string, nodeID int32) *Store {
	ctx = logu.AddTag(ctx, "Metrics")
	s := &Store{
		dir:    dir,
		nodeID: nodeID,
		ch:     make(chan []*apigen.MetricsSample, queueDepth),
		done:   make(chan struct{}),
		latest: make(map[metrics.TargetKey]*apigen.MetricsSample),
		prev:   make(map[metrics.TargetKey]*apigen.MetricsSample),
	}
	go s.writeLoop(ctx)
	go s.maintainLoop(ctx)
	return s
}

func (s *Store) Consume(ctx context.Context, samples []metrics.Sample) {
	batch := make([]*apigen.MetricsSample, len(samples))
	for i := range samples {
		batch[i] = toSample(&samples[i], s.nodeID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range batch {
		k := Key(b)
		if b.Terminal {
			delete(s.latest, k)
			delete(s.prev, k)
			continue
		}
		if cur, ok := s.latest[k]; ok {
			s.prev[k] = cur
		}
		s.latest[k] = b
	}
	select {
	case s.ch <- batch:
		if s.dropped > 0 {
			slog.WarnContext(ctx, fmt.Sprintf("metrics wal queue drained; %d batches were dropped", s.dropped))
			s.dropped = 0
		}
	default:
		s.dropped++
		if s.dropped == 1 {
			slog.WarnContext(ctx, "metrics wal queue full; dropping batches")
		}
	}
}

func (s *Store) Latest() []*apigen.MetricsSample {
	pairs := s.LatestPairs()
	out := make([]*apigen.MetricsSample, len(pairs))
	for i, p := range pairs {
		out[i] = p.Cur
	}
	return out
}

func (s *Store) LatestPairs() []LatestPair {
	s.mu.Lock()
	out := make([]LatestPair, 0, len(s.latest))
	for k, v := range s.latest {
		out = append(out, LatestPair{Prev: s.prev[k], Cur: v})
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b LatestPair) int { return compareKeyTime(a.Cur, b.Cur) })
	return out
}

func (s *Store) Scan(ctx context.Context, q Query, yield func(*apigen.MetricsSample) bool) error {
	return Scan(ctx, s.dir, s.nodeID, q, yield)
}

func (s *Store) Collect(ctx context.Context, q Query) ([]*apigen.MetricsSample, error) {
	return Collect(ctx, s.dir, s.nodeID, q)
}

func (s *Store) writeLoop(ctx context.Context) {
	defer close(s.done)
	w := &walWriter{dir: s.dir}
	failing := false
	write := func(batch []*apigen.MetricsSample) {
		err := w.append(batch)
		switch {
		case err != nil && !failing:
			failing = true
			slog.WarnContext(ctx, "appending metrics wal failed", "err", err)
		case err == nil && failing:
			failing = false
			slog.InfoContext(ctx, "appending metrics wal resumed")
		}
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case batch := <-s.ch:
					write(batch)
				default:
					if err := w.close(); err != nil {
						slog.WarnContext(ctx, "closing metrics wal failed", "err", err)
					}
					return
				}
			}
		case batch := <-s.ch:
			write(batch)
		}
	}
}

func (s *Store) maintainLoop(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) maintain(ctx context.Context) {
	now := time.Now()
	if err := Compact(ctx, s.dir, s.nodeID, now); err != nil {
		slog.WarnContext(ctx, "compacting metrics wal failed", "err", err)
	}
	if err := Retain(s.dir, now, DefaultRetention); err != nil {
		slog.WarnContext(ctx, "applying metrics retention failed", "err", err)
	}
}
