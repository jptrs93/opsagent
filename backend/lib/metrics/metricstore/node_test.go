package metricstore

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/parquet-go/parquet-go"
)

const gib = int64(1) << 30

func mkNodeSample(t time.Time, cpuUsec int64) *apigen.NodeMetricsSample {
	return &apigen.NodeMetricsSample{
		Time:             t.UnixMilli(),
		NodeID:           7,
		CpuUsageUsec:     apigen.Some(cpuUsec),
		CpuCount:         apigen.Some(int64(4)),
		Load1:            apigen.Some(0.5),
		MemTotal:         apigen.Some(16 * gib),
		MemAvailable:     apigen.Some(10 * gib),
		FsDataTotalBytes: apigen.Some(100 * gib),
		FsDataFreeBytes:  apigen.Some(40 * gib),
		FsDataAvailBytes: apigen.Some(35 * gib),
	}
}

func writeNodeWAL(t *testing.T, dir string, samples ...*apigen.NodeMetricsSample) {
	t.Helper()
	w := newWALWriter(nodeTable, dir)
	if err := w.append(samples); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}

func collectNode(t *testing.T, dir string) []*apigen.NodeMetricsSample {
	t.Helper()
	var out []*apigen.NodeMetricsSample
	err := scan(context.Background(), nodeTable, dir, 7, scanSpec[apigen.NodeMetricsSample]{}, func(s *apigen.NodeMetricsSample) bool {
		out = append(out, s)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNodeRowMirrorsNodeMetricsSample(t *testing.T) {
	rt := reflect.TypeFor[nodeRow]()
	st := reflect.TypeFor[apigen.NodeMetricsSample]()
	exported := 0
	for i := range st.NumField() {
		sf := st.Field(i)
		if !sf.IsExported() {
			continue
		}
		exported++
		rf, ok := rt.FieldByName(sf.Name)
		if !ok {
			t.Errorf("node row has no field %s", sf.Name)
			continue
		}
		if sf.Name != "ExtraFilesystems" && !rowTypeMirrors(rf.Type, sf.Type) {
			t.Errorf("node row field %s is mistyped", sf.Name)
		}
	}
	if rt.NumField() != exported || len(nodeRows.pairs) != exported-1 {
		t.Fatalf("node row has %d fields, NodeMetricsSample has %d exported, %d paired; only the filesystem map is mapped by hand", rt.NumField(), exported, len(nodeRows.pairs))
	}
}

func TestEveryNodeFieldReadsAValue(t *testing.T) {
	full := toNodeSample(&metrics.NodeSample{
		Time:           day1,
		CPU:            &metrics.HostCPU{UsageUsec: 1, UserUsec: 1, SystemUsec: 1, IowaitUsec: 1, StealUsec: 1, Count: 2},
		Load:           &metrics.LoadAvg{Load1: 1, Load5: 1, Load15: 1},
		Memory:         &metrics.HostMemory{Total: 8, Available: 3, Free: 1, Cached: 2, SwapTotal: 4, SwapFree: 1},
		CPUPressure:    &metrics.Pressure{},
		MemoryPressure: &metrics.Pressure{},
		IOPressure:     &metrics.Pressure{},
		Net:            &metrics.HostNet{RxBytes: 1},
		IO:             &metrics.IOStats{ReadBytes: 1},
		DataFS:         &metrics.FSUsage{TotalBytes: 100, FreeBytes: 40, AvailBytes: 30, InodesTotal: 50, InodesFree: 20},
	}, 7)
	seen := map[string]bool{}
	for _, f := range NodeFields {
		if seen[f.Name] {
			t.Errorf("field %s is listed twice", f.Name)
		}
		seen[f.Name] = true
		if _, ok := f.Value(full); !ok {
			t.Errorf("field %s has no value on a full sample", f.Name)
		}
		if _, ok := f.Value(&apigen.NodeMetricsSample{}); ok {
			t.Errorf("field %s has a value on an empty sample", f.Name)
		}
	}
	for name, want := range map[string]float64{"mem_used": 5, "swap_used": 3, "fs_data_used_bytes": 60, "fs_data_inodes_used": 30} {
		f, ok := fieldByName(NodeFields, name)
		if !ok {
			t.Fatalf("no field %s", name)
		}
		if got, _ := f.Value(full); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestToNodeSampleEncodesPresence(t *testing.T) {
	s := toNodeSample(&metrics.NodeSample{
		Time:    day1,
		DataFS:  &metrics.FSUsage{TotalBytes: 100, FreeBytes: 40, AvailBytes: 30},
		ExtraFS: map[string]metrics.FSUsage{"/mnt/nix": {TotalBytes: 9, FreeBytes: 4, AvailBytes: 3, InodesTotal: 2, InodesFree: 1}},
	}, 7)
	if s.NodeID != 7 || s.Time != day1.UnixMilli() {
		t.Fatalf("identity lost: %+v", s)
	}
	if s.CpuUsageUsec.Present || s.MemTotal.Present || s.Load1.Present || s.NetRxBytes.Present || s.IoReadBytes.Present || s.PsiCpuSomeAvg10.Present {
		t.Fatalf("a part that was not read encoded values: %+v", s)
	}
	if !s.FsDataTotalBytes.Present || s.FsDataFreeBytes.Value != 40 || s.FsDataAvailBytes.Value != 30 {
		t.Fatalf("data filesystem lost: %+v", s)
	}
	want := map[string]apigen.FilesystemUsage{"/mnt/nix": {TotalBytes: 9, FreeBytes: 4, AvailBytes: 3, InodesTotal: 2, InodesFree: 1}}
	if !reflect.DeepEqual(s.ExtraFilesystems, want) {
		t.Fatalf("extra filesystems = %+v, want %+v", s.ExtraFilesystems, want)
	}
	if empty := toNodeSample(&metrics.NodeSample{Time: day1}, 7); empty.ExtraFilesystems != nil || empty.FsDataTotalBytes.Present {
		t.Fatalf("an empty sample encoded filesystems: %+v", empty)
	}
}

func TestNodeSamplesRoundTripThroughTheWALAndASealedDay(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	withExtra := mkNodeSample(day1.Add(20*time.Second), 3_000_000)
	withExtra.ExtraFilesystems = map[string]apigen.FilesystemUsage{
		"/var/lib/opendeploy-nix": {TotalBytes: 500 * gib, FreeBytes: 200 * gib, AvailBytes: 190 * gib, InodesTotal: 1000, InodesFree: 400},
		"/mnt/logs":               {TotalBytes: 50 * gib, FreeBytes: 20 * gib, AvailBytes: 18 * gib},
	}
	in := []*apigen.NodeMetricsSample{
		mkNodeSample(day1.Add(10*time.Second), 1_000_000),
		withExtra,
		mkNodeSample(day2.Add(10*time.Second), 9_000_000),
	}
	writeNodeWAL(t, dir, in[1], in[0], in[2])

	fromWAL := collectNode(t, dir)
	if len(fromWAL) != 3 {
		t.Fatalf("read %d samples from the wal, want 3", len(fromWAL))
	}

	if err := compact(ctx, nodeTable, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("day one wal still present after compaction: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dayDir(dir, day1), parquetName(7, 1))); err != nil {
		t.Fatalf("no sealed file for day one: %v", err)
	}
	if _, err := os.Stat(walPath(dir, day2)); err != nil {
		t.Fatalf("the open day was compacted: %v", err)
	}

	got := collectNode(t, dir)
	if len(got) != len(in) {
		t.Fatalf("read %d samples, want %d", len(got), len(in))
	}
	for i := range in {
		if !reflect.DeepEqual(got[i], in[i]) {
			t.Errorf("sample %d = %+v, want %+v", i, got[i], in[i])
		}
	}
}

type nodeRowBeforeLaterColumns struct {
	Time         int64  `parquet:"time,timestamp(millisecond)"`
	NodeID       int32  `parquet:"node_id"`
	CpuUsageUsec *int64 `parquet:"cpu_usage_usec"`
}

func TestSealedFileWithoutLaterColumnsReadsThemAsAbsent(t *testing.T) {
	dir := t.TempDir()
	dd := dayDir(dir, day1)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dd, parquetName(7, 1)))
	if err != nil {
		t.Fatal(err)
	}
	cpu := int64(123)
	w := parquet.NewGenericWriter[nodeRowBeforeLaterColumns](f)
	if _, err := w.Write([]nodeRowBeforeLaterColumns{
		{Time: day1.Add(10 * time.Second).UnixMilli(), NodeID: 7, CpuUsageUsec: &cpu},
		{Time: day1.Add(20 * time.Second).UnixMilli(), NodeID: 7},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got := collectNode(t, dir)
	if len(got) != 2 {
		t.Fatalf("read %d samples, want 2", len(got))
	}
	if got[0].CpuUsageUsec != apigen.Some(cpu) || got[1].CpuUsageUsec.Present {
		t.Fatalf("the column the file has was misread: %+v %+v", got[0], got[1])
	}
	for i, s := range got {
		if s.MemTotal.Present || s.Load1.Present || s.FsDataTotalBytes.Present {
			t.Errorf("sample %d has a value for a column the file lacks: %+v", i, s)
		}
		if s.ExtraFilesystems != nil {
			t.Errorf("sample %d has filesystems from a file without the map column: %+v", i, s.ExtraFilesystems)
		}
	}
}

func TestNodeRollupGivesRatesGaugesAndDerivedFields(t *testing.T) {
	dir := t.TempDir()
	var samples []*apigen.NodeMetricsSample
	for i := range 13 {
		s := mkNodeSample(day1.Add(time.Duration(i)*10*time.Second), int64(i)*20_000_000)
		s.MemAvailable = apigen.Some((10 - int64(i/6)) * gib)
		samples = append(samples, s)
	}
	other := mkNodeSample(day1.Add(15*time.Second), 999)
	other.NodeID = 8
	samples = append(samples, other)
	writeNodeWAL(t, dir, samples...)

	fields, err := NodeRequestFields([]string{"cpu_usage_usec", "mem_used", "fs_data_used_bytes", "load1", "net_rx_bytes"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NodeRollup(context.Background(), dir, 7, day1, day1.Add(2*time.Minute), time.Minute, fields)
	if err != nil {
		t.Fatal(err)
	}
	if res.Buckets != 2 || res.Step != time.Minute || !res.Start.Equal(day1) || res.Scanned != 12 {
		t.Fatalf("plan = %+v, want two one-minute buckets over the node's 12 samples inside the range", res)
	}
	byName := map[string][]float64{}
	for _, ser := range res.Series {
		byName[ser.Field.Name] = ser.Values
	}
	if _, ok := byName["net_rx_bytes"]; ok {
		t.Fatal("a field with no data produced a series")
	}
	const cpuRate = 2_000_000.0
	for name, want := range map[string][]float64{
		"cpu_usage_usec":     {cpuRate, cpuRate},
		"mem_used":           {float64(6 * gib), float64(7 * gib)},
		"fs_data_used_bytes": {float64(60 * gib), float64(60 * gib)},
		"load1":              {0.5, 0.5},
	} {
		got := byName[name]
		if len(got) != 2 || math.Abs(got[0]-want[0]) > 1e-6 || math.Abs(got[1]-want[1]) > 1e-6 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	if _, err := NodeRequestFields([]string{"nope"}); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := NodeRollup(context.Background(), dir, 7, day1, day1, time.Minute, nil); err == nil {
		t.Fatal("an empty range was accepted")
	}
}

func TestNodeStoreTracksTheLatestSampleAndItsRates(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s := StartNode(ctx, dir, 7)
	if got := s.LatestResponse(); len(got.Entries) != 0 {
		t.Fatalf("latest before any sample = %+v, want none", got)
	}
	now := time.Now().Truncate(time.Second)
	sample := func(at time.Time, cpu uint64) metrics.NodeSample {
		return metrics.NodeSample{Time: at, CPU: &metrics.HostCPU{UsageUsec: cpu, Count: 4}, DataFS: &metrics.FSUsage{TotalBytes: 100, FreeBytes: 40, AvailBytes: 30}}
	}
	s.ConsumeNode(ctx, sample(now, 1_000_000))
	first := s.LatestResponse()
	if len(first.Entries) != 1 || len(first.Entries[0].Rates) != 0 || first.Entries[0].Sample.FsDataFreeBytes.Value != 40 {
		t.Fatalf("latest after one sample = %+v, want the sample without rates", first)
	}
	s.ConsumeNode(ctx, sample(now.Add(10*time.Second), 6_000_000))
	latest := s.LatestResponse()
	if len(latest.Entries) != 1 || latest.Entries[0].Sample.NodeID != 7 || latest.Entries[0].Sample.CpuUsageUsec.Value != 6_000_000 {
		t.Fatalf("latest = %+v, want the second sample of node 7", latest)
	}
	rates := map[string]float64{}
	for _, r := range latest.Entries[0].Rates {
		rates[r.Field] = r.PerSecond
	}
	if rates["cpu_usage_usec"] != 500_000 {
		t.Fatalf("cpu rate = %v, want 500000 usec per second", rates["cpu_usage_usec"])
	}
	if _, ok := rates["cpu_count"]; ok {
		t.Fatal("a gauge was given a rate")
	}

	resp, err := s.QueryResponse(ctx, &apigen.NodeMetricsQueryRequest{
		TimeStart: apigen.Some(now.Add(-time.Minute)),
		TimeEnd:   apigen.Some(now.Add(time.Minute)),
		Fields:    []string{"fs_data_used_bytes"},
	})
	cancel()
	<-s.done
	if err != nil {
		t.Fatal(err)
	}
	if got := collectNode(t, dir); len(got) != 2 {
		t.Fatalf("wal holds %d samples, want 2", len(got))
	}
	if len(resp.Series) > 1 {
		t.Fatalf("series = %+v, want at most the one requested field", resp.Series)
	}
	for _, ser := range resp.Series {
		if ser.NodeID != 7 || ser.Field != "fs_data_used_bytes" || ser.Kind != int32(Gauge) || len(ser.Values) != int(resp.Buckets) {
			t.Fatalf("series = %+v", ser)
		}
	}
}
