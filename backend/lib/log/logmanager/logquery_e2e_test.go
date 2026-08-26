package logmanager

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

func realDatasetManager(t *testing.T, root string) (*Manager, int32) {
	t.Helper()
	archiveRoot := filepath.Join(root, "log-archive")
	walRoot := filepath.Join(root, "run-logs")
	deps, err := os.ReadDir(archiveRoot)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tmpArchive := filepath.Join(tmp, "log-archive")
	tmpWal := filepath.Join(tmp, "run-logs")
	if err := os.MkdirAll(tmpArchive, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tmpWal, 0o750); err != nil {
		t.Fatal(err)
	}
	var deploymentID int32
	for _, d := range deps {
		if !d.IsDir() {
			continue
		}
		var id int32
		if _, err := fmt.Sscanf(d.Name(), "%d", &id); err != nil {
			continue
		}
		deploymentID = id
		src, err := filepath.Abs(filepath.Join(archiveRoot, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(src, filepath.Join(tmpArchive, d.Name())); err != nil {
			t.Fatal(err)
		}
		wsrc, err := filepath.Abs(filepath.Join(walRoot, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(wsrc); err == nil {
			if err := os.Symlink(wsrc, filepath.Join(tmpWal, d.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldArchive, oldWal := ainit.StaticConfig.LogArchiveDir, ainit.StaticConfig.LogWALDir
	ainit.StaticConfig.LogArchiveDir = tmpArchive
	ainit.StaticConfig.LogWALDir = tmpWal
	t.Cleanup(func() {
		ainit.StaticConfig.LogArchiveDir = oldArchive
		ainit.StaticConfig.LogWALDir = oldWal
	})
	db := logdb.Open(logDBPath())
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	dayDirs, err := os.ReadDir(filepath.Join(tmpArchive, fmt.Sprint(deploymentID)))
	if err != nil {
		t.Fatal(err)
	}
	for _, dd := range dayDirs {
		day, ok := parseDayDirName(dd.Name())
		if !ok {
			continue
		}
		files, err := os.ReadDir(filepath.Join(tmpArchive, fmt.Sprint(deploymentID), dd.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			var level int
			var minMs, maxMs, seq int64
			var node int32
			if _, err := fmt.Sscanf(f.Name(), "L%d_%d-%d_n%d_%d.parquet", &level, &minMs, &maxMs, &node, &seq); err != nil {
				continue
			}
			info, err := f.Info()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.InsertLogFile(ctx, logdb.InsertLogFileParams{
				DeploymentID: int64(deploymentID),
				Day:          int64(day),
				Level:        int64(level),
				Node:         int64(node),
				Seq:          seq,
				MinTime:      minMs * 1e6,
				MaxTime:      maxMs * 1e6,
				RowCount:     0,
				ByteSize:     info.Size(),
				CreatedAt:    clock().UnixMilli(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := NewLogStreamCollector(deploymentID, db)
	return &Manager{db: db, collectors: map[int32]*LogStreamCollector{deploymentID: c}}, deploymentID
}

func TestQueryRealDataset(t *testing.T) {
	if os.Getenv("LOGQUERY_E2E") == "" {
		t.Skip("set LOGQUERY_E2E=1 to run against test-logs/")
	}
	root := "../../../../test-logs"
	if _, err := os.Stat(root); err != nil {
		t.Skipf("test-logs not found: %v", err)
	}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	streamTiming(t, time.Minute, time.Millisecond, time.Millisecond)
	m, dep := realDatasetManager(t, root)
	reqs := map[string]*apigen.LogQueryRequest{
		"levelError": {
			DeploymentID:     dep,
			TimeStart:        mustTime(t, "2026-08-23T00:00:00Z"),
			TimeEnd:          mustTime(t, "2026-08-27T00:00:00Z"),
			Limit:            10000,
			HistogramBuckets: 90,
			Filters:          []*apigen.LogFilter{{Field: "level", Op: "in", Values: []string{"ERROR"}}},
		},
		"aggregatesOnly": {
			DeploymentID:     dep,
			TimeStart:        mustTime(t, "2026-08-23T00:00:00Z"),
			TimeEnd:          mustTime(t, "2026-08-27T00:00:00Z"),
			Limit:            -1,
			HistogramBuckets: 90,
		},
		"msgContains": {
			DeploymentID: dep,
			TimeStart:    mustTime(t, "2026-08-23T00:00:00Z"),
			TimeEnd:      mustTime(t, "2026-08-27T00:00:00Z"),
			Limit:        1000,
			Filters:      []*apigen.LogFilter{{Op: "contains", Value: "error"}},
		},
	}
	for name, req := range reqs {
		t.Run(name, func(t *testing.T) {
			start := clock()
			fast, err := m.Query(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			fastDur := clock().Sub(start)
			start = clock()
			forceFullScan = true
			t.Cleanup(func() { forceFullScan = false })
			full, err := m.Query(context.Background(), req)
			forceFullScan = false
			if err != nil {
				t.Fatal(err)
			}
			fullDur := clock().Sub(start)
			t.Logf("two-pass: %v (scanned %d, matched %d, returned %d, warnings %v)",
				fastDur.Round(time.Millisecond), fast.Stats.ScannedRows, fast.Stats.MatchedRows, fast.Stats.ReturnedRows, fast.Warnings)
			t.Logf("full scan: %v (scanned %d, matched %d, returned %d, warnings %v)",
				fullDur.Round(time.Millisecond), full.Stats.ScannedRows, full.Stats.MatchedRows, full.Stats.ReturnedRows, full.Warnings)
			fast.Stats.TookMs, full.Stats.TookMs = 0, 0
			fast.Stats.SampledRows, full.Stats.SampledRows = 0, 0
			fast.Fields, full.Fields = nil, nil
			if !reflect.DeepEqual(fast.Stats, full.Stats) {
				t.Fatalf("stats diverge:\ntwo-pass = %+v\nfull = %+v", fast.Stats, full.Stats)
			}
			if !reflect.DeepEqual(fast.Histogram, full.Histogram) {
				t.Fatal("histograms diverge")
			}
			if len(fast.Records) != len(full.Records) {
				t.Fatalf("record counts diverge: %d vs %d", len(fast.Records), len(full.Records))
			}
			for i := range fast.Records {
				if !reflect.DeepEqual(fast.Records[i], full.Records[i]) {
					t.Fatalf("record %d diverges:\ntwo-pass = %+v\nfull = %+v", i, fast.Records[i], full.Records[i])
				}
			}
		})
	}
}
