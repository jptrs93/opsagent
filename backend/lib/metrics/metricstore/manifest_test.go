package metricstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func dayOneSamples() []*apigen.MetricsSample {
	var all []*apigen.MetricsSample
	for i := range 40 {
		ts := day1.Add(time.Duration(i) * 30 * time.Second)
		all = append(all, mkSample(1, 0, ts, int64(i*1000)), mkSample(2, 3, ts, int64(i*500)))
	}
	return append(all, mkSample(5, 0, day1.Add(time.Hour), 7))
}

func dayNames(t *testing.T, dd string) []string {
	t.Helper()
	entries, err := os.ReadDir(dd)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestCompactSealsOneFilePerDeploymentAndAManifestDescribesThem(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	writeWAL(t, dir, dayOneSamples()...)
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dd := dayDir(dir, day1)
	want := []string{"d1_n7_1.parquet", "d2_n7_1.parquet", "d5_n7_1.parquet", "n7_1.manifest"}
	if got := dayNames(t, dd); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("day one holds %v, want %v", got, want)
	}
	files, err := SealedFiles(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("listed %d sealed files, want 3", len(files))
	}
	rows := map[int32]int64{1: 40, 2: 40, 5: 1}
	for _, f := range files {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if !bytes.Equal(sum[:], f.Sha256) || f.SizeBytes != int64(len(b)) {
			t.Fatalf("deployment %d: the manifest describes other bytes than the file holds", f.DeploymentID)
		}
		if f.Rows != rows[f.DeploymentID] || f.Seq != 1 || f.Day != DayNumber(day1) {
			t.Fatalf("deployment %d: got %+v", f.DeploymentID, f)
		}
		if f.Path != RunFilePath(dir, f.Day, f.DeploymentID, 7, f.Seq) {
			t.Fatalf("deployment %d is at %s, the path helper says %s", f.DeploymentID, f.Path, RunFilePath(dir, f.Day, f.DeploymentID, 7, f.Seq))
		}
	}
	if files[0].MinTime != day1.UnixMilli() || files[0].MaxTime != day1.Add(39*30*time.Second).UnixMilli() {
		t.Fatalf("deployment 1 spans %d..%d", files[0].MinTime, files[0].MaxTime)
	}
	if other, _ := SealedFiles(dir, 8); len(other) != 0 {
		t.Fatalf("node 8 has no files here, listed %d", len(other))
	}
}

func TestADeploymentQueryOpensOnlyThatDeploymentsFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	writeWAL(t, dir, dayOneSamples()...)
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dayDir(dir, day1), splitParquetName(1, 7, 1)), []byte("not parquet"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := Collect(ctx, dir, 7, Query{DeploymentID: 2})
	if err != nil {
		t.Fatalf("a query for deployment 2 read another deployment's file: %v", err)
	}
	if len(got) != 40 {
		t.Fatalf("collected %d samples of deployment 2, want 40", len(got))
	}
	if _, err := Collect(ctx, dir, 7, Query{}); err == nil {
		t.Fatal("a query for every deployment skipped the damaged file")
	}
}

