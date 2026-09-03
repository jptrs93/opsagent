package metricstore

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
)

const (
	MinStep          = 10 * time.Second
	DefaultMaxPoints = 300
	MaxBuckets       = 2000
	rateLookback     = 3 * time.Minute
)

var stepLadder = []time.Duration{
	10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
	15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour,
	6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

type Series struct {
	Key    metrics.TargetKey
	NodeID int32
	Field  Field
	Values []float64
}

type RollupRequest struct {
	Query
	Step        time.Duration
	Fields      []Field
	SpecVersion int32
	Run         int32
}

type RollupResult struct {
	Start   time.Time
	Step    time.Duration
	Buckets int
	Series  []Series
	Scanned int
}

func ChooseStep(from, to time.Time, maxPoints int) time.Duration {
	if maxPoints <= 0 {
		maxPoints = DefaultMaxPoints
	}
	span := to.Sub(from)
	for _, s := range stepLadder {
		if span/s <= time.Duration(maxPoints) {
			return s
		}
	}
	last := stepLadder[len(stepLadder)-1]
	n := (span + last*time.Duration(maxPoints) - 1) / (last * time.Duration(maxPoints))
	return last * n
}

func AlignRange(from, to time.Time, step time.Duration) (time.Time, int) {
	start := from.Truncate(step)
	buckets := int((to.Sub(start) + step - 1) / step)
	if buckets < 1 {
		buckets = 1
	}
	return start, buckets
}

func (s *Store) Rollup(ctx context.Context, req RollupRequest) (RollupResult, error) {
	return Rollup(ctx, s.dir, s.nodeID, req)
}

func Rollup(ctx context.Context, dir string, nodeID int32, req RollupRequest) (RollupResult, error) {
	if req.From.IsZero() || req.To.IsZero() || !req.To.After(req.From) {
		return RollupResult{}, errInvalidRange
	}
	step := req.Step
	if step < MinStep {
		step = ChooseStep(req.From, req.To, DefaultMaxPoints)
	}
	start, buckets := AlignRange(req.From, req.To, step)
	if buckets > MaxBuckets {
		return RollupResult{}, errTooManyBuckets
	}
	fields := req.Fields
	if len(fields) == 0 {
		fields = Fields
	}
	q := req.Query
	q.From = start.Add(-rateLookback)
	q.To = start.Add(step * time.Duration(buckets))
	grid := bucketGrid{startMs: start.UnixMilli(), stepMs: step.Milliseconds(), buckets: buckets}
	grid.endMs = grid.startMs + grid.stepMs*int64(buckets)
	accs := map[metrics.TargetKey]*rollupAcc{}
	scanned := 0
	err := Scan(ctx, dir, nodeID, q, func(s *apigen.MetricsSample) bool {
		if req.SpecVersion != 0 && s.SpecVersion != req.SpecVersion {
			return true
		}
		if req.Run != 0 && s.Run != req.Run {
			return true
		}
		k := Key(s)
		acc := accs[k]
		if acc == nil {
			acc = &rollupAcc{key: k, nodeID: s.NodeID, sum: make([][]float64, len(fields)), weight: make([][]float64, len(fields))}
			accs[k] = acc
		} else if s.Time <= acc.prev.Time {
			return true
		}
		scanned++
		acc.fold(s, fields, grid)
		return true
	})
	if err != nil {
		return RollupResult{}, err
	}
	keys := make([]metrics.TargetKey, 0, len(accs))
	for k := range accs {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, CompareKey)
	res := RollupResult{Start: start, Step: step, Buckets: buckets, Scanned: scanned}
	for _, k := range keys {
		res.Series = append(res.Series, accs[k].series(fields, buckets)...)
	}
	return res, nil
}

type bucketGrid struct {
	startMs, stepMs, endMs int64
	buckets                int
}

type rollupAcc struct {
	key    metrics.TargetKey
	nodeID int32
	prev   *apigen.MetricsSample
	sum    [][]float64
	weight [][]float64
}

func (a *rollupAcc) fold(s *apigen.MetricsSample, fields []Field, g bucketGrid) {
	for i, f := range fields {
		if f.Kind == Counter {
			if a.prev == nil {
				continue
			}
			rate, ok := Rate(a.prev, s, f)
			if !ok || s.Time <= g.startMs || a.prev.Time >= g.endMs {
				continue
			}
			lo, hi := max(a.prev.Time, g.startMs), min(s.Time, g.endMs)
			for b := (lo - g.startMs) / g.stepMs; b < int64(g.buckets) && g.startMs+b*g.stepMs < hi; b++ {
				edge0, edge1 := g.startMs+b*g.stepMs, g.startMs+(b+1)*g.stepMs
				overlap := float64(min(hi, edge1) - max(lo, edge0))
				if overlap <= 0 {
					continue
				}
				a.add(i, int(b), rate*overlap, overlap, g.buckets)
			}
			continue
		}
		v, ok := f.Value(s)
		if !ok || s.Time < g.startMs || s.Time >= g.endMs {
			continue
		}
		a.add(i, int((s.Time-g.startMs)/g.stepMs), v, 1, g.buckets)
	}
	a.prev = s
}

func (a *rollupAcc) add(field, bucket int, sum, weight float64, buckets int) {
	if a.sum[field] == nil {
		a.sum[field] = make([]float64, buckets)
		a.weight[field] = make([]float64, buckets)
	}
	a.sum[field][bucket] += sum
	a.weight[field][bucket] += weight
}

func (a *rollupAcc) series(fields []Field, buckets int) []Series {
	var out []Series
	for i, f := range fields {
		if a.sum[i] == nil {
			continue
		}
		values := make([]float64, buckets)
		for b := range values {
			if a.weight[i][b] > 0 {
				values[b] = a.sum[i][b] / a.weight[i][b]
			} else {
				values[b] = math.NaN()
			}
		}
		out = append(out, Series{Key: a.key, NodeID: a.nodeID, Field: f, Values: values})
	}
	return out
}
