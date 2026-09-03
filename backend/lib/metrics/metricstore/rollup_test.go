package metricstore

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func rollupSample(t time.Time, key int32, cpu int64, mem int64) *apigen.MetricsSample {
	return &apigen.MetricsSample{
		Time:                t.UnixMilli(),
		DeploymentID:        7,
		ScheduledInstanceID: key,
		SpecVersion:         1,
		Run:                 1,
		NodeID:              3,
		CpuUsageUsec:        &cpu,
		MemCurrent:          &mem,
	}
}

func TestChooseStepAndAlign(t *testing.T) {
	base := time.Date(2026, 3, 1, 12, 7, 13, 0, time.UTC)
	if got := ChooseStep(base, base.Add(5*time.Minute), 300); got != 10*time.Second {
		t.Fatalf("5m step = %s", got)
	}
	if got := ChooseStep(base, base.Add(time.Hour), 300); got != 30*time.Second {
		t.Fatalf("1h step = %s", got)
	}
	if got := ChooseStep(base, base.Add(24*time.Hour), 300); got != 5*time.Minute {
		t.Fatalf("24h step = %s", got)
	}
	if got := ChooseStep(base, base.Add(30*24*time.Hour), 300); got != 3*time.Hour {
		t.Fatalf("30d step = %s", got)
	}
	start, buckets := AlignRange(base, base.Add(time.Hour), time.Minute)
	if start != base.Truncate(time.Minute) || buckets != 61 {
		t.Fatalf("align = %s %d", start, buckets)
	}
}

func TestRollupRatesAndMeans(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w := &walWriter{dir: dir}
	var samples []*apigen.MetricsSample
	for i := range 21 {
		mem := int64(100)
		if i%2 == 1 {
			mem = 300
		}
		samples = append(samples, rollupSample(start.Add(time.Duration(i)*30*time.Second), 1, int64(i)*15_000_000, mem))
	}
	for i := range 21 {
		cpu := int64(i) * 30_000_000
		if i >= 10 {
			cpu = int64(i-10) * 30_000_000
		}
		samples = append(samples, rollupSample(start.Add(time.Duration(i)*30*time.Second), 2, cpu, 50))
	}
	if err := w.append(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.append(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	cpu, _ := FieldByName("cpu_usage_usec")
	mem, _ := FieldByName("mem_current")
	res, err := Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start.Add(time.Minute), To: start.Add(9 * time.Minute), DeploymentID: 7},
		Step:   2 * time.Minute,
		Fields: []Field{cpu, mem},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Step != 2*time.Minute || res.Buckets != 5 || !res.Start.Equal(start) {
		t.Fatalf("grid = %s %d from %s", res.Step, res.Buckets, res.Start)
	}
	if res.Scanned != 40 {
		t.Fatalf("scanned = %d, duplicates were not skipped", res.Scanned)
	}
	if len(res.Series) != 4 {
		t.Fatalf("series = %d", len(res.Series))
	}
	find := func(key int32, name string) Series {
		for _, s := range res.Series {
			if s.Key.ScheduledInstanceID == key && s.Field.Name == name {
				return s
			}
		}
		t.Fatalf("missing series %d %s", key, name)
		return Series{}
	}
	approx := func(got, want float64) bool { return math.Abs(got-want) < 1e-6 }
	for b, v := range find(1, "cpu_usage_usec").Values {
		if !approx(v, 500_000) {
			t.Fatalf("run 1 cpu bucket %d = %v want 500000 usec/s", b, v)
		}
	}
	for b, v := range find(1, "mem_current").Values {
		if !approx(v, 200) {
			t.Fatalf("run 1 mem bucket %d = %v want 200", b, v)
		}
	}
	run2 := find(2, "cpu_usage_usec").Values
	for b, v := range run2 {
		if !approx(v, 1_000_000) {
			t.Fatalf("run 2 cpu bucket %d = %v want 1000000", b, v)
		}
	}
	if s := find(2, "mem_current"); len(s.Values) != 5 || !approx(s.Values[0], 50) {
		t.Fatalf("run 2 mem = %v", s.Values)
	}

	res, err = Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(2 * time.Minute), DeploymentID: 7, ScheduledInstanceID: 1},
		Step:   30 * time.Second,
		Fields: []Field{mem},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || len(res.Series[0].Values) != 4 {
		t.Fatalf("narrow = %+v", res.Series)
	}
	res, err = Rollup(context.Background(), dir, 3, RollupRequest{
		Query: Query{From: start, To: start.Add(2 * time.Minute), DeploymentID: 7},
		Run:   9,
	})
	if err != nil || len(res.Series) != 0 {
		t.Fatalf("run filter = %v %d", err, len(res.Series))
	}
}

func TestPeekSample(t *testing.T) {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := rollupSample(base, 1, 5, 5)
	if pt, dep := peekSample(s.Encode()); pt != base.UnixMilli() || dep != 7 {
		t.Fatalf("peek = %d %d", pt, dep)
	}
	zero := &apigen.MetricsSample{ScheduledInstanceID: 4}
	if pt, dep := peekSample(zero.Encode()); pt != 0 || dep != 0 {
		t.Fatalf("peek zero = %d %d", pt, dep)
	}
	if pt, dep := peekSample(nil); pt != 0 || dep != 0 {
		t.Fatalf("peek nil = %d %d", pt, dep)
	}
}

func TestRollupBeforeStartUsesLookback(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	w := &walWriter{dir: dir}
	if err := w.append([]*apigen.MetricsSample{
		rollupSample(start.Add(-30*time.Second), 1, 0, 1),
		rollupSample(start.Add(30*time.Second), 1, 60_000_000, 1),
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.close()
	cpu, _ := FieldByName("cpu_usage_usec")
	res, err := Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(time.Minute), DeploymentID: 7},
		Step:   time.Minute,
		Fields: []Field{cpu},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || math.Abs(res.Series[0].Values[0]-1_000_000) > 1e-6 {
		t.Fatalf("lookback rate = %+v", res.Series)
	}
}

func TestQueryResponseAndLatestEntry(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir, nodeID: 3}
	base := time.Now().Add(-10 * time.Minute)
	w := &walWriter{dir: dir}
	_ = w.append([]*apigen.MetricsSample{rollupSample(base, 1, 0, 5), rollupSample(base.Add(30*time.Second), 1, 30_000_000, 5)})
	_ = w.close()
	resp, err := s.QueryResponse(context.Background(), &apigen.MetricsQueryRequest{
		DeploymentID: 7,
		Fields:       []string{"cpu_usage_usec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StepMs != 30_000 || len(resp.Series) != 1 || resp.Series[0].Kind != int32(Counter) {
		t.Fatalf("resp = %+v", resp)
	}
	if _, err := s.QueryResponse(context.Background(), &apigen.MetricsQueryRequest{DeploymentID: 7, Fields: []string{"nope"}}); err == nil {
		t.Fatal("unknown field accepted")
	}
	prev := rollupSample(base, 1, 0, 5)
	cur := rollupSample(base.Add(10*time.Second), 1, 5_000_000, 5)
	e := LatestEntry(prev, cur)
	if len(e.Rates) != 1 || e.Rates[0].Field != "cpu_usage_usec" || math.Abs(e.Rates[0].PerSecond-500_000) > 1e-6 {
		t.Fatalf("rates = %+v", e.Rates)
	}
	if e := LatestEntry(nil, cur); len(e.Rates) != 0 {
		t.Fatalf("rates without prev = %+v", e.Rates)
	}
}
