package logmigrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// preSeqRow is the pre-migration schema from just before the seq column
// landed (level/msg present, no seq).
type preSeqRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	Level           string `parquet:"level,optional"`
	Msg             string `parquet:"msg,optional"`
	RawMessage      []byte `parquet:"raw_message"`
}

// preLevelRow is the oldest schema, predating level/msg as well.
type preLevelRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	RawMessage      []byte `parquet:"raw_message"`
}

func writeParquet[T any](t *testing.T, path string, rows []T, metadata map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := parquet.NewGenericWriter[T](f)
	if len(rows) > 0 {
		if _, err := w.Write(rows); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range metadata {
		w.SetKeyValueMetadata(k, v)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func dayDir(t *testing.T, archiveDir string, dep, day string) string {
	t.Helper()
	dir := filepath.Join(archiveDir, dep, day)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openFooter(t *testing.T, path string) *parquet.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func TestRunMigratesLegacyFiles(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "7", "20250101")
	oldest := filepath.Join(dir, "L0_1-9_n1_555.parquet")
	writeParquet(t, oldest, []preLevelRow{
		{Time: 9, Version: 1, Run: 2, Node: 1, RawMessage: []byte("late\n")},
		{Time: 2, Version: 1, Run: 2, Node: 1, RawMessage: []byte("early\n")},
	}, nil)
	mid := filepath.Join(dir, "L0_1-9_n1_556.parquet")
	writeParquet(t, mid, []preSeqRow{
		{Time: 5, Version: 3, Run: 4, Node: 1, Level: "info", Msg: "hello", RawMessage: []byte("hello\n")},
	}, map[string]string{"deployment": "7"})

	s := Run(context.Background(), walDir, archiveDir)
	if s.FilesScanned != 2 || s.FilesMigrated != 2 || s.FilesFailed != 0 || s.RowsMigrated != 3 {
		t.Fatalf("summary = %+v", s)
	}
	if !s.Clean() {
		t.Fatalf("not clean: %+v", s)
	}

	rows, err := parquet.ReadFile[logRow](oldest)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Time != 2 || rows[1].Time != 9 {
		t.Fatalf("rows not resorted: %+v", rows)
	}
	for _, r := range rows {
		if r.Seq != 0 || r.Level != "" || r.Msg != "" {
			t.Fatalf("unexpected fill values: %+v", r)
		}
	}
	pf := openFooter(t, oldest)
	if _, ok := pf.Schema().Lookup("seq"); !ok {
		t.Fatal("seq column missing after migration")
	}
	if v, _ := pf.Lookup(metadataSortedKey); v != metadataSortedVal {
		t.Fatalf("sorted metadata = %q", v)
	}
	if v, _ := pf.Lookup(metadataDeploymentKey); v != "7" {
		t.Fatalf("deployment metadata = %q", v)
	}

	midRows, err := parquet.ReadFile[logRow](mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(midRows) != 1 || midRows[0].Level != "info" || midRows[0].Msg != "hello" || midRows[0].Seq != 0 {
		t.Fatalf("mid rows = %+v", midRows)
	}
}

func TestRunIdempotent(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "3", "20250102")
	path := filepath.Join(dir, "L0_1-9_n2_777.parquet")
	writeParquet(t, path, []preLevelRow{{Time: 1, RawMessage: []byte("x\n")}}, nil)

	first := Run(context.Background(), walDir, archiveDir)
	if first.FilesMigrated != 1 {
		t.Fatalf("first = %+v", first)
	}
	st1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	second := Run(context.Background(), walDir, archiveDir)
	if second.FilesMigrated != 0 || second.FilesAlreadyModern != 1 || !second.Clean() {
		t.Fatalf("second = %+v", second)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st1.Size() != st2.Size() || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("modern file was rewritten on second run")
	}
}

func TestModernFileWithoutSortedFlagIsAnomaly(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "4", "20250103")
	path := filepath.Join(dir, "L0_1-9_n1_1.parquet")
	writeParquet(t, path, []logRow{{Time: 1, Seq: 5, RawMessage: []byte("x\n")}}, map[string]string{"deployment": "4"})

	s := Run(context.Background(), walDir, archiveDir)
	if s.FilesAlreadyModern != 1 || s.FilesMigrated != 0 || s.Anomalies != 1 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
	rows, err := parquet.ReadFile[logRow](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Seq != 5 {
		t.Fatalf("modern file changed: %+v", rows)
	}
}

func TestLeftoverTempFilesSwept(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "5", "20250104")
	real := filepath.Join(dir, "L0_1-9_n1_2.parquet")
	writeParquet(t, real, []preLevelRow{{Time: 1, RawMessage: []byte("x\n")}}, nil)
	for _, name := range []string{"L0_1-9_n1_2.parquet.migrate.tmp", "migrate-sortbuf-123"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	collectorTmp := filepath.Join(dir, "99.parquet.tmp")
	if err := os.WriteFile(collectorTmp, []byte("junk"), 0o640); err != nil {
		t.Fatal(err)
	}

	s := Run(context.Background(), walDir, archiveDir)
	if s.TmpFilesSwept != 2 || s.FilesMigrated != 1 || s.StrayFiles != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if _, err := os.Stat(collectorTmp); err != nil {
		t.Fatal("collector orphan tmp must be left for the collector's own sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, "migrate-sortbuf-123")); !os.IsNotExist(err) {
		t.Fatal("sort buffer leftover not swept")
	}
}

func TestCorruptParquetCountedFailed(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "6", "20250105")
	path := filepath.Join(dir, "L0_1-9_n1_3.parquet")
	if err := os.WriteFile(path, []byte("not parquet"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := Run(context.Background(), walDir, archiveDir)
	if s.FilesFailed != 1 || s.FilesMigrated != 0 || s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "not parquet" {
		t.Fatalf("original modified: %q err=%v", b, err)
	}
}

func TestZeroRowLegacyFile(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := dayDir(t, archiveDir, "0", "20250106")
	path := filepath.Join(dir, "L0_0-0_n1_4.parquet")
	writeParquet(t, path, []preLevelRow{}, nil)

	s := Run(context.Background(), walDir, archiveDir)
	if s.FilesMigrated != 1 || s.RowsMigrated != 0 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
	rows, err := parquet.ReadFile[logRow](path)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
}

func TestSkipsAndStrays(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	// Catalog files and non-numeric dirs at the root are skipped silently.
	if err := os.WriteFile(filepath.Join(archiveDir, "log.db"), []byte("db"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(archiveDir, "lost+found"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Empty deployment dir is fine.
	if err := os.MkdirAll(filepath.Join(archiveDir, "9"), 0o750); err != nil {
		t.Fatal(err)
	}
	dir := dayDir(t, archiveDir, "8", "20250107")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := Run(context.Background(), walDir, archiveDir)
	if s.StrayFiles != 1 || s.FilesScanned != 0 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestRunOnMissingDirs(t *testing.T) {
	base := t.TempDir()
	s := Run(context.Background(), filepath.Join(base, "nope-wal"), filepath.Join(base, "nope-archive"))
	if s != (Summary{}) || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}
