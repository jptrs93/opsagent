package logmanager

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

type fakeInstanceStore struct {
	mu    sync.Mutex
	items []apigen.ScheduledInstanceState
	subs  []chan apigen.ScheduledInstanceState
}

func (f *fakeInstanceStore) FetchScheduledSnapshot(storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items)
}

func (f *fakeInstanceStore) MustFetchScheduledSnapshotAndSubscribe(storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan apigen.ScheduledInstanceState, 16)
	f.subs = append(f.subs, ch)
	return slices.Clone(f.items), ch, func() {}
}

func (f *fakeInstanceStore) set(items ...apigen.ScheduledInstanceState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = items
	for _, ch := range f.subs {
		select {
		case ch <- apigen.ScheduledInstanceState{}:
		default:
		}
	}
}

func instanceState(instanceID, deploymentID int32, status apigen.RunningStatus, systemd bool) apigen.ScheduledInstanceState {
	var st apigen.ScheduledInstanceState
	st.Instance.ID = instanceID
	st.Instance.DeploymentID = deploymentID
	st.Status.Runner.Status = status
	if systemd {
		st.Config.Spec.SystemdSpec = &apigen.SystemdSpec{}
	}
	return st
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitCollectorStopped(t *testing.T, c *LogStreamCollector) {
	t.Helper()
	waitFor(t, "collector stop", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.collectorRunning
	})
}

func archiveEnv(t *testing.T) *logdb.Queries {
	t.Helper()
	old := ainit.StaticConfig.LogArchiveDir
	ainit.StaticConfig.LogArchiveDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.LogArchiveDir = old })
	db := logdb.Open(logDBPath())
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func listFiles(t *testing.T, db *logdb.Queries) []logdb.LogFile {
	t.Helper()
	files, err := db.ListLogFilesNewestFirst(context.Background(), int64(testDeploymentID))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func archiveLines(t *testing.T, f logdb.LogFile, skip int64) []string {
	t.Helper()
	var out []string
	for row, err := range readArchiveRows(archiveFilePath(testDeploymentID, f), skip) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(row.RawMessage))
	}
	return out
}

// collectLast returns the raw lines of the newest n records, oldest first,
// matching the old StreamLastRecords contract.
func collectLast(t *testing.T, c *LogStreamCollector, n int) []string {
	t.Helper()
	resp := runWideQuery(t, c, n)
	out := make([]string, 0, len(resp.Records))
	for i := len(resp.Records) - 1; i >= 0; i-- {
		out = append(out, string(resp.Records[i].Raw))
	}
	return out
}

// runWideQuery runs an unfiltered newest-first query over all time.
func runWideQuery(t *testing.T, c *LogStreamCollector, n int) *apigen.LogQueryResponse {
	t.Helper()
	resp, err := c.runQuery(context.Background(), queryParams{
		from:        mustTime(t, "2000-01-01T00:00:00Z"),
		till:        mustTime(t, "2100-01-01T00:00:00Z"),
		limit:       n,
		newestFirst: true,
		includeRaw:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// managerQueryLines returns raw lines for a deployment via the manager query
// API, oldest first.
func managerQueryLines(t *testing.T, m *Manager, deploymentID int32) []string {
	t.Helper()
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID: deploymentID,
		TimeStart:    mustTime(t, "2000-01-01T00:00:00Z"),
		TimeEnd:      mustTime(t, "2100-01-01T00:00:00Z"),
		IncludeRaw:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(resp.Records))
	for i := len(resp.Records) - 1; i >= 0; i-- {
		out = append(out, string(resp.Records[i].Raw))
	}
	return out
}

func fillSpool(t *testing.T, c *LogStreamCollector) {
	t.Helper()
	for _, r := range drainStream(t, deadProducer(), c.deploymentID, c.liveSpool.committed) {
		c.liveSpool.Add(r)
	}
}

func TestRunCollectorOnceCommitsAndResumes(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}

	files := listFiles(t, db)
	if len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
	f := files[0]
	wantDay := mustTime(t, "2026-06-15T00:00:00Z").Unix() / daySeconds
	if f.RowCount != 2 || f.Level != 0 || f.Node != int64(testNodeID) || f.Day != wantDay {
		t.Fatalf("file row = %+v", f)
	}
	if f.MinTime != mustTime(t, "2026-06-15T14:30:01Z").UnixNano() || f.MaxTime != mustTime(t, "2026-06-15T14:31:01Z").UnixNano() {
		t.Fatalf("bounds = %d..%d", f.MinTime, f.MaxTime)
	}
	if got := archiveLines(t, f, 0); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("archive lines = %#v", got)
	}
	marker, err := db.GetLogStreamCommitMarker(context.Background(), int64(testDeploymentID))
	if err != nil {
		t.Fatal(err)
	}
	if marker.RecordTime != mustTime(t, "2026-06-15T14:31:01Z").UnixNano() || marker.File == "" {
		t.Fatalf("marker = %+v", marker)
	}

	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "gamma\n"),
	)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	files = listFiles(t, db)
	if len(files) != 2 {
		t.Fatalf("log_files rows = %d, want 2", len(files))
	}
	if got := archiveLines(t, files[0], 0); !equalStrings(got, []string{"gamma\n"}) {
		t.Fatalf("resumed archive lines = %#v", got)
	}
}

