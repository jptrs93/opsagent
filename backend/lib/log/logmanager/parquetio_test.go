package logmanager

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parquet-go/parquet-go"
)

func TestArchiveWriterRoundTripsAllColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), archiveFileName(archiveLevelBatch, 2, 9, testNodeID, 1234))
	w, err := newArchiveWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []logRow{
		{Time: 5, Version: 1, Run: 2, Node: 7, InstanceOrdinal: 0, Stream: 0, RawMessage: []byte("alpha\n")},
		{Time: 2, Version: 1, Run: 2, Node: 7, InstanceOrdinal: 3, Stream: 1, RawMessage: []byte("beta\n")},
		{Time: 9, Version: 4, Run: 5, Node: 8, InstanceOrdinal: 1, Stream: 0, RawMessage: []byte("gamma\n")},
	}
	for _, row := range want {
		if err := w.append(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.finish(map[string]string{"deployment": "42"}); err != nil {
		t.Fatal(err)
	}

	got, err := parquet.ReadFile[logRow](path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

func TestScanArchiveAggFallsBackOnFilesWithoutLevelColumn(t *testing.T) {
	type preLevelRow struct {
		Time       int64  `parquet:"time"`
		RawMessage []byte `parquet:"raw_message"`
	}
	path := filepath.Join(t.TempDir(), "old"+archiveExt)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := parquet.NewGenericWriter[preLevelRow](f)
	if _, err := w.Write([]preLevelRow{{Time: 1, RawMessage: []byte("x\n")}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	agg := &thinAgg{tillN: 1 << 62}
	rows, handled, err := scanArchiveAgg(context.Background(), path, 0, 1<<62, agg)
	if err != nil || handled || rows != 0 || agg.scanned != 0 {
		t.Fatalf("rows = %d, handled = %v, err = %v, scanned = %d", rows, handled, err, agg.scanned)
	}
	got, err := parquet.ReadFile[logRow](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Level != "" {
		t.Fatalf("rows = %+v", got)
	}
}

func TestArchiveWriterTracksTimeBoundsOutOfOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounds"+archiveExt)
	w, err := newArchiveWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{50, 10, 90, 30} {
		if err := w.append(logRow{Time: at, RawMessage: []byte("x\n")}); err != nil {
			t.Fatal(err)
		}
	}
	if w.minTime != 10 || w.maxTime != 90 {
		t.Fatalf("bounds = %d..%d, want 10..90", w.minTime, w.maxTime)
	}
	if w.count != 4 {
		t.Fatalf("count = %d, want 4", w.count)
	}
	if err := w.finish(nil); err != nil {
		t.Fatal(err)
	}
}
