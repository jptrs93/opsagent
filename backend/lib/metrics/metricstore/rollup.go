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
	Step              time.Duration
	Fields            []Field
	DeploymentVersion int32
	Run               int32
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

type rollupPlan struct {
	start   time.Time
	step    time.Duration
	buckets int
	grid    bucketGrid
	scan    timeRange
}

func planRollup(from, to time.Time, step time.Duration) (rollupPlan, error) {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return rollupPlan{}, errInvalidRange
	}
	if step < MinStep {
		step = ChooseStep(from, to, DefaultMaxPoints)
	}
	start, buckets := AlignRange(from, to, step)
	if buckets > MaxBuckets {
		return rollupPlan{}, errTooManyBuckets
	}
	grid := bucketGrid{startMs: start.UnixMilli(), stepMs: step.Milliseconds(), buckets: buckets}
	grid.endMs = grid.startMs + grid.stepMs*int64(buckets)
	return rollupPlan{
		start:   start,
		step:    step,
		buckets: buckets,
		grid:    grid,
		scan:    timeRange{start.Add(-rateLookback), start.Add(step * time.Duration(buckets))},
	}, nil
}

func (s *Store) Rollup(ctx context.Context, req RollupRequest) (RollupResult, error) {
	return Rollup(ctx, s.dir, s.nodeID, req)
}

func Rollup(ctx context.Context, dir string, nodeID int32, req RollupRequest) (RollupResult, error) {
	plan, err := planRollup(req.From, req.To, req.Step)
	if err != nil {
		return RollupResult{}, err
	}
	fields := req.Fields
	if len(fields) == 0 {
		fields = Fields
	}
	q := req.Query
	q.From, q.To = plan.scan.from, plan.scan.to
	type runAcc struct {
		nodeID int32
		acc    *rollupAcc[apigen.MetricsSample]
	}
	accs := map[metrics.TargetKey]*runAcc{}
	scanned := 0
	err = Scan(ctx, dir, nodeID, q, func(s *apigen.MetricsSample) bool {
		if req.DeploymentVersion != 0 && s.DeploymentVersion != req.DeploymentVersion {
			return true
		}
		if req.Run != 0 && s.Run != req.Run {
			return true
		}
		k := Key(s)
		run := accs[k]
		if run == nil {
			run = &runAcc{nodeID: s.NodeID, acc: newRollupAcc[apigen.MetricsSample](len(fields))}
			accs[k] = run
		} else if s.Time <= run.acc.prevTime {
			return true
		}
		scanned++
		run.acc.fold(s, s.Time, fields, plan.grid)
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
	res := RollupResult{Start: plan.start, Step: plan.step, Buckets: plan.buckets, Scanned: scanned}
	for _, k := range keys {
		run := accs[k]
		for i, f := range fields {
			if values := run.acc.values(i, plan.buckets); values != nil {
				res.Series = append(res.Series, Series{Key: k, NodeID: run.nodeID, Field: f, Values: values})
			}
		}
	}
	return res, nil
}

type bucketGrid struct {
	startMs, stepMs, endMs int64
	buckets                int
}

type rollupAcc[S any] struct {
	prev     *S
	prevTime int64
	sum      [][]float64
	weight   [][]float64
}

func newRollupAcc[S any](fields int) *rollupAcc[S] {
	return &rollupAcc[S]{sum: make([][]float64, fields), weight: make([][]float64, fields)}
}

func (a *rollupAcc[S]) fold(s *S, t int64, fields []FieldOf[S], g bucketGrid) {
	for i, f := range fields {
		if f.Kind == Counter {
			if a.prev == nil {
				continue
			}
			rate, ok := counterRate(a.prev, s, a.prevTime, t, f)
			if !ok || t <= g.startMs || a.prevTime >= g.endMs {
				continue
			}
			lo, hi := max(a.prevTime, g.startMs), min(t, g.endMs)
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
		if !ok || t < g.startMs || t >= g.endMs {
			continue
		}
		a.add(i, int((t-g.startMs)/g.stepMs), v, 1, g.buckets)
	}
	a.prev, a.prevTime = s, t
}

func (a *rollupAcc[S]) add(field, bucket int, sum, weight float64, buckets int) {
	if a.sum[field] == nil {
		a.sum[field] = make([]float64, buckets)
		a.weight[field] = make([]float64, buckets)
	}
	a.sum[field][bucket] += sum
	a.weight[field][bucket] += weight
}

func (a *rollupAcc[S]) values(field, buckets int) []float64 {
	if a.sum[field] == nil {
		return nil
	}
	values := make([]float64, buckets)
	for b := range values {
		if a.weight[field][b] > 0 {
			values[b] = a.sum[field][b] / a.weight[field][b]
		} else {
			values[b] = math.NaN()
		}
	}
	return values
}