func TestRunCollectorOnceCommitsCompletedDayDuringCatchup(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "day1-a\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "day1-b\n"),
	)
	writeBucket(t, walDir, "20260616_0900",
		record(t, "2026-06-16T09:00:01Z", 1, 1, logv2.StreamStdout, "day2-a\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	files := listFiles(t, db)
	if len(files) != 2 {
		t.Fatalf("log_files rows = %d, want 2", len(files))
	}
	if files[0].RowCount != 1 || files[1].RowCount != 2 {
		t.Fatalf("row counts = %d, %d; want 1, 2", files[0].RowCount, files[1].RowCount)
	}
	if got := archiveLines(t, files[1], 0); !equalStrings(got, []string{"day1-a\n", "day1-b\n"}) {
		t.Fatalf("day1 lines = %#v", got)
	}
	if got := archiveLines(t, files[0], 0); !equalStrings(got, []string{"day2-a\n"}) {
		t.Fatalf("day2 lines = %#v", got)
	}
}

func TestCompletePendingSwapRecoversProvisionalFile(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	f := listFiles(t, db)[0]
	final := archiveFilePath(testDeploymentID, f)
	provisional := filepath.Join(filepath.Dir(final), provisionalFileName(f.Seq))
	if err := os.Rename(final, provisional); err != nil {
		t.Fatal(err)
	}

	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("final file not recovered: %v", err)
	}
	if _, err := os.Stat(provisional); !os.IsNotExist(err) {
		t.Fatalf("provisional still present: %v", err)
	}
	if files := listFiles(t, db); len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"alpha\n"}) {
		t.Fatalf("lines after recovery = %#v", got)
	}
}

func TestRunCollectorOnceRemovesOrphanTmpFiles(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)
	dayDir := archiveDayDir(testDeploymentID, int32(mustTime(t, "2026-06-15T00:00:00Z").Unix()/daySeconds))
	if err := os.MkdirAll(dayDir, 0o750); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dayDir, provisionalFileName(777))
	if err := os.WriteFile(orphan, []byte("junk"), 0o640); err != nil {
		t.Fatal(err)
	}
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan tmp still present: %v", err)
	}
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), tmpExt) {
			t.Fatalf("tmp file left behind: %s", e.Name())
		}
	}
}

