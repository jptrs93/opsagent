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

type sink[S any, R any] struct {
	t      *table[S, R]
	dir    string
	nodeID int32
	ch     chan []*S
	done   chan struct{}

	mu      sync.Mutex
	dropped int
}

func startSink[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32) *sink[S, R] {
	k := &sink[S, R]{
		t:      t,
		dir:    dir,
		nodeID: nodeID,
		ch:     make(chan []*S, queueDepth),
		done:   make(chan struct{}),
	}
	go k.writeLoop(ctx)
	go k.maintainLoop(ctx)
	return k
}

func (k *sink[S, R]) enqueue(ctx context.Context, batch []*S) {
	k.mu.Lock()
	defer k.mu.Unlock()
	select {
	case k.ch <- batch:
		if k.dropped > 0 {
			slog.WarnContext(ctx, fmt.Sprintf("%s wal queue drained; %d batches were dropped", k.t.name, k.dropped))
			k.dropped = 0
		}
	default:
		k.dropped++
		if k.dropped == 1 {
			slog.WarnContext(ctx, fmt.Sprintf("%s wal queue full; dropping batches", k.t.name))
		}
	}
}

func (k *sink[S, R]) writeLoop(ctx context.Context) {
	defer close(k.done)
	w := newWALWriter(k.t, k.dir)
	failing := false
	write := func(batch []*S) {
		err := w.append(batch)
		switch {
		case err != nil && !failing:
			failing = true
			slog.WarnContext(ctx, fmt.Sprintf("appending %s wal failed", k.t.name), "err", err)
		case err == nil && failing:
			failing = false
			slog.InfoContext(ctx, fmt.Sprintf("appending %s wal resumed", k.t.name))
		}
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case batch := <-k.ch:
					write(batch)
				default:
					if err := w.close(); err != nil {
						slog.WarnContext(ctx, fmt.Sprintf("closing %s wal failed", k.t.name), "err", err)
					}
					return
				}
			}
		case batch := <-k.ch:
			write(batch)
		}
	}
}

func (k *sink[S, R]) maintainLoop(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		k.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (k *sink[S, R]) maintain(ctx context.Context) {
	now := time.Now()
	if err := compact(ctx, k.t, k.dir, k.nodeID, now); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("compacting %s wal failed", k.t.name), "err", err)
	}
	if err := Retain(k.dir, now, DefaultRetention); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("applying %s retention failed", k.t.name), "err", err)
	}
}

type Store struct {
	dir    string
	nodeID int32
	sink   *sink[apigen.MetricsSample, row]
	done   chan struct{}

	mu     sync.Mutex
	latest map[metrics.TargetKey]*apigen.MetricsSample
	prev   map[metrics.TargetKey]*apigen.MetricsSample
}

type LatestPair struct {
	Prev *apigen.MetricsSample
	Cur  *apigen.MetricsSample
}

func Start(ctx context.Context, dir string, nodeID int32) *Store {
	ctx = logu.AddTag(ctx, "Metrics")
	k := startSink(ctx, runTable, dir, nodeID)
	return &Store{
		dir:    dir,
		nodeID: nodeID,
		sink:   k,
		done:   k.done,
		latest: make(map[metrics.TargetKey]*apigen.MetricsSample),
		prev:   make(map[metrics.TargetKey]*apigen.MetricsSample),
	}
}

func (s *Store) Consume(ctx context.Context, samples []metrics.Sample) {
	batch := make([]*apigen.MetricsSample, len(samples))
	for i := range samples {
		batch[i] = toSample(&samples[i], s.nodeID)
	}
	s.mu.Lock()
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
	s.mu.Unlock()
	s.sink.enqueue(ctx, batch)
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
