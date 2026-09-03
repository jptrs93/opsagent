package metricstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
)

var (
	day1 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	day2 = day1.AddDate(0, 0, 1)
)

func mkSample(dep, run int32, t time.Time, cpu int64) *apigen.MetricsSample {
	return &apigen.MetricsSample{
		Time:                t.UnixMilli(),
		DeploymentID:        dep,
		ScheduledInstanceID: dep * 10,
		SpecVersion:         1,
		Run:                 run,
		NodeID:              7,
		CpuUsageUsec:        &cpu,
	}
}

func writeWAL(t *testing.T, dir string, samples ...*apigen.MetricsSample) {
	t.Helper()
	w := &walWriter{dir: dir}
	if err := w.append(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRowMirrorsMetricsSample(t *testing.T) {
	rt := reflect.TypeFor[row]()
	st := reflect.TypeFor[apigen.MetricsSample]()
	if rt.NumField() != st.NumField() || len(rowFieldPairs) != st.NumField() {
		t.Fatalf("row has %d fields, MetricsSample has %d, %d paired", rt.NumField(), st.NumField(), len(rowFieldPairs))
	}
	for i := range st.NumField() {
		sf := st.Field(i)
		rf, ok := rt.FieldByName(sf.Name)
		if !ok || rf.Type != sf.Type {
			t.Errorf("row field %s missing or mistyped", sf.Name)
		}
	}
}

func TestWALRoundTripKeepsRecordsBeforeDamagedTail(t *testing.T) {
	dir := t.TempDir()
	in := []*apigen.MetricsSample{
		mkSample(1, 0, day1.Add(time.Minute), 100),
		mkSample(2, 0, day1.Add(time.Minute), 200),
		mkSample(1, 0, day1.Add(2*time.Minute), 150),
	}
	writeWAL(t, dir, in...)
	path := walPath(dir, day1)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0, 0, 0, 40, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var out []*apigen.MetricsSample
	n, clean, err := readWAL(path, nil, func(s *apigen.MetricsSample) bool {
		out = append(out, s)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || clean {
		t.Fatalf("records=%d clean=%v, want 3 records and a damaged tail", n, clean)
	}
	for i := range in {
		if !reflect.DeepEqual(in[i], out[i]) {
			t.Errorf("record %d = %+v, want %+v", i, out[i], in[i])
		}
	}
}

func TestWALWriterSplitsDays(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, mkSample(1, 0, day1.Add(time.Hour), 1), mkSample(1, 0, day2.Add(time.Hour), 2), mkSample(1, 0, day2.Add(2*time.Hour), 3))
	for _, tc := range []struct {
		day  time.Time
		want int
	}{{day1, 1}, {day2, 2}} {
		n, clean, err := readWAL(walPath(dir, tc.day), nil, func(*apigen.MetricsSample) bool { return true })
		if err != nil || !clean || n != tc.want {
			t.Errorf("%s: records=%d clean=%v err=%v, want %d", tc.day.Format(dayLayout), n, clean, err, tc.want)
		}
	}
}

func TestCompactSealsPastDaysAndCollectRoutesBothSources(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	var all []*apigen.MetricsSample
	for i := range 40 {
		ts := day1.Add(time.Duration(i) * 30 * time.Second)
		all = append(all, mkSample(1, 0, ts, int64(i*1000)), mkSample(2, 3, ts, int64(i*500)))
	}
	all = append(all, mkSample(1, 0, day2.Add(time.Minute), 999), mkSample(1, 1, day2.Add(2*time.Minute), 5))
	writeWAL(t, dir, all...)

	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("day1 wal still present: %v", err)
	}
	if _, err := os.Stat(walPath(dir, day2)); err != nil {
		t.Fatalf("day2 wal missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dayDir(dir, day1), parquetName(7, 1))); err != nil {
		t.Fatalf("day1 parquet missing: %v", err)
	}

	got, err := Collect(ctx, dir, 7, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("collected %d samples, want %d", len(got), len(all))
	}
	for i := 1; i < len(got); i++ {
		if compareKeyTime(got[i-1], got[i]) >= 0 {
			t.Fatalf("result not sorted at %d", i)
		}
	}
	groups := GroupByKey(got)
	if len(groups) != 3 {
		t.Fatalf("got %d key groups, want 3", len(groups))
	}
	rate, ok := Rate(groups[0][0], groups[0][1], Fields[0])
	if !ok || rate != 1000.0/30 {
		t.Fatalf("rate=%v ok=%v, want %v", rate, ok, 1000.0/30)
	}

	filtered, err := Collect(ctx, dir, 7, Query{DeploymentID: 2, From: day1.Add(10 * time.Minute), To: day1.Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 10 {
		t.Fatalf("filtered %d samples, want 10", len(filtered))
	}
	for _, s := range filtered {
		if s.DeploymentID != 2 || s.Time < day1.Add(10*time.Minute).UnixMilli() || s.Time >= day1.Add(15*time.Minute).UnixMilli() {
			t.Fatalf("unexpected sample %+v", s)
		}
	}

	stopped := 0
	if err := Scan(ctx, dir, 7, Query{}, func(*apigen.MetricsSample) bool { stopped++; return stopped < 5 }); err != nil {
		t.Fatal(err)
	}
	if stopped != 5 {
		t.Fatalf("scan yielded %d before stopping, want 5", stopped)
	}

	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	writeWAL(t, dir, all[:4]...)
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dayDir(dir, day1), parquetName(7, 2))); err != nil {
		t.Fatalf("second day1 parquet missing: %v", err)
	}
	again, err := Collect(ctx, dir, 7, Query{To: day2})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 80 {
		t.Fatalf("collected %d day1 samples after duplicate compaction, want 80", len(again))
	}
}

func TestCompactRemovesEmptyWAL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(walPath(dir, day1), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Compact(context.Background(), dir, 7, day2.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("empty wal still present: %v", err)
	}
	entries, _ := os.ReadDir(dayDir(dir, day1))
	if len(entries) != 0 {
		t.Fatalf("day dir has %d entries, want none", len(entries))
	}
}

func TestCompactRespectsGrace(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, mkSample(1, 0, day1.Add(23*time.Hour), 1))
	if err := Compact(context.Background(), dir, 7, day2.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); err != nil {
		t.Fatalf("wal compacted inside the grace window: %v", err)
	}
	if err := Compact(context.Background(), dir, 7, day2.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("wal not compacted after the grace window: %v", err)
	}
}

func TestRetainDeletesOldDays(t *testing.T) {
	dir := t.TempDir()
	old := day1.AddDate(0, 0, -100)
	if err := os.MkdirAll(dayDir(dir, old), 0o750); err != nil {
		t.Fatal(err)
	}
	writeWAL(t, dir, mkSample(1, 0, old.Add(time.Hour), 1), mkSample(1, 0, day1.Add(time.Hour), 2))
	if err := os.MkdirAll(dayDir(dir, day1), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Retain(dir, day2, DefaultRetention); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dayDir(dir, old), walPath(dir, old)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed: %v", p, err)
		}
	}
	for _, p := range []string{dayDir(dir, day1), walPath(dir, day1)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
}

func TestToSampleEncodesPresence(t *testing.T) {
	peak := uint64(42)
	fds := uint64(9)
	pids := uint64(3)
	s := metrics.Sample{
		Key:  metrics.TargetKey{DeploymentID: 1, ScheduledInstanceID: 2, Ordinal: 0, SpecVersion: 3, Run: 4},
		Time: day1.Add(time.Second),
		Cgroup: metrics.CgroupMetrics{
			CPU:         &metrics.CPUStats{UsageUsec: 10, ThrottledUsec: 1},
			Memory:      &metrics.MemoryStats{Current: 20, Peak: &peak, OOMKill: 1},
			Pids:        &pids,
			CPUPressure: &metrics.Pressure{Some: metrics.PressureLine{Avg10: 1.5, TotalUsec: 7}},
		},
		Net:     &metrics.NetMetrics{RxBytes: 5, TCP: metrics.TCPStates{Established: 2}},
		OpenFDs: &fds,
	}
	out := toSample(&s, 7)
	if Key(out) != s.Key || out.NodeID != 7 || out.Time != s.Time.UnixMilli() {
		t.Fatalf("identity mismatch: %+v", out)
	}
	if *out.CpuUsageUsec != 10 || *out.MemPeak != 42 || *out.Pids != 3 || *out.PsiCpuSomeAvg10 != 1.5 || *out.PsiCpuSomeTotalUsec != 7 || *out.NetRxBytes != 5 || *out.TcpEstablished != 2 || *out.OpenFds != 9 {
		t.Fatalf("values not carried: %+v", out)
	}
	if out.IoReadBytes != nil || out.PsiMemSomeAvg10 != nil {
		t.Fatalf("absent sections encoded as present: %+v", out)
	}
	host := metrics.Sample{Key: s.Key, Time: s.Time}
	if h := toSample(&host, 7); h.NetRxBytes != nil || h.CpuUsageUsec != nil || h.MemCurrent != nil {
		t.Fatalf("empty sample encoded values: %+v", h)
	}
}

func TestStoreConsumeWritesWALAndTracksLatest(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s := Start(ctx, dir, 7)
	key := metrics.TargetKey{DeploymentID: 1, Run: 0}
	other := metrics.TargetKey{DeploymentID: 2, Run: 0}
	now := time.Now()
	s.Consume(ctx, []metrics.Sample{{Key: key, Time: now}, {Key: other, Time: now}})
	s.Consume(ctx, []metrics.Sample{{Key: key, Time: now.Add(time.Second), Terminal: true}})
	latest := s.Latest()
	if len(latest) != 1 || Key(latest[0]) != other {
		t.Fatalf("latest = %+v, want only the live run", latest)
	}
	cancel()
	<-s.done
	got, err := Collect(context.Background(), dir, 7, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("collected %d samples, want 3", len(got))
	}
	terminal := 0
	for _, g := range got {
		if g.Terminal {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("%d terminal samples, want 1", terminal)
	}
}

func TestRateRejectsCrossKeyAndReset(t *testing.T) {
	a := mkSample(1, 0, day1, 100)
	b := mkSample(1, 0, day1.Add(10*time.Second), 200)
	c := mkSample(1, 1, day1.Add(20*time.Second), 5)
	d := mkSample(1, 0, day1.Add(20*time.Second), 50)
	cpu, _ := FieldByName("cpu_usage_usec")
	if r, ok := Rate(a, b, cpu); !ok || r != 10 {
		t.Fatalf("rate=%v ok=%v, want 10", r, ok)
	}
	if _, ok := Rate(b, c, cpu); ok {
		t.Fatal("rate across keys accepted")
	}
	if _, ok := Rate(b, d, cpu); ok {
		t.Fatal("rate over a decreasing counter accepted")
	}
	mem, _ := FieldByName("mem_current")
	if _, ok := Rate(a, b, mem); ok {
		t.Fatal("rate of a gauge accepted")
	}
}
