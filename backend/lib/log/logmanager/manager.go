package logmanager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jptrs93/goutil/contextu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

var (
	deploymentScanInterval = fileListInterval
	defaultSearchLimit     = 5_000
	defaultSearchWindow    = 12 * time.Hour
	fieldStatsSample       = int64(5_000)
)

type scheduledInstanceStore interface {
	FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState
	MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func())
}

type Manager struct {
	db          *logdb.Queries
	collectors  map[int32]*LogStreamCollector
	mu          sync.Mutex
	scanStopped chan struct{}
}

func StartManager(ctx context.Context, store scheduledInstanceStore, predicate storage.ScheduledInstancePredicate) *Manager {
	m := &Manager{
		db:          logdb.Open(logDBPath()),
		collectors:  map[int32]*LogStreamCollector{},
		scanStopped: make(chan struct{}),
	}
	snapshot, updates, unsub := store.MustFetchScheduledSnapshotAndSubscribe(predicate)
	producing := map[int32]int32{}
	m.alignProducers(producing, snapshot)
	go m.runProducerAlignment(ctx, store, predicate, producing, updates, unsub)
	go func() {
		defer close(m.scanStopped)
		for ctx.Err() == nil {
			m.startNewCollectors()
			contextu.Sleep(ctx, deploymentScanInterval)
		}
	}()
	return m
}

func (m *Manager) runProducerAlignment(ctx context.Context, store scheduledInstanceStore, predicate storage.ScheduledInstancePredicate, producing map[int32]int32, updates chan apigen.ScheduledInstanceState, unsub func()) {
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			m.alignProducers(producing, store.FetchScheduledSnapshot(predicate))
		}
	}
}

func (m *Manager) alignProducers(producing map[int32]int32, items []apigen.ScheduledInstanceState) {
	desired := map[int32]int32{}
	for _, it := range items {
		if it.Config.Spec.OpendeploySpec != nil {
			continue
		}
		switch it.Status.Runner.Status {
		case apigen.RunningStatus_STARTING, apigen.RunningStatus_RUNNING, apigen.RunningStatus_CRASHED:
			desired[it.Instance.ID] = it.Instance.DeploymentID
		}
	}
	for id, dep := range desired {
		if _, ok := producing[id]; !ok {
			producing[id] = dep
			m.AlignCollecting(dep, 1)
		}
	}
	for id, dep := range producing {
		if _, ok := desired[id]; !ok {
			delete(producing, id)
			m.AlignCollecting(dep, -1)
		}
	}
}

func (m *Manager) startNewCollectors() {
	entries, err := os.ReadDir(ainit.StaticConfig.LogWALDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		if m.collector(int32(id)) != nil {
			continue
		}
		m.AlignCollecting(int32(id), 0)
	}
}

func (m *Manager) AlignCollecting(deploymentID int32, runningCountChange int) {
	m.mu.Lock()
	c := m.collectors[deploymentID]
	if c == nil {
		c = NewLogStreamCollector(deploymentID, m.db)
		m.collectors[deploymentID] = c
	}
	m.mu.Unlock()
	c.AlignCollecting(runningCountChange)
}

func (m *Manager) collector(deploymentID int32) *LogStreamCollector {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.collectors[deploymentID]
}

func (m *Manager) queryCollector(ctx context.Context, deploymentID int32) (*LogStreamCollector, error) {
	if c := m.collector(deploymentID); c != nil {
		return c, nil
	}
	c := NewLogStreamCollector(deploymentID, m.db)
	committed, err := c.loadCommittedMarker(ctx)
	if err != nil {
		return nil, err
	}
	c.liveSpool.Reset(committed)
	return c, nil
}

// Query runs the one-shot structured log query: matching records plus the
// per-level histogram over the full range, the total match count, and
// per-field sampled value stats.
func (m *Manager) Query(ctx context.Context, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	c, err := m.queryCollector(ctx, req.DeploymentID)
	if err != nil {
		return nil, err
	}
	from, till, err := resolveQueryScope(req.TimeStart, req.TimeEnd)
	if err != nil {
		return nil, err
	}
	filters, err := compileFilters(req.Filters)
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	switch {
	case limit < 0:
		limit = 0
	case limit == 0 || limit > defaultSearchLimit:
		limit = defaultSearchLimit
	}
	buckets := int(req.HistogramBuckets)
	if buckets > maxHistogramBuckets {
		buckets = maxHistogramBuckets
	}
	newestFirst := true
	switch req.Order {
	case "", "desc":
	case "asc":
		newestFirst = false
	default:
		return nil, apigen.NewApiErr(fmt.Sprintf("Unknown order %q", req.Order), "invalid_order", http.StatusBadRequest)
	}
	return c.runQuery(ctx, queryParams{
		from:        from,
		till:        till,
		limit:       limit,
		newestFirst: newestFirst,
		includeRaw:  req.IncludeRaw,
		buckets:     buckets,
		specVersion: req.SpecVersion,
		filters:     filters,
	})
}
