package logmanager

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

func maintenanceEnv(t *testing.T, m *Manager) *Manager {
	t.Helper()
	oldGrace := rewriteGrace
	rewriteGrace = 0
	t.Cleanup(func() { rewriteGrace = oldGrace })
	mm := newManager(m.db)
	mm.collectors = m.collectors
	return mm
}

func step(t *testing.T, m *Manager) bool {
	t.Helper()
	did, err := m.maintenanceStep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.unlinkWG.Wait()
	return did
}

func queryMsgsAll(t *testing.T, m *Manager, filters ...*apigen.LogFilter) *apigen.LogQueryResponse {
	t.Helper()
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		TimeStart:    mustTime(t, "2026-06-01T00:00:00Z"),
		TimeEnd:      mustTime(t, "2026-07-01T00:00:00Z"),
		Filters:      filters,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Stats.TookMs = 0
	return resp
}

func twoBatchFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1400",
		record(t, "2026-06-15T14:00:01Z", 1, 1, logv2.StreamStdout, typedLines[0]+"\n"),
		record(t, "2026-06-15T14:00:02Z", 1, 1, logv2.StreamStdout, typedLines[1]+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, typedLines[2]+"\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, typedLines[3]+"\n"),
	)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func disableRetention(t *testing.T) {
	t.Helper()
	old := retentionDays
	retentionDays = 1 << 20
	t.Cleanup(func() { retentionDays = old })
}

func TestRollupMergesOnBytes(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	inputs := listFiles(t, m.db)
	if len(inputs) != 2 {
		t.Fatalf("files = %+v", inputs)
	}
	oldTarget := rollupTargetBytes
	rollupTargetBytes = inputs[0].ByteSize + inputs[1].ByteSize
	t.Cleanup(func() { rollupTargetBytes = oldTarget })
	before := queryMsgsAll(t, m, &apigen.LogFilter{Field: "user", Op: "eq", Value: "68"})
	if !step(t, m) {
		t.Fatal("roll-up did not run")
	}
	files := listFiles(t, m.db)
	if len(files) != 1 || files[0].Level != archiveLevelRollup || files[0].RowCount != 4 {
		t.Fatalf("files after roll-up = %+v", files)
	}
	if files[0].MinTime != inputs[1].MinTime || files[0].MaxTime != inputs[0].MaxTime {
		t.Fatalf("bounds = %+v from %+v", files[0], inputs)
	}
	for _, in := range inputs {
		if _, err := os.Stat(archiveFilePath(testDeploymentID, in)); !os.IsNotExist(err) {
			t.Fatalf("input %s still present: %v", logFileName(in), err)
		}
	}
	lines := archiveLines(t, files[0], 0)
	if len(lines) != 4 || lines[0] != typedLines[0]+"\n" || lines[3] != typedLines[3]+"\n" {
		t.Fatalf("merged lines = %#v", lines)
	}
	after := queryMsgsAll(t, m, &apigen.LogFilter{Field: "user", Op: "eq", Value: "68"})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("before = %+v\nafter = %+v", before, after)
	}
	all := queryMsgsAll(t, m)
	if all.Stats.MatchedRows != 4 {
		t.Fatalf("stats = %+v", all.Stats)
	}
	if step(t, m) {
		t.Fatal("second step should find nothing to do")
	}
}

func TestRollupDayEndSweep(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	stubClock(t, "2026-06-15T23:00:00Z")
	if step(t, m) {
		t.Fatal("roll-up ran before day end")
	}
	stubClock(t, "2026-06-16T00:20:00Z")
	if !step(t, m) {
		t.Fatal("day-end roll-up did not run")
	}
	files := listFiles(t, m.db)
	if len(files) != 1 || files[0].Level != archiveLevelRollup {
		t.Fatalf("files = %+v", files)
	}
	if step(t, m) {
		t.Fatal("lone file should be left alone")
	}
}

func TestRollupSplitsOutputByTargetBytes(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	inputs := listFiles(t, m.db)
	var total int64
	for _, f := range inputs {
		total += f.ByteSize
	}
	oldTarget := rollupTargetBytes
	rollupTargetBytes = total / 2
	t.Cleanup(func() { rollupTargetBytes = oldTarget })
	if !step(t, m) {
		t.Fatal("roll-up did not run")
	}
	files := listFiles(t, m.db)
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	if files[0].RowCount+files[1].RowCount != 4 || files[0].MinTime <= files[1].MaxTime {
		t.Fatalf("split outputs = %+v", files)
	}
}

