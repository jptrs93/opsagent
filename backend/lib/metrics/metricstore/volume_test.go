package metricstore

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/parquet-go/parquet-go"
)

func volumeSample(t time.Time, instance, run int32, bytes int64) *apigen.MetricsSample {
	s := &apigen.MetricsSample{
		Time:                t.UnixMilli(),
		DeploymentID:        7,
		ScheduledInstanceID: instance,
		DeploymentVersion:   1,
		Run:                 run,
		NodeID:              3,
		MemCurrent:          apigen.Some[int64](100),
	}
	if bytes >= 0 {
		s.VolumeBytes = apigen.Some(bytes)
		s.VolumeInodes = apigen.Some(bytes / 1000)
	}
	return s
}

func heldSeries(t *testing.T, res RollupResult, field string) Series {
	t.Helper()
	var found []Series
	for _, s := range res.Series {
		if s.Field.Name == field {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d series for %s, want one for the deployment: %+v", len(found), field, found)
	}
	return found[0]
}

func TestToSampleCarriesAVolumeReading(t *testing.T) {
	key := metrics.TargetKey{DeploymentID: 1, Run: 1}
	with := toSample(&metrics.Sample{Key: key, Time: day1, Volume: &metrics.VolumeUsage{Bytes: 4096, Inodes: 2}}, 7)
	if with.VolumeBytes != apigen.Some[int64](4096) || with.VolumeInodes != apigen.Some[int64](2) {
		t.Fatalf("volume not carried: %+v", with)
	}
	if without := toSample(&metrics.Sample{Key: key, Time: day1}, 7); without.VolumeBytes.Present || without.VolumeInodes.Present {
		t.Fatalf("a sample without a reading has a volume: %+v", without)
	}
}

func TestRollupHoldsAVolumeReadingAcrossRunsOfTheDeployment(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var samples []*apigen.MetricsSample
	samples = append(samples, volumeSample(start.Add(-40*time.Minute), 1, 1, 1000))
	for i := range 60 {
		at := start.Add(time.Duration(i) * time.Minute)
		bytes := int64(-1)
		if i == 20 {
			bytes = 2000
		}
		samples = append(samples, volumeSample(at, 1, 1, bytes))
	}
	samples = append(samples, volumeSample(start.Add(50*time.Minute+10*time.Second), 2, 4, 3000))
	writeWAL(t, dir, samples...)

	fields, err := RequestFields([]string{"mem_current", "volume_bytes", "volume_inodes"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(time.Hour), DeploymentID: 7},
		Step:   10 * time.Minute,
		Fields: fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	vol := heldSeries(t, res, "volume_bytes")
	if vol.Key != (metrics.TargetKey{DeploymentID: 7}) || vol.NodeID != 3 || vol.Field.Kind != Held {
		t.Fatalf("held series identity = %+v node %d kind %d, want the deployment on the node", vol.Key, vol.NodeID, vol.Field.Kind)
	}
	want := []float64{1000, 1000, 2000, 2000, 2000, 3000}
	for b, w := range want {
		if vol.Values[b] != w {
			t.Fatalf("bucket %d = %v, want %v (all %v)", b, vol.Values[b], w, vol.Values)
		}
	}
	if inodes := heldSeries(t, res, "volume_inodes"); inodes.Values[5] != 3 {
		t.Fatalf("inodes = %v", inodes.Values)
	}
	runs := 0
	for _, s := range res.Series {
		if s.Field.Name == "mem_current" {
			runs++
			if s.Key.Run == 0 {
				t.Fatalf("a per-run field has a deployment-level series: %+v", s.Key)
			}
		}
	}
	if runs != 2 {
		t.Fatalf("%d mem_current series, want one per run", runs)
	}

	scoped, err := Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(time.Hour), DeploymentID: 7, ScheduledInstanceID: 2},
		Step:   10 * time.Minute,
		Fields: fields,
		Run:    4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if vol := heldSeries(t, scoped, "volume_bytes"); vol.Values[0] != 1000 || vol.Values[5] != 3000 {
		t.Fatalf("a run scope changed the volume series: %v", vol.Values)
	}
	for _, s := range scoped.Series {
		if s.Field.Name == "mem_current" && (s.Key.ScheduledInstanceID != 2 || s.Key.Run != 4) {
			t.Fatalf("the scope let another run through: %+v", s.Key)
		}
	}
}

func TestRollupStopsHoldingAnOldReadingAndAtTheLastSample(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var samples []*apigen.MetricsSample
	samples = append(samples, volumeSample(start.Add(5*time.Minute), 1, 1, 1000))
	for i := 1; i <= 24; i++ {
		samples = append(samples, volumeSample(start.Add(5*time.Minute+time.Duration(i)*5*time.Minute), 1, 1, -1))
	}
	writeWAL(t, dir, samples...)
	fields, _ := RequestFields([]string{"volume_bytes"})
	res, err := Rollup(context.Background(), dir, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(3 * time.Hour), DeploymentID: 7},
		Step:   15 * time.Minute,
		Fields: fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	vol := heldSeries(t, res, "volume_bytes")
	held := 0
	for b, v := range vol.Values {
		edge := time.Duration(b) * 15 * time.Minute
		switch {
		case edge-5*time.Minute < heldFor:
			if v != 1000 {
				t.Fatalf("bucket %d = %v, want the reading still held", b, v)
			}
			held++
		case !math.IsNaN(v):
			t.Fatalf("bucket %d = %v, want no value %s after the reading", b, v, edge-5*time.Minute)
		}
	}
	if held == 0 || held == len(vol.Values) {
		t.Fatalf("held %d of %d buckets", held, len(vol.Values))
	}

	stopped := t.TempDir()
	writeWAL(t, stopped, volumeSample(start.Add(5*time.Minute), 1, 1, 1000), volumeSample(start.Add(20*time.Minute), 1, 1, -1))
	res, err = Rollup(context.Background(), stopped, 3, RollupRequest{
		Query:  Query{From: start, To: start.Add(time.Hour), DeploymentID: 7},
		Step:   10 * time.Minute,
		Fields: fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	vol = heldSeries(t, res, "volume_bytes")
	if vol.Values[0] != 1000 || vol.Values[2] != 1000 || !math.IsNaN(vol.Values[3]) {
		t.Fatalf("values = %v, want the reading held only while the deployment has rows", vol.Values)
	}
}

type runRowBeforeVolumeColumns struct {
	Time         int64  `parquet:"time,timestamp(millisecond)"`
	DeploymentID int32  `parquet:"deployment_id"`
	Run          int32  `parquet:"run"`
	NodeID       int32  `parquet:"node_id"`
	OpenFds      *int64 `parquet:"open_fds"`
}

func TestSealedRunFileWithoutVolumeColumnsReadsThemAsAbsent(t *testing.T) {
	dir := t.TempDir()
	dd := dayDir(dir, day1)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dd, parquetName(7, 1)))
	if err != nil {
		t.Fatal(err)
	}
	fds := int64(9)
	w := parquet.NewGenericWriter[runRowBeforeVolumeColumns](f)
	if _, err := w.Write([]runRowBeforeVolumeColumns{
		{Time: day1.Add(10 * time.Second).UnixMilli(), DeploymentID: 1, Run: 1, NodeID: 7, OpenFds: &fds},
		{Time: day1.Add(20 * time.Second).UnixMilli(), DeploymentID: 1, Run: 1, NodeID: 7},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), dir, 7, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OpenFds != apigen.Some(fds) {
		t.Fatalf("the column the file has was misread: %+v", got)
	}
	for i, s := range got {
		if s.VolumeBytes.Present || s.VolumeInodes.Present {
			t.Errorf("sample %d has a volume from a file without the columns: %+v", i, s)
		}
	}
	fields, _ := RequestFields([]string{"volume_bytes", "open_fds"})
	res, err := Rollup(context.Background(), dir, 7, RollupRequest{
		Query:  Query{From: day1, To: day1.Add(time.Minute), DeploymentID: 1},
		Fields: fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Series {
		if s.Field.Name == "volume_bytes" {
			t.Fatalf("a volume series came from a file without the columns: %+v", s)
		}
	}
}

func TestLatestResponseListsEveryMeasuredVolume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := Start(ctx, t.TempDir(), 7)
	if got := s.LatestResponse(); len(got.Volumes) != 0 {
		t.Fatalf("volumes without a source: %+v", got.Volumes)
	}
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s.Volumes = func() []metrics.VolumeReading {
		return []metrics.VolumeReading{
			{DeploymentID: 3, Time: at, Usage: metrics.VolumeUsage{Bytes: 4096, Inodes: 2}, Walk: 1500 * time.Microsecond},
			{DeploymentID: 12, Time: at.Add(time.Minute), Usage: metrics.VolumeUsage{Bytes: 1 << 30, Inodes: 900}, Walk: 2 * time.Second},
		}
	}
	got := s.LatestResponse()
	if len(got.Entries) != 0 || len(got.Volumes) != 2 {
		t.Fatalf("response = %+v, want two volumes and no running container", got)
	}
	v := got.Volumes[1]
	if v.DeploymentID != 12 || v.NodeID != 7 || v.Time != at.Add(time.Minute).UnixMilli() || v.Bytes != 1<<30 || v.Inodes != 900 || v.WalkMs != 2000 {
		t.Fatalf("volume = %+v", v)
	}
}