func TestShredFields(t *testing.T) {
	cases := []struct {
		line      string
		wantLevel string
		wantMsg   string
	}{
		{`{"time":"2026-06-15T14:30:01Z","level":"INFO","msg":"started"}` + "\n", "INFO", "started"},
		{`{"level":"ERROR","msg":"boom"}`, "ERROR", "boom"},
		{`  {"level":"warn"}  ` + "\n", "WARN", ""},
		{`{"msg":"no level here"}`, "", "no level here"},
		{`{"level":30,"msg":"numeric level"}`, "", "numeric level"},
		{`{"level":"warning","message":"alt msg key"}`, "WARN", "alt msg key"},
		{"plain text line\n", "", "plain text line"},
		{`{"level":"INFO","broken`, "", `{"level":"INFO","broken`},
		{"", "", ""},
	}
	for _, c := range cases {
		level, msg := shredFields(apigen.RawLogLine{Line: []byte(c.line)})
		if level != c.wantLevel || msg != c.wantMsg {
			t.Fatalf("shredFields(%q) = %q, %q; want %q, %q", c.line, level, msg, c.wantLevel, c.wantMsg)
		}
	}
}

func TestCommitPopulatesShreddedColumns(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, `{"level":"INFO","msg":"up"}`+"\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStderr, "plain crash output\n"),
		record(t, "2026-06-15T14:32:01Z", 1, 1, logv2.StreamStdout, `{"level":"ERROR","msg":"down"}`+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	files := listFiles(t, db)
	if len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
	var levels, msgs []string
	for row, err := range readArchiveRows(archiveFilePath(testDeploymentID, files[0]), 0) {
		if err != nil {
			t.Fatal(err)
		}
		levels = append(levels, row.Level)
		msgs = append(msgs, row.Msg)
	}
	if want := []string{"INFO", "", "ERROR"}; !equalStrings(levels, want) {
		t.Fatalf("levels = %#v, want %#v", levels, want)
	}
	if want := []string{"up", "plain crash output", "down"}; !equalStrings(msgs, want) {
		t.Fatalf("msgs = %#v, want %#v", msgs, want)
	}
}

func TestCommitSortsRowsWithinBucket(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "beta\n"),
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:32:01Z", 1, 1, logv2.StreamStdout, "gamma\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	files := listFiles(t, db)
	if len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
	if got := archiveLines(t, files[0], 0); !equalStrings(got, []string{"alpha\n", "beta\n", "gamma\n"}) {
		t.Fatalf("archive lines = %#v, want time order", got)
	}
	f := files[0]
	if f.MinTime != mustTime(t, "2026-06-15T14:30:01Z").UnixNano() || f.MaxTime != mustTime(t, "2026-06-15T14:32:01Z").UnixNano() {
		t.Fatalf("bounds = %d..%d", f.MinTime, f.MaxTime)
	}
}

func TestCommitTriggers(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "day1-a\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	fillSpool(t, c)

	if err := c.CommitIfNeed(clock()); err != nil {
		t.Fatal(err)
	}
	if files := listFiles(t, db); len(files) != 0 {
		t.Fatalf("single stale range committed by record-loop trigger, files = %d", len(files))
	}

	writeBucket(t, walDir, "20260616_0900",
		record(t, "2026-06-16T09:00:01Z", 1, 1, logv2.StreamStdout, "day2-a\n"),
	)
	fillSpool(t, c)
	if c.liveSpool.Len() != 2 {
		t.Fatalf("ranges = %d, want 2", c.liveSpool.Len())
	}
	if err := c.CommitIfNeed(clock()); err != nil {
		t.Fatal(err)
	}
	if files := listFiles(t, db); len(files) != 1 || files[0].RowCount != 1 {
		t.Fatalf("completed day not committed, files = %+v", files)
	}
	if c.liveSpool.Len() != 1 {
		t.Fatalf("ranges after day commit = %d, want 1", c.liveSpool.Len())
	}

	if err := c.commitOnTick(clock()); err != nil {
		t.Fatal(err)
	}
	if files := listFiles(t, db); len(files) != 2 {
		t.Fatalf("tick did not commit stale single range, files = %d", len(files))
	}
	if c.liveSpool.Len() != 0 {
		t.Fatalf("ranges after tick commit = %d, want 0", c.liveSpool.Len())
	}
}

