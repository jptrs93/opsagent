package logmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func oneBatchFixture(t *testing.T) (*Manager, func(bucket, at string)) {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	c := NewLogStreamCollector(testDeploymentID, db)
	commit := func(bucket, at string) {
		t.Helper()
		writeBucket(t, walDir, bucket, record(t, at, 1, 1, logv2.StreamStdout, typedLines[0]+"\n"))
		if err := c.RunCollectorOnce(deadProducer()); err != nil {
			t.Fatal(err)
		}
	}
	commit("20260615_1400", "2026-06-15T14:00:01Z")
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}, commit
}

func sealed(t *testing.T, m *Manager) []SealedFile {
	t.Helper()
	files, err := m.SealedFiles(context.Background(), testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSealedFilesListsARolledUpFileWithTheHashOfItsBytes(t *testing.T) {
	m := maintenanceEnv(t, twoBatchFixture(t))
	stubClock(t, "2026-06-16T00:20:00Z")
	if !step(t, m) {
		t.Fatal("day-end roll-up did not run")
	}
	if got := sealed(t, m); len(got) != 0 {
		t.Fatalf("a file was listed before it was sealed: %+v", got)
	}
	if !step(t, m) {
		t.Fatal("the rolled-up file was not sealed")
	}
	got := sealed(t, m)
	if len(got) != 1 {
		t.Fatalf("listed %d sealed files, want 1", len(got))
	}
	row := listFiles(t, m.db)[0]
	b, err := os.ReadFile(got[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if !bytes.Equal(sum[:], got[0].Sha256) || got[0].SizeBytes != int64(len(b)) {
		t.Fatal("the listing describes other bytes than the file holds")
	}
	if got[0].DeploymentID != testDeploymentID || got[0].Level != archiveLevelRollup || got[0].Seq != row.Seq || got[0].Rows != 4 ||
		got[0].Day != uint32(row.Day) || got[0].MinTime != row.MinTime || got[0].MaxTime != row.MaxTime {
		t.Fatalf("listed %+v for catalog row %+v", got[0], row)
	}
	if other, _ := m.SealedFiles(context.Background(), testNodeID+1); len(other) != 0 {
		t.Fatalf("another node's listing holds %d files", len(other))
	}
}

func TestALoneLevelOneFileIsSealedOnceItsDayIsOver(t *testing.T) {
	fixture, commit := oneBatchFixture(t)
	m := maintenanceEnv(t, fixture)
	for _, at := range []string{"2026-06-15T23:00:00Z", "2026-06-16T00:20:00Z"} {
		stubClock(t, at)
		if step(t, m) {
			t.Fatalf("the open day's only file was sealed at %s", at)
		}
	}
	stubClock(t, "2026-06-16T00:31:00Z")
	if !step(t, m) {
		t.Fatal("the lone file of a finished day was not sealed")
	}
	first := sealed(t, m)
	if len(first) != 1 || first[0].Level != archiveLevelShredded {
		t.Fatalf("sealed %+v", first)
	}
	if step(t, m) {
		t.Fatal("nothing is left to seal")
	}

	commit("20260615_1430", "2026-06-15T14:30:01Z")
	if !step(t, m) {
		t.Fatal("a file that arrived after its day was sealed was not sealed itself")
	}
	files := listFiles(t, m.db)
	if len(files) != 2 {
		t.Fatalf("a sealed file was rolled up: %+v", files)
	}
	for _, f := range files {
		if f.Level != archiveLevelShredded || f.Sha256 == "" {
			t.Fatalf("both files stay level 1 and sealed, got %+v", f)
		}
	}
	if got := sealed(t, m); len(got) != 2 || !bytes.Equal(got[0].Sha256, first[0].Sha256) && !bytes.Equal(got[1].Sha256, first[0].Sha256) {
		t.Fatalf("the first sealed file changed: %+v", got)
	}
	if step(t, m) {
		t.Fatal("two sealed level 1 files are not a roll-up")
	}
}

func TestSealingSkipsAFileItCannotHashUntilTheNextReconcile(t *testing.T) {
	fixture, _ := oneBatchFixture(t)
	m := maintenanceEnv(t, fixture)
	disableRetention(t)
	stubClock(t, "2026-06-16T01:00:00Z")
	row := listFiles(t, m.db)[0]
	path := archiveFilePath(testDeploymentID, row)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)-1], 0o640); err != nil {
		t.Fatal(err)
	}
	if !step(t, m) {
		t.Fatal("the step did not try the file")
	}
	if step(t, m) {
		t.Fatal("the step tried a file it had just failed on")
	}
	if got := sealed(t, m); len(got) != 0 {
		t.Fatalf("a file whose size does not match its row was sealed: %+v", got)
	}
	if err := os.WriteFile(path, b, 0o640); err != nil {
		t.Fatal(err)
	}
	m.reconcile(context.Background())
	if !step(t, m) || len(sealed(t, m)) != 1 {
		t.Fatal("the file was not sealed after the reconcile cleared the skip")
	}
}

func TestTheSystemLogIsNeverSealed(t *testing.T) {
	db := archiveEnv(t)
	m := maintenanceEnv(t, &Manager{db: db, collectors: map[int32]*LogStreamCollector{}})
	if _, err := db.InsertLogFile(context.Background(), logdb.InsertLogFileParams{
		DeploymentID: 0, Day: 20_000, Level: archiveLevelRollup, Node: int64(testNodeID), Seq: 9, MinTime: 1, MaxTime: 2, RowCount: 1, ByteSize: 1, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	stubClock(t, "2026-06-16T01:00:00Z")
	if step(t, m) {
		t.Fatal("a file of deployment 0 was taken for sealing")
	}
}

func TestOpeningAnOlderCatalogAddsTheHashColumn(t *testing.T) {
	db := archiveEnv(t)
	ctx := context.Background()
	if err := db.Tx(ctx, func(q *logdb.Queries) error {
		_, err := q.InsertLogFile(ctx, logdb.InsertLogFileParams{DeploymentID: 1, Day: 1, Level: 1, Node: 1, Seq: 1, MinTime: 1, MaxTime: 2, RowCount: 1, ByteSize: 1, CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw := sqlitedb.MustOpen(logDBPath())
	if _, err := raw.Exec(`ALTER TABLE log_files DROP COLUMN sha256`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	again := logdb.Open(logDBPath())
	defer again.Close()
	rows, err := again.ListLogFilesNewestFirst(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Sha256 != "" {
		t.Fatalf("the row of the older catalog reads %+v", rows)
	}
}