func TestReconcileSweepsAndRetention(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	disableRetention(t)
	files := listFiles(t, m.db)
	dayDir := filepath.Dir(archiveFilePath(testDeploymentID, files[0]))
	rowless := filepath.Join(dayDir, archiveFileName(archiveLevelShredded, files[0].MinTime, files[0].MaxTime, testNodeID, 424242))
	if err := os.WriteFile(rowless, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dayDir, provisionalFileName(5))
	if err := os.WriteFile(tmp, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archiveFilePath(testDeploymentID, files[1])); err != nil {
		t.Fatal(err)
	}
	m.reconcile(context.Background())
	for _, p := range []string{rowless, tmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s not swept: %v", p, err)
		}
	}
	if left := listFiles(t, m.db); len(left) != 1 || left[0].ID != files[0].ID {
		t.Fatalf("fileless row not removed: %+v", left)
	}
	if _, err := os.Stat(archiveFilePath(testDeploymentID, files[0])); err != nil {
		t.Fatal(err)
	}

	oldWal := filepath.Join(walDeploymentDir(testDeploymentID), "20260501_1200.wal")
	if err := os.WriteFile(oldWal, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	stubClock(t, "2026-08-01T00:00:00Z")
	retentionDays = 30
	m.reconcile(context.Background())
	if left := listFiles(t, m.db); len(left) != 0 {
		t.Fatalf("retention left rows: %+v", left)
	}
	if _, err := os.Stat(dayDir); !os.IsNotExist(err) {
		t.Fatalf("day dir not removed: %v", err)
	}
	if _, err := os.Stat(oldWal); !os.IsNotExist(err) {
		t.Fatalf("old wal not removed: %v", err)
	}
	var n int
	if err := m.db.Tx(context.Background(), func(q *logdb.Queries) error {
		rows, err := q.ListLogFileKeysInRange(context.Background(), logdb.ListLogFileKeysInRangeParams{
			DeploymentID: int64(testDeploymentID), MaxTime: 0, MinTime: 1 << 62, Keys: []string{"user"},
		})
		n = len(rows)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("key rows left after retention: %d", n)
	}
}

func TestCompletePendingSwapToleratesRewrittenFile(t *testing.T) {
	db := archiveEnv(t)
	row := logdb.LogStreamCommitMarker{Day: 20620, File: archiveFileName(archiveLevelShredded, 1, 2, testNodeID, 99)}
	if err := completePendingSwap(context.Background(), db, testDeploymentID, row); err != nil {
		t.Fatalf("missing file without a catalog row should be tolerated: %v", err)
	}
	if _, err := db.InsertLogFile(context.Background(), logdb.InsertLogFileParams{
		DeploymentID: int64(testDeploymentID), Day: 20620, Level: archiveLevelShredded, Node: int64(testNodeID), Seq: 99,
		MinTime: 1, MaxTime: 2, RowCount: 1, ByteSize: 1, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := completePendingSwap(context.Background(), db, testDeploymentID, row); err == nil {
		t.Fatal("missing file with a catalog row should error")
	}
}

func TestSweepKeepsFilesPendingUnlink(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	disableRetention(t)
	rewriteGrace = time.Hour
	inputs := listFiles(t, m.db)
	if len(inputs) != 2 {
		t.Fatalf("files = %+v", inputs)
	}
	oldTarget := rollupTargetBytes
	rollupTargetBytes = inputs[0].ByteSize + inputs[1].ByteSize
	t.Cleanup(func() { rollupTargetBytes = oldTarget })
	past := time.Now().Add(-48 * time.Hour)
	var oldPaths []string
	for _, in := range inputs {
		p := archiveFilePath(testDeploymentID, in)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
		oldPaths = append(oldPaths, p)
	}
	if did, err := m.maintenanceStep(context.Background()); err != nil || !did {
		t.Fatalf("roll-up did not run: did=%v err=%v", did, err)
	}
	for _, p := range oldPaths {
		if !m.isPendingUnlink(p) {
			t.Fatalf("%s not registered for grace unlink", p)
		}
	}
	m.reconcile(context.Background())
	for _, p := range oldPaths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("sweep removed a file inside the rewrite grace: %v", err)
		}
	}
	rewriteGrace = 0
	m.setPendingUnlink(oldPaths, false)
	m.reconcile(context.Background())
	for _, p := range oldPaths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("sweep left a rowless file past the grace: %v", err)
		}
	}
}