func TestStreamLastRecordsDiscardsSpoolSurplus(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "one\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "two\n"),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, "three\n"),
		record(t, "2026-06-15T14:30:04Z", 1, 1, logv2.StreamStdout, "four\n"),
		record(t, "2026-06-15T14:30:05Z", 1, 1, logv2.StreamStdout, "five\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	fillSpool(t, c)

	if got := collectLast(t, c, 3); !equalStrings(got, []string{"three\n", "four\n", "five\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	if got := collectLast(t, c, 5); !equalStrings(got, []string{"one\n", "two\n", "three\n", "four\n", "five\n"}) {
		t.Fatalf("lines = %#v", got)
	}
}

func TestStreamLastRecordsSpansParquetAndWal(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "a1\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "a2\n"),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, "a3\n"),
		record(t, "2026-06-15T14:30:04Z", 1, 1, logv2.StreamStdout, "a4\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "b1\n"),
		record(t, "2026-06-15T15:00:02Z", 1, 1, logv2.StreamStdout, "b2\n"),
	)
	fillSpool(t, c)

	if got := collectLast(t, c, 5); !equalStrings(got, []string{"a2\n", "a3\n", "a4\n", "b1\n", "b2\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	if got := collectLast(t, c, 2); !equalStrings(got, []string{"b1\n", "b2\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"a1\n", "a2\n", "a3\n", "a4\n", "b1\n", "b2\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	resp := runWideQuery(t, c, 5)
	oldest := resp.Records[len(resp.Records)-1]
	newest := resp.Records[0]
	if string(oldest.Raw) != "a2\n" || string(newest.Raw) != "b2\n" {
		t.Fatalf("first/last = %q, %q", oldest.Raw, newest.Raw)
	}
	if oldest.Version != 1 || oldest.Stream != int32(logv2.StreamStdout) {
		t.Fatalf("parquet-sourced record metadata = %+v", oldest)
	}
}

func TestStreamLastRecordsAcrossMultipleArchiveFiles(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "c1\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "c2\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "c3\n"),
		record(t, "2026-06-15T15:00:02Z", 1, 1, logv2.StreamStdout, "c4\n"),
	)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if files := listFiles(t, db); len(files) != 2 {
		t.Fatalf("log_files rows = %d, want 2", len(files))
	}

	if got := collectLast(t, c, 3); !equalStrings(got, []string{"c2\n", "c3\n", "c4\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	if got := collectLast(t, c, 4); !equalStrings(got, []string{"c1\n", "c2\n", "c3\n", "c4\n"}) {
		t.Fatalf("lines = %#v", got)
	}
}

func listWalFiles(t *testing.T, walDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), walExt) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestDeleteConsumedLogWALsAfterCommit(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "a1\n"),
		record(t, "2026-06-15T14:31:01Z", 1, 1, logv2.StreamStdout, "a2\n"),
	)
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, "b1\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}

	if left := listWalFiles(t, walDir); len(left) != 0 {
		t.Fatalf("consumed wal files not deleted: %v", left)
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"a1\n", "a2\n", "b1\n"}) {
		t.Fatalf("lines after deletion = %#v", got)
	}
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if files := listFiles(t, db); len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
}

func TestDeleteConsumedLogWALsRetainsCurrentBucket(t *testing.T) {
	streamTiming(t, time.Hour, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}

	if len(listFiles(t, db)) != 1 {
		t.Fatal("drain did not commit")
	}
	if left := listWalFiles(t, walDir); len(left) != 1 {
		t.Fatalf("current bucket must be retained, wal files = %v", left)
	}

	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("lines = %#v", got)
	}
}

func TestBootSweepDeletesLeftoverConsumedWAL(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	rec := record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n")
	writeBucket(t, walDir, "20260615_1430", rec)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if left := listWalFiles(t, walDir); len(left) != 0 {
		t.Fatalf("wal files after commit = %v", left)
	}

	writeBucket(t, walDir, "20260615_1430", rec)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	if left := listWalFiles(t, walDir); len(left) != 0 {
		t.Fatalf("boot sweep left consumed wal files: %v", left)
	}
	if files := listFiles(t, db); len(files) != 1 {
		t.Fatalf("log_files rows = %d, want 1", len(files))
	}
}