func TestCompactionInterruptedAfterTheManifestIsFinishedNotRepeated(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	all := dayOneSamples()
	writeWAL(t, dir, all...)
	wal, err := os.ReadFile(walPath(dir, day1))
	if err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dd := dayDir(dir, day1)
	for _, dep := range []int32{2, 5} {
		name := filepath.Join(dd, splitParquetName(dep, 7, 1))
		if err := os.Rename(name, name+tmpExt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(walPath(dir, day1), wal, 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := Collect(ctx, dir, 7, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("read %d samples while the renames were unfinished, want %d", len(got), len(all))
	}
	n := 0
	if err := Scan(ctx, dir, 7, Query{}, func(*apigen.MetricsSample) bool { n++; return true }); err != nil {
		t.Fatal(err)
	}
	if n != len(all) {
		t.Fatalf("a scan yielded %d samples while the renames were unfinished, want each of the %d once", n, len(all))
	}
	if files, _ := SealedFiles(dir, 7); len(files) != 1 || files[0].DeploymentID != 1 {
		t.Fatalf("only the renamed file is listed until the rest are, got %+v", files)
	}

	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := []string{"d1_n7_1.parquet", "d2_n7_1.parquet", "d5_n7_1.parquet", "n7_1.manifest"}
	if got := dayNames(t, dd); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("day one holds %v, want %v", got, want)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("the wal of a finished compaction is still present: %v", err)
	}
	if files, _ := SealedFiles(dir, 7); len(files) != 3 {
		t.Fatalf("listed %d sealed files after recovery, want 3", len(files))
	}
}

func TestAWALThatIsAlreadySealedIsRemovedNotSealedAgain(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	writeWAL(t, dir, dayOneSamples()...)
	wal, err := os.ReadFile(walPath(dir, day1))
	if err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPath(dir, day1), wal, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("the wal is still present: %v", err)
	}
	if got := dayNames(t, dayDir(dir, day1)); len(got) != 4 {
		t.Fatalf("the same wal was sealed twice: %v", got)
	}
}

func TestAManifestWithAMissingFileDoesNotCostTheWALItsRows(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	all := dayOneSamples()
	writeWAL(t, dir, all...)
	wal, err := os.ReadFile(walPath(dir, day1))
	if err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	dd := dayDir(dir, day1)
	if err := os.Remove(filepath.Join(dd, splitParquetName(2, 7, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPath(dir, day1), wal, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := []string{"d1_n7_2.parquet", "d2_n7_2.parquet", "d5_n7_2.parquet", "n7_2.manifest"}
	if got := dayNames(t, dd); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("day one holds %v, want %v", got, want)
	}
	got, err := Collect(ctx, dir, 7, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("read %d samples after the day was compacted again, want %d", len(got), len(all))
	}
	if _, err := os.Stat(walPath(dir, day1)); !os.IsNotExist(err) {
		t.Fatalf("the wal is still present: %v", err)
	}
}

func TestCompactionInterruptedBeforeTheManifestStartsOver(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	all := dayOneSamples()
	writeWAL(t, dir, all...)
	dd := dayDir(dir, day1)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{splitParquetName(1, 7, 1) + tmpExt, manifestName(7, 1) + tmpExt, "sortbuf-1" + tmpExt} {
		if err := os.WriteFile(filepath.Join(dd, name), []byte("half written"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := Collect(ctx, dir, 7, Query{}); err != nil || len(got) != len(all) {
		t.Fatalf("read %d samples beside an unfinished compaction (err %v), want %d", len(got), err, len(all))
	}
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	want := []string{"d1_n7_1.parquet", "d2_n7_1.parquet", "d5_n7_1.parquet", "n7_1.manifest"}
	if got := dayNames(t, dd); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("day one holds %v, want %v", got, want)
	}
	if got, err := Collect(ctx, dir, 7, Query{}); err != nil || len(got) != len(all) {
		t.Fatalf("read %d samples after the compaction (err %v), want %d", len(got), err, len(all))
	}
}

func TestARunFileSealedBeforeTheSplitIsReadAndNeverListed(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	all := dayOneSamples()
	dd := dayDir(dir, day1)
	if err := os.MkdirAll(dd, 0o750); err != nil {
		t.Fatal(err)
	}
	wal := appendFrames(all)
	if _, _, err := writeSealed(runTable, dd, parquetName(7, 1), wal, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dd, parquetName(7, 1)+tmpExt), filepath.Join(dd, parquetName(7, 1))); err != nil {
		t.Fatal(err)
	}
	if err := backfillManifests(ctx, runTable, dir, 7); err != nil {
		t.Fatal(err)
	}
	if files, _ := SealedFiles(dir, 7); len(files) != 0 {
		t.Fatalf("a file that holds every deployment was listed as a chunk: %+v", files)
	}
	if got, err := Collect(ctx, dir, 7, Query{DeploymentID: 2}); err != nil || len(got) != 40 {
		t.Fatalf("read %d samples of deployment 2 from the old file (err %v), want 40", len(got), err)
	}
	writeWAL(t, dir, mkSample(9, 0, day1.Add(2*time.Hour), 1))
	if err := Compact(ctx, dir, 7, day2.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dd, splitParquetName(9, 7, 2))); err != nil {
		t.Fatalf("the next compaction did not take the number after the old file's: %v", err)
	}
}

func appendFrames(samples []*apigen.MetricsSample) []byte {
	var b []byte
	for _, s := range samples {
		b = appendFrame(b, runTable.encode(s))
	}
	return b
}

func TestNodeDaySealsOneFileAndOlderFilesGetAManifest(t *testing.T) {
	metricsDir := t.TempDir()
	dir := NodeDir(metricsDir)
	ctx := context.Background()
	writeNodeWAL(t, dir, mkNodeSample(day1.Add(10*time.Second), 1), mkNodeSample(day1.Add(20*time.Second), 2), mkNodeSample(day2.Add(10*time.Second), 3))
	if err := compact(ctx, nodeTable, dir, 7, day2.AddDate(0, 0, 1).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dayDir(dir, day2), manifestName(7, 1))); err != nil {
		t.Fatal(err)
	}
	files, err := SealedFiles(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Rows != 2 || files[0].DeploymentID != 0 || files[0].Day != DayNumber(day1) {
		t.Fatalf("only day one has a manifest, got %+v", files)
	}
	if files[0].Path != NodeFilePath(metricsDir, files[0].Day, 7, 1) {
		t.Fatalf("day one is at %s, the path helper says %s", files[0].Path, NodeFilePath(metricsDir, files[0].Day, 7, 1))
	}
	if err := backfillManifests(ctx, nodeTable, dir, 7); err != nil {
		t.Fatal(err)
	}
	files, err = SealedFiles(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("listed %d files after describing the older one, want 2", len(files))
	}
	b, err := os.ReadFile(files[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if !bytes.Equal(sum[:], files[1].Sha256) || files[1].SizeBytes != int64(len(b)) || files[1].Rows != 1 ||
		files[1].MinTime != day2.Add(10*time.Second).UnixMilli() || files[1].MaxTime != files[1].MinTime {
		t.Fatalf("the described file reads %+v", files[1])
	}
	before := dayNames(t, dayDir(dir, day2))
	if err := backfillManifests(ctx, nodeTable, dir, 7); err != nil {
		t.Fatal(err)
	}
	if after := dayNames(t, dayDir(dir, day2)); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Fatalf("describing twice changed the day: %v then %v", before, after)
	}
}
