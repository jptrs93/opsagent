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
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

var typedLines = []string{
	`{"level":"info","msg":"m1","user":68,"dur":1.5,"ok":true,"tags":["a","b"],"ctx":{"req":"r1","n":1}}`,
	`{"level":"info","msg":"m2","user":"68","dur":2,"ok":false}`,
	`{"level":"warn","msg":"m3","user":70,"dur":10.25}`,
	`{"level":"error","msg":"m4","user":"alice"}`,
}

func typedFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, typedLines[0]+"\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, typedLines[1]+"\n"),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, typedLines[2]+"\n"),
		record(t, "2026-06-15T14:30:04Z", 1, 1, logv2.StreamStdout, typedLines[3]+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"m5","user":68,"dur":3}`+"\n"),
	)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func spilledFixture(t *testing.T) *Manager {
	t.Helper()
	old := denseLeafBudget
	denseLeafBudget = 2
	t.Cleanup(func() { denseLeafBudget = old })
	return typedFixture(t)
}

var typedCases = []struct {
	name    string
	filters []*apigen.LogFilter
	want    []string
}{
	{"int and str eq", []*apigen.LogFilter{{Field: "user", Op: "eq", Value: "68"}}, []string{"m5", "m2", "m1"}},
	{"str eq", []*apigen.LogFilter{{Field: "user", Op: "eq", Value: "Alice"}}, []string{"m4"}},
	{"text only eq", []*apigen.LogFilter{{Field: "user", Op: "eq", Value: "68", Text: true}}, []string{"m2"}},
	{"neq", []*apigen.LogFilter{{Field: "user", Op: "neq", Value: "68"}}, []string{"m4", "m3"}},
	{"gt", []*apigen.LogFilter{{Field: "dur", Op: "gt", Value: "1.9"}}, []string{"m5", "m3", "m2"}},
	{"lte", []*apigen.LogFilter{{Field: "dur", Op: "lte", Value: "2"}}, []string{"m2", "m1"}},
	{"range on str never matches", []*apigen.LogFilter{{Field: "user", Op: "gte", Value: "0"}}, []string{"m5", "m3", "m1"}},
	{"bool eq", []*apigen.LogFilter{{Field: "ok", Op: "eq", Value: "true"}}, []string{"m1"}},
	{"nested eq", []*apigen.LogFilter{{Field: "ctx.req", Op: "eq", Value: "r1"}}, []string{"m1"}},
	{"nested int eq", []*apigen.LogFilter{{Field: "ctx.n", Op: "eq", Value: "1"}}, []string{"m1"}},
	{"array element", []*apigen.LogFilter{{Field: "tags", Op: "eq", Value: "b"}}, []string{"m1"}},
	{"exists", []*apigen.LogFilter{{Field: "ok", Op: "exists"}}, []string{"m2", "m1"}},
	{"not exists", []*apigen.LogFilter{{Field: "ok", Op: "not_exists"}}, []string{"m5", "m4", "m3"}},
	{"contains number text", []*apigen.LogFilter{{Field: "user", Op: "contains", Value: "6"}}, []string{"m5", "m2", "m1"}},
	{"in mixed", []*apigen.LogFilter{{Field: "user", Op: "in", Values: []string{"70", "alice"}}}, []string{"m4", "m3"}},
	{"absent key eq", []*apigen.LogFilter{{Field: "nope", Op: "eq", Value: "1"}}, nil},
	{"absent key neq", []*apigen.LogFilter{{Field: "nope", Op: "neq", Value: "1"}}, []string{"m5", "m4", "m3", "m2", "m1"}},
	{"combined", []*apigen.LogFilter{{Field: "user", Op: "eq", Value: "68"}, {Field: "level", Op: "eq", Value: "info"}, {Field: "dur", Op: "lt", Value: "3"}}, []string{"m2", "m1"}},
}

func runTypedCases(t *testing.T, m *Manager) {
	t.Helper()
	for _, c := range typedCases {
		req := wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Filters: c.filters, HistogramBuckets: 4})
		fast, err := m.Query(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := make([]string, 0, len(fast.Records))
		for _, r := range fast.Records {
			got = append(got, r.Msg)
		}
		if !equalStrings(got, c.want) {
			t.Fatalf("%s: msgs = %#v, want %#v", c.name, got, c.want)
		}
		forceFullScan = true
		full, err := m.Query(context.Background(), req)
		forceFullScan = false
		if err != nil {
			t.Fatalf("%s: full scan: %v", c.name, err)
		}
		fast.Stats.TookMs, full.Stats.TookMs = 0, 0
		fast.Stats.ScannedRows, full.Stats.ScannedRows = 0, 0
		if !reflect.DeepEqual(fast, full) {
			t.Fatalf("%s: two-pass = %+v\nfull = %+v", c.name, fast, full)
		}
	}
}

func TestTypedFiltersOnShreddedFiles(t *testing.T) {
	runTypedCases(t, typedFixture(t))
}

func TestTypedFiltersOnSpilledKeys(t *testing.T) {
	m := spilledFixture(t)
	files := listFiles(t, m.db)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	keys := fileKeyRows(t, m.db, files[0].ID)
	if keys["user/0"].Placement != placementDense || keys["user/3"].Placement != placementDense {
		t.Fatalf("user variants should be dense: %+v", keys)
	}
	if keys["dur/1"].Placement != placementSpill || keys["ok/2"].Placement != placementSpill || keys["ctx.req/3"].Placement != placementSpill {
		t.Fatalf("other keys should spill: %+v", keys)
	}
	runTypedCases(t, m)
}

func fileKeyRows(t *testing.T, db *logdb.Queries, fileID int64) map[string]logdb.LogFileKey {
	t.Helper()
	rows, err := db.ListLogFileKeysInRange(context.Background(), logdb.ListLogFileKeysInRangeParams{
		DeploymentID: int64(testDeploymentID), MaxTime: 0, MinTime: 1 << 62,
		Keys: []string{"user", "dur", "ok", "tags", "ctx.req", "ctx.n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]logdb.LogFileKey{}
	for _, r := range rows {
		if r.FileID == fileID {
			out[r.Key+"/"+fieldTypeSuffixOf(r.Type)] = r
		}
	}
	return out
}

func fieldTypeSuffixOf(typ int64) string {
	return []string{"0", "1", "2", "3"}[typ]
}

func TestCommitWritesCatalogKeys(t *testing.T) {
	m := typedFixture(t)
	files := listFiles(t, m.db)
	if len(files) != 1 || files[0].Level != archiveLevelShredded {
		t.Fatalf("files = %+v", files)
	}
	keys := fileKeyRows(t, m.db, files[0].ID)
	want := map[string]int64{"user/0": 2, "user/3": 2, "dur/1": 2, "dur/0": 1, "ok/2": 2, "tags/3": 1, "ctx.req/3": 1, "ctx.n/0": 1}
	for k, rows := range want {
		if got := keys[k]; got.RowCount != rows || got.Placement != placementDense {
			t.Fatalf("key %s = %+v, want %d dense rows", k, got, rows)
		}
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %+v", keys)
	}
	f, err := os.Open(archiveFilePath(testDeploymentID, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pf, err := openArchive(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"f_user__i", "f_user__s", "f_dur__f", "f_dur__i", "f_ok__b", "f_tags__s", "f_ctx.req__s", "f_ctx.n__i"} {
		if _, ok := pf.Schema().Lookup(name); !ok {
			t.Fatalf("column %s missing from %s", name, pf.Schema())
		}
	}
}

func TestRangeFilterRejectsNonNumericValue(t *testing.T) {
	m := typedFixture(t)
	_, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Filters:      []*apigen.LogFilter{{Field: "dur", Op: "gt", Value: "fast"}},
	}))
	if err == nil {
		t.Fatal("non-numeric range value did not error")
	}
}

func TestQueryRecordFieldsKeepNumberText(t *testing.T) {
	m := typedFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID,
		Filters: []*apigen.LogFilter{{Field: "dur", Op: "eq", Value: "1.5"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 1 {
		t.Fatalf("records = %+v", resp.Records)
	}
	f := resp.Records[0].Fields
	if f["dur"] != "1.5" || f["user"] != "68" || f["ok"] != "true" || f["ctx.req"] != "r1" || f["ctx.n"] != "1" || f["tags"] != `["a","b"]` {
		t.Fatalf("fields = %+v", f)
	}
	stats := fieldStatsByName(resp)
	if s := stats["ctx.req"]; s == nil || s.Top[0].Value != "r1" {
		t.Fatalf("ctx.req stats = %+v", s)
	}
}

func writeLevelZeroFile(t *testing.T, db *logdb.Queries, deploymentID int32, rows []logRow) logdb.LogFile {
	t.Helper()
	day := int32(rows[0].Time / int64(time.Second) / daySeconds)
	dayDir := archiveDayDir(deploymentID, day)
	if err := os.MkdirAll(dayDir, 0o750); err != nil {
		t.Fatal(err)
	}
	seq := newArchiveSeq()
	minT, maxT := rows[0].Time, rows[0].Time
	for _, r := range rows {
		minT, maxT = min(minT, r.Time), max(maxT, r.Time)
	}
	path := filepath.Join(dayDir, archiveFileName(archiveLevelBatch, minT, maxT, rows[0].Node, seq))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := parquet.NewGenericWriter[logRow](f, parquet.Compression(&zstd.Codec{}))
	if _, err := w.Write(rows); err != nil {
		t.Fatal(err)
	}
	w.SetKeyValueMetadata(metadataSortedKey, metadataSortedVal)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertLogFile(context.Background(), logdb.InsertLogFileParams{
		DeploymentID: int64(deploymentID), Day: int64(day), Level: archiveLevelBatch, Node: int64(rows[0].Node), Seq: seq,
		MinTime: minT, MaxTime: maxT, RowCount: int64(len(rows)), ByteSize: info.Size(), CreatedAt: clock().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return logdb.LogFile{ID: id, DeploymentID: int64(deploymentID), Day: int64(day), Level: archiveLevelBatch, Node: int64(rows[0].Node), Seq: seq,
		MinTime: minT, MaxTime: maxT, RowCount: int64(len(rows)), ByteSize: info.Size()}
}

func legacyRows(t *testing.T, day string) []logRow {
	t.Helper()
	base := mustTime(t, day+"T10:00:00Z")
	var rows []logRow
	for i, line := range typedLines {
		level, msg, _ := parseLine([]byte(line))
		rows = append(rows, logRow{
			Time: base.Add(time.Duration(i) * time.Second).UnixNano(), Version: 1, Run: 1, Node: testNodeID,
			Seq: int64(i + 1), Level: level, Msg: msg, RawMessage: []byte(line + "\n"),
		})
	}
	return rows
}

func TestFieldFiltersAcrossLevelZeroAndShredded(t *testing.T) {
	m := typedFixture(t)
	writeLevelZeroFile(t, m.db, testDeploymentID, legacyRows(t, "2026-06-14"))
	req := &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		TimeStart:    mustTime(t, "2026-06-14T00:00:00Z"),
		TimeEnd:      mustTime(t, "2026-06-16T00:00:00Z"),
		Filters:      []*apigen.LogFilter{{Field: "user", Op: "eq", Value: "68"}},
	}
	fast, err := m.Query(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fast.Records))
	for _, r := range fast.Records {
		got = append(got, r.Msg)
	}
	if !equalStrings(got, []string{"m5", "m2", "m1", "m2", "m1"}) {
		t.Fatalf("msgs = %#v", got)
	}
	forceFullScan = true
	full, err := m.Query(context.Background(), req)
	forceFullScan = false
	if err != nil {
		t.Fatal(err)
	}
	fast.Stats.TookMs, full.Stats.TookMs = 0, 0
	fast.Stats.ScannedRows, full.Stats.ScannedRows = 0, 0
	if !reflect.DeepEqual(fast, full) {
		t.Fatalf("two-pass = %+v\nfull = %+v", fast, full)
	}
}

func TestRowGroupPruningKeepsResults(t *testing.T) {
	old := denseLeafBudget
	denseLeafBudget = 400
	t.Cleanup(func() { denseLeafBudget = old })
	m := typedFixture(t)
	files := listFiles(t, m.db)
	fk := map[string][]logdb.LogFileKey{}
	for k, r := range fileKeyRows(t, m.db, files[0].ID) {
		fk[r.Key] = append(fk[r.Key], r)
		_ = k
	}
	filters := mustCompile(t, "user", "eq", "999")
	plan := planFile(files[0], filters, []int{0}, fk, &lineScanner{})
	if plan.skip || plan.raw || len(plan.bound) != 1 || len(plan.prune) != 0 {
		t.Fatalf("plan with a string variant must not prune: %+v", plan)
	}
	filters = mustCompile(t, "dur", "gt", "100")
	plan = planFile(files[0], filters, []int{0}, fk, &lineScanner{})
	if len(plan.prune) != 1 || len(plan.prune[0].needs) != 2 {
		t.Fatalf("numeric-only key should prune: %+v", plan)
	}
	rows := int64(0)
	err := scanArchiveColumns(context.Background(), archiveFilePath(testDeploymentID, files[0]), 0,
		columnNeeds{fields: plan.needs, prune: plan.prune}, func(b *cheapBatch, n int, baseRow int64, sorted bool) bool {
			rows += int64(n)
			return false
		})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("row group not pruned: scanned %d rows", rows)
	}
	filters = mustCompile(t, "dur", "gt", "5")
	plan = planFile(files[0], filters, []int{0}, fk, &lineScanner{})
	rows = 0
	err = scanArchiveColumns(context.Background(), archiveFilePath(testDeploymentID, files[0]), 0,
		columnNeeds{fields: plan.needs, prune: plan.prune}, func(b *cheapBatch, n int, baseRow int64, sorted bool) bool {
			rows += int64(n)
			return false
		})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Fatalf("matching row group pruned: scanned %d rows", rows)
	}
}