func TestDeleteConsumedLogWALsKeepsPartiallyConsumedBucket(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	oldThresh := commitSizeThresh
	commitSizeThresh = 1
	t.Cleanup(func() { commitSizeThresh = oldThresh })
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}

	files := listFiles(t, db)
	if len(files) != 2 {
		t.Fatalf("log_files rows = %d, want 2", len(files))
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("lines = %#v", got)
	}
	if left := listWalFiles(t, walDir); len(left) != 0 {
		t.Fatalf("fully consumed wal files not deleted: %v", left)
	}
}

func TestAlignCollectingDrainsOnceWithoutProducer(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	baseline := runtime.NumGoroutine()
	c := NewLogStreamCollector(testDeploymentID, db)
	c.AlignCollecting(0)

	waitFor(t, "drain commit", func() bool { return len(listFiles(t, db)) == 1 })
	waitCollectorStopped(t, c)
	waitFor(t, "collector goroutine exit", func() bool { return runtime.NumGoroutine() <= baseline })
	if got := archiveLines(t, listFiles(t, db)[0], 0); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("drained lines = %#v", got)
	}
}

func TestAlignCollectingProducerRefCount(t *testing.T) {
	streamTiming(t, time.Hour, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	c.AlignCollecting(1)
	c.AlignCollecting(1)
	waitFor(t, "first record spooled", func() bool { return c.liveSpool.Len() > 0 })

	c.AlignCollecting(-1)
	c.mu.Lock()
	count, running := c.producerCount, c.collectorRunning
	c.mu.Unlock()
	if count != 1 || !running {
		t.Fatalf("after one producer stopped: count = %d, running = %v", count, running)
	}
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	waitFor(t, "tail picked up record after one producer stopped", func() bool {
		return len(collectLast(t, c, 10)) == 2
	})

	c.AlignCollecting(-1)
	waitCollectorStopped(t, c)
	files := listFiles(t, db)
	total := int64(0)
	for _, f := range files {
		total += f.RowCount
	}
	if total != 2 {
		t.Fatalf("committed rows = %d across %d files, want 2", total, len(files))
	}
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("lines = %#v", got)
	}
}

func TestAlignCollectingRestartDuringDrain(t *testing.T) {
	streamTiming(t, time.Hour, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	c.AlignCollecting(1)
	waitFor(t, "first record spooled", func() bool { return c.liveSpool.Len() > 0 })

	c.AlignCollecting(-1)
	c.AlignCollecting(1)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	waitFor(t, "tail resumed after restart", func() bool {
		return len(collectLast(t, c, 10)) == 2
	})

	c.AlignCollecting(-1)
	waitCollectorStopped(t, c)
	if got := collectLast(t, c, 10); !equalStrings(got, []string{"alpha\n", "beta\n"}) {
		t.Fatalf("lines = %#v", got)
	}
}

func TestManagerAlignsCollectorsFromInstanceStream(t *testing.T) {
	streamTiming(t, time.Hour, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)

	fake := &fakeInstanceStore{}
	fake.set(
		instanceState(1, testDeploymentID, apigen.RunningStatus_RUNNING, false),
		instanceState(2, testDeploymentID, apigen.RunningStatus_STARTING, false),
		instanceState(3, 43, apigen.RunningStatus_RUNNING, true),
		instanceState(4, 44, apigen.RunningStatus_STOPPED, false),
	)
	ctx, cancel := context.WithCancel(context.Background())
	m := StartManager(ctx, fake, nil)
	t.Cleanup(func() {
		cancel()
		<-m.scanStopped
	})

	waitFor(t, "collector armed from snapshot", func() bool {
		c := m.collector(testDeploymentID)
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.producerCount == 2 && c.collectorRunning
	})
	if m.collector(43) != nil {
		t.Fatal("systemd instance armed a collector")
	}
	if m.collector(44) != nil {
		t.Fatal("stopped instance armed a collector")
	}

	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)
	waitFor(t, "live tail picked up record", func() bool {
		return len(managerQueryLines(t, m, testDeploymentID)) == 2
	})

	fake.set(
		instanceState(1, testDeploymentID, apigen.RunningStatus_STOPPED, false),
		instanceState(2, testDeploymentID, apigen.RunningStatus_CRASHED, false),
	)
	waitFor(t, "count dropped to one with crashed instance still producing", func() bool {
		c := m.collector(testDeploymentID)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.producerCount == 1 && c.collectorRunning
	})

	fake.set(
		instanceState(2, testDeploymentID, apigen.RunningStatus_STOPPED, false),
	)
	waitCollectorStopped(t, m.collector(testDeploymentID))
	files := listFiles(t, db)
	total := int64(0)
	for _, f := range files {
		total += f.RowCount
	}
	if total != 2 {
		t.Fatalf("committed rows = %d across %d files, want 2", total, len(files))
	}
}

func TestManagerStartupArmsRunningInstancesBeforeDirScan(t *testing.T) {
	streamTiming(t, time.Hour, time.Millisecond, time.Millisecond)
	oldScan := deploymentScanInterval
	deploymentScanInterval = time.Millisecond
	t.Cleanup(func() { deploymentScanInterval = oldScan })
	db := archiveEnv(t)
	walDir := walEnv(t)
	bucket := clock().UTC().Truncate(bucketDuration).Format(bucketLayout)
	writeBucket(t, walDir, bucket,
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
	)

	fake := &fakeInstanceStore{}
	fake.set(instanceState(1, testDeploymentID, apigen.RunningStatus_RUNNING, false))
	ctx, cancel := context.WithCancel(context.Background())
	m := StartManager(ctx, fake, nil)
	t.Cleanup(func() {
		cancel()
		<-m.scanStopped
	})

	c := m.collector(testDeploymentID)
	if c == nil {
		t.Fatal("running instance not armed synchronously at startup")
	}
	c.mu.Lock()
	count := c.producerCount
	c.mu.Unlock()
	if count != 1 {
		t.Fatalf("producer count = %d, want 1", count)
	}

	time.Sleep(20 * time.Millisecond)
	if files := listFiles(t, db); len(files) != 0 {
		t.Fatalf("dir scan committed WAL of a running deployment, files = %+v", files)
	}

	fake.set(instanceState(1, testDeploymentID, apigen.RunningStatus_STOPPED, false))
	waitCollectorStopped(t, c)
}

func TestManagerRunsCollectorsAndServesQueries(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	oldScan := deploymentScanInterval
	deploymentScanInterval = time.Millisecond
	t.Cleanup(func() { deploymentScanInterval = oldScan })
	oldArchive := ainit.StaticConfig.LogArchiveDir
	ainit.StaticConfig.LogArchiveDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.LogArchiveDir = oldArchive })

	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "alpha\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "beta\n"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	m := StartManager(ctx, &fakeInstanceStore{}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := managerQueryLines(t, m, testDeploymentID)
		if equalStrings(got, []string{"alpha\n", "beta\n"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("query never converged, last = %#v", got)
		}
		time.Sleep(time.Millisecond)
	}

	db := logdb.Open(logDBPath())
	defer db.Close()
	waitFor(t, "startup drain commit", func() bool {
		files, err := db.ListLogFilesNewestFirst(context.Background(), int64(testDeploymentID))
		if err != nil || len(files) != 1 {
			return false
		}
		c := m.collector(testDeploymentID)
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.collectorRunning
	})

	cancel()
	<-m.scanStopped
	files, err := db.ListLogFilesNewestFirst(context.Background(), int64(testDeploymentID))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].RowCount != 2 {
		t.Fatalf("files after shutdown = %+v", files)
	}
}
