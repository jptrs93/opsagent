package logmanager

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

func queryMsgs(t *testing.T, m *Manager, req *apigen.LogQueryRequest) []string {
	t.Helper()
	resp, err := m.Query(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(resp.Records))
	for _, r := range resp.Records {
		out = append(out, r.Msg)
	}
	return out
}

func searchFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "a1\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "a2\n"),
		record(t, "2026-06-15T14:30:03Z", 2, 1, logv2.StreamStdout, "a3\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 2, 1, logv2.StreamStdout, "b1\n"),
		record(t, "2026-06-15T15:00:02Z", 2, 1, logv2.StreamStdout, "b2\n"),
	)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

// jsonFixture commits three JSON records to parquet and leaves a JSON record
// plus a plain-text record in the WAL tail.
func jsonFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"server started","service":"api"}`+"\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"db connection failed","service":"api","err":"timeout"}`+"\n"),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, `{"level":"warn","msg":"slow query detected","service":"secondary","duration_ms":1500}`+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"request handled","service":"ingress","status":200}`+"\n"),
		record(t, "2026-06-15T15:00:02Z", 1, 1, logv2.StreamStderr, "plain panic output\n"),
	)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func wideRange(t *testing.T, req *apigen.LogQueryRequest) *apigen.LogQueryRequest {
	t.Helper()
	if req.TimeStart.IsZero() {
		req.TimeStart = mustTime(t, "2026-06-15T00:00:00Z")
	}
	if req.TimeEnd.IsZero() {
		req.TimeEnd = mustTime(t, "2026-06-16T00:00:00Z")
	}
	return req
}

func TestQueryRangeBounds(t *testing.T) {
	m := searchFixture(t)
	got := queryMsgs(t, m, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		TimeStart:    mustTime(t, "2026-06-15T14:30:02Z"),
		TimeEnd:      mustTime(t, "2026-06-15T15:00:02Z"),
	})
	if !equalStrings(got, []string{"b1", "a3", "a2"}) {
		t.Fatalf("msgs = %#v", got)
	}
}

func TestQueryLimitKeepsNewestAndReportsTruncation(t *testing.T) {
	m := searchFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Records[0].Msg != "b2" || resp.Records[1].Msg != "b1" {
		t.Fatalf("records = %#v, %#v", resp.Records[0], resp.Records[1])
	}
	if resp.Stats.MatchedRows != 5 || resp.Stats.ReturnedRows != 2 || !resp.Stats.Truncated {
		t.Fatalf("stats = %+v", resp.Stats)
	}
	if resp.Stats.ScannedRows < 5 {
		t.Fatalf("scanned = %d, want >= 5", resp.Stats.ScannedRows)
	}
}

func TestQueryOrderAscKeepsOldest(t *testing.T) {
	m := searchFixture(t)
	got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 2, Order: "asc"}))
	if !equalStrings(got, []string{"a1", "a2"}) {
		t.Fatalf("msgs = %#v", got)
	}
}

func TestQueryAggregatesOnly(t *testing.T) {
	m := searchFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: -1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 0 || resp.Stats.MatchedRows != 5 || !resp.Stats.Truncated {
		t.Fatalf("records = %d, stats = %+v", len(resp.Records), resp.Stats)
	}
}

func TestQueryConfigVersionFilter(t *testing.T) {
	m := searchFixture(t)
	got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, SpecVersion: 1}))
	if !equalStrings(got, []string{"a2", "a1"}) {
		t.Fatalf("msgs = %#v", got)
	}
}

func TestQueryIncludeRaw(t *testing.T) {
	m := jsonFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 1, IncludeRaw: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Records[0].Raw); got != "plain panic output\n" {
		t.Fatalf("raw = %q", got)
	}
	resp, err = m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records[0].Raw) != 0 {
		t.Fatalf("raw unexpectedly set: %q", resp.Records[0].Raw)
	}
}

func TestQueryFilters(t *testing.T) {
	m := jsonFixture(t)
	cases := []struct {
		name    string
		filters []*apigen.LogFilter
		want    []string
	}{
		{"level in", []*apigen.LogFilter{{Field: "level", Op: "in", Values: []string{"ERROR", "WARN"}}},
			[]string{"slow query detected", "db connection failed"}},
		{"field eq case-insensitive", []*apigen.LogFilter{{Field: "service", Op: "eq", Value: "API"}},
			[]string{"db connection failed", "server started"}},
		{"message contains", []*apigen.LogFilter{{Op: "contains", Value: "CONNECTION"}},
			[]string{"db connection failed"}},
		{"field exists", []*apigen.LogFilter{{Field: "err", Op: "exists"}},
			[]string{"db connection failed"}},
		{"field not exists", []*apigen.LogFilter{{Field: "err", Op: "not_exists"}},
			[]string{"plain panic output", "request handled", "slow query detected", "server started"}},
		{"neq includes missing field", []*apigen.LogFilter{{Field: "service", Op: "neq", Value: "api"}},
			[]string{"plain panic output", "request handled", "slow query detected"}},
		{"message not contains", []*apigen.LogFilter{{Op: "not_contains", Value: "query"}},
			[]string{"plain panic output", "request handled", "db connection failed", "server started"}},
		{"numeric field eq", []*apigen.LogFilter{{Field: "status", Op: "eq", Value: "200"}},
			[]string{"request handled"}},
	}
	for _, c := range cases {
		got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Filters: c.filters}))
		if !equalStrings(got, c.want) {
			t.Fatalf("%s: msgs = %#v, want %#v", c.name, got, c.want)
		}
	}
}

func TestQueryHistogramAndFieldNames(t *testing.T) {
	m := jsonFixture(t)
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID:     testDeploymentID,
		TimeStart:        mustTime(t, "2026-06-15T14:00:00Z"),
		TimeEnd:          mustTime(t, "2026-06-15T16:00:00Z"),
		HistogramBuckets: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Histogram
	if h == nil || h.BucketMs != 30*60*1000 {
		t.Fatalf("histogram = %+v", h)
	}
	counts := map[string][]int64{}
	for _, s := range h.Series {
		counts[s.Level] = s.Counts
	}
	check := func(level string, want []int64) {
		t.Helper()
		got := counts[level]
		if len(got) != len(want) {
			t.Fatalf("%s counts = %v, want %v", level, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s counts = %v, want %v", level, got, want)
			}
		}
	}
	check("ERROR", []int64{0, 1, 0, 0})
	check("WARN", []int64{0, 1, 0, 0})
	check("INFO", []int64{0, 1, 1, 0})
	check("DEBUG", []int64{0, 0, 0, 0})
	check("", []int64{0, 0, 1, 0})
	wantNames := []string{"duration_ms", "err", "instance", "level", "node", "run", "service", "status", "stream", "version"}
	names := make([]string, 0, len(resp.Fields))
	for _, f := range resp.Fields {
		names = append(names, f.Field)
	}
	if !equalStrings(names, wantNames) {
		t.Fatalf("field names = %#v, want %#v", names, wantNames)
	}
}

func TestSnapBucketMs(t *testing.T) {
	cases := []struct{ ideal, want int64 }{
		{1, 10},
		{10, 10},
		{11, 20},
		{999, 1000},
		{30_001, 60_000},
		{1_700_000, 1_800_000},
		{24 * 3_600_000, 24 * 3_600_000},
		{24*3_600_000 + 1, 48 * 3_600_000},
		{5 * 24 * 3_600_000, 5 * 24 * 3_600_000},
	}
	for _, c := range cases {
		if got := snapBucketMs(c.ideal); got != c.want {
			t.Fatalf("snapBucketMs(%d) = %d, want %d", c.ideal, got, c.want)
		}
	}
}

func TestQueryHistogramAlignsBucketEdges(t *testing.T) {
	m := jsonFixture(t)
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID:     testDeploymentID,
		TimeStart:        mustTime(t, "2026-06-15T13:59:30Z"),
		TimeEnd:          mustTime(t, "2026-06-15T16:00:30Z"),
		HistogramBuckets: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Histogram
	if h == nil || h.BucketMs != 3_600_000 {
		t.Fatalf("histogram = %+v", h)
	}
	if !h.StartTime.Equal(mustTime(t, "2026-06-15T13:00:00Z")) {
		t.Fatalf("start time = %v", h.StartTime)
	}
	counts := map[string][]int64{}
	for _, s := range h.Series {
		counts[s.Level] = s.Counts
	}
	want := map[string][]int64{
		"ERROR": {0, 1, 0, 0},
		"WARN":  {0, 1, 0, 0},
		"INFO":  {0, 1, 1, 0},
		"":      {0, 0, 1, 0},
	}
	for level, w := range want {
		got := counts[level]
		if len(got) != len(w) {
			t.Fatalf("%s counts = %v, want %v", level, got, w)
		}
		for i := range w {
			if got[i] != w[i] {
				t.Fatalf("%s counts = %v, want %v", level, got, w)
			}
		}
	}
}

func TestQueryValidation(t *testing.T) {
	m := searchFixture(t)
	if _, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Filters:      []*apigen.LogFilter{{Op: "regex", Value: "x"}},
	})); err == nil {
		t.Fatal("unknown op did not error")
	}
	if _, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Order:        "sideways",
	})); err == nil {
		t.Fatal("unknown order did not error")
	}
	if _, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		TimeStart:    mustTime(t, "2026-06-16T00:00:00Z"),
		TimeEnd:      mustTime(t, "2026-06-15T00:00:00Z"),
	}); err == nil {
		t.Fatal("inverted range did not error")
	}
}

func stubClock(t *testing.T, at string) {
	t.Helper()
	fixed := mustTime(t, at)
	old := clock
	clock = func() time.Time { return fixed }
	t.Cleanup(func() { clock = old })
}

func TestQueryDefaultWindowExcludesOldRecords(t *testing.T) {
	m := searchFixture(t)
	stubClock(t, "2026-06-17T00:00:00Z")
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{DeploymentID: testDeploymentID})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 0 || resp.Stats.MatchedRows != 0 {
		t.Fatalf("records = %d, matched = %d, want none inside default window", len(resp.Records), resp.Stats.MatchedRows)
	}
	if got := resp.Stats.TimeEnd; !got.Equal(mustTime(t, "2026-06-17T00:00:00Z")) {
		t.Fatalf("effective end = %v, want pinned clock", got)
	}
}

func fieldStatsByName(resp *apigen.LogQueryResponse) map[string]*apigen.LogFieldStats {
	out := map[string]*apigen.LogFieldStats{}
	for _, f := range resp.Fields {
		out[f.Field] = f
	}
	return out
}

func TestQueryFieldStats(t *testing.T) {
	m := jsonFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.SampledRows != 5 {
		t.Fatalf("sampledRows = %d, want 5", resp.Stats.SampledRows)
	}
	svc := fieldStatsByName(resp)["service"]
	if svc == nil || svc.Distinct != 3 {
		t.Fatalf("service stats = %+v", svc)
	}
	if svc.Coverage < 0.79 || svc.Coverage > 0.81 {
		t.Fatalf("coverage = %v, want 0.8", svc.Coverage)
	}
	if len(svc.Top) != 3 || svc.Top[0].Value != "api" || svc.Top[0].Count != 2 || svc.Other != 0 {
		t.Fatalf("top = %#v, other = %d", svc.Top, svc.Other)
	}
}

// arrayFixture commits records whose _tags field is a JSON array, split across
// parquet and the WAL tail so both scan paths shred the same way.
func arrayFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"sync started","_tags":["Calendar","RosterRepl"]}`+"\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"sync finished","_tags":["Calendar"]}`+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"price sync","_tags":["PriceList"]}`+"\n"),
	)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func TestQueryArrayFieldStatsCountElements(t *testing.T) {
	m := arrayFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID}))
	if err != nil {
		t.Fatal(err)
	}
	tags := fieldStatsByName(resp)["_tags"]
	if tags == nil || tags.Distinct != 3 || tags.Other != 0 {
		t.Fatalf("_tags stats = %+v", tags)
	}
	if len(tags.Top) != 3 || tags.Top[0].Value != "Calendar" || tags.Top[0].Count != 2 {
		t.Fatalf("top = %#v", tags.Top)
	}
}

func TestQueryArrayFieldEqMatchesElements(t *testing.T) {
	m := arrayFixture(t)
	got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID,
		Filters: []*apigen.LogFilter{{Field: "_tags", Op: "eq", Value: "calendar"}}}))
	if !equalStrings(got, []string{"sync finished", "sync started"}) {
		t.Fatalf("eq msgs = %#v", got)
	}
	got = queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID,
		Filters: []*apigen.LogFilter{{Field: "_tags", Op: "neq", Value: "calendar"}}}))
	if !equalStrings(got, []string{"price sync"}) {
		t.Fatalf("neq msgs = %#v", got)
	}
}

func TestQueryFieldStatsSampleBounded(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 2
	t.Cleanup(func() { fieldStatsSample = old })
	m := jsonFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.SampledRows != 2 || resp.Stats.MatchedRows != 5 {
		t.Fatalf("stats = %+v", resp.Stats)
	}
	// The sample covers only the WAL tail (newest 2 records: one JSON with
	// service=ingress/status=200, one plain line); parquet-only fields still
	// register with zeroed stats.
	stats := fieldStatsByName(resp)
	if svc := stats["service"]; svc == nil || svc.Distinct != 1 || svc.Top[0].Value != "ingress" {
		t.Fatalf("service stats = %+v", svc)
	}
	if errField := stats["err"]; errField == nil || errField.Distinct != 0 || errField.Coverage != 0 || len(errField.Top) != 0 {
		t.Fatalf("err stats = %+v", errField)
	}
}

// thinFixture commits two parquet files with mixed levels and leaves one
// record in the WAL tail, so an aggregates-only query scans both files with
// the thin time+level projection.
func thinFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1400",
		record(t, "2026-06-15T14:00:01Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e1"}`+"\n"),
		record(t, "2026-06-15T14:00:02Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i1"}`+"\n"),
		record(t, "2026-06-15T14:00:03Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i2"}`+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, `{"level":"warn","msg":"w1"}`+"\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e2"}`+"\n"),
	)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	writeBucket(t, walDir, "20260615_1500",
		record(t, "2026-06-15T15:00:01Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i3"}`+"\n"),
	)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func TestQueryThinProjectionAggregates(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 1
	t.Cleanup(func() { fieldStatsSample = old })
	m := thinFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID, Limit: -1, HistogramBuckets: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.MatchedRows != 6 || len(resp.Records) != 0 {
		t.Fatalf("stats = %+v, records = %d", resp.Stats, len(resp.Records))
	}
	counts := map[string]int64{}
	for _, s := range resp.Histogram.Series {
		for _, c := range s.Counts {
			counts[s.Level] += c
		}
	}
	if counts["ERROR"] != 2 || counts["WARN"] != 1 || counts["INFO"] != 3 || counts[""] != 0 {
		t.Fatalf("level counts = %#v", counts)
	}

	resp, err = m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID, Limit: -1,
		Filters: []*apigen.LogFilter{{Field: "level", Op: "eq", Value: "error"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.MatchedRows != 2 {
		t.Fatalf("filtered stats = %+v", resp.Stats)
	}
}

func TestTwoPassMatchesFullScan(t *testing.T) {
	cases := []struct {
		name string
		fix  func(*testing.T) *Manager
		req  func(*testing.T) *apigen.LogQueryRequest
	}{
		{"wide", jsonFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, HistogramBuckets: 6})
		}},
		{"levelEq", thinFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 2, HistogramBuckets: 4,
				Filters: []*apigen.LogFilter{{Field: "level", Op: "eq", Value: "error"}}})
		}},
		{"levelIn", jsonFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 3,
				Filters: []*apigen.LogFilter{{Field: "level", Op: "in", Values: []string{"ERROR", ""}}}})
		}},
		{"msgContains", jsonFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 5,
				Filters: []*apigen.LogFilter{{Op: "contains", Value: "query"}}})
		}},
		{"asc", searchFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 2, Order: "asc"})
		}},
		{"specVersion", searchFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, SpecVersion: 1})
		}},
		{"metaFilter", searchFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID,
				Filters: []*apigen.LogFilter{{Field: "version", Op: "eq", Value: "2"}}})
		}},
		{"streamFilter", jsonFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID,
				Filters: []*apigen.LogFilter{{Field: "stream", Op: "eq", Value: "stderr"}}})
		}},
		{"includeRaw", jsonFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 3, IncludeRaw: true})
		}},
		{"aggOnly", spoolAggFixture, func(t *testing.T) *apigen.LogQueryRequest {
			return &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: -1, HistogramBuckets: 10,
				TimeStart: mustTime(t, "2026-06-15T14:00:00Z"), TimeEnd: mustTime(t, "2026-06-15T14:20:00Z")}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.fix(t)
			fast, err := m.Query(context.Background(), c.req(t))
			if err != nil {
				t.Fatal(err)
			}
			forceFullScan = true
			t.Cleanup(func() { forceFullScan = false })
			full, err := m.Query(context.Background(), c.req(t))
			forceFullScan = false
			if err != nil {
				t.Fatal(err)
			}
			fast.Stats.TookMs, full.Stats.TookMs = 0, 0
			if !reflect.DeepEqual(fast, full) {
				t.Fatalf("two-pass = %+v\nfull = %+v", fast, full)
			}
		})
	}
}

func spoolAggFixture(t *testing.T) *Manager {
	t.Helper()
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1400",
		record(t, "2026-06-15T14:00:30Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i1"}`+"\n"),
		record(t, "2026-06-15T14:01:10Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e1"}`+"\n"),
		record(t, "2026-06-15T14:01:40Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i2"}`+"\n"),
		record(t, "2026-06-15T14:02:20Z", 1, 1, logv2.StreamStdout, `{"level":"warn","msg":"w1"}`+"\n"),
		record(t, "2026-06-15T14:03:15Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e2"}`+"\n"),
		record(t, "2026-06-15T14:05:30Z", 1, 1, logv2.StreamStdout, `{"level":"info","msg":"i3"}`+"\n"),
		record(t, "2026-06-15T14:06:10Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e4"}`+"\n"),
		record(t, "2026-06-15T14:07:20Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e5"}`+"\n"),
		record(t, "2026-06-15T14:10:05Z", 1, 1, logv2.StreamStdout, `{"level":"debug","msg":"d1"}`+"\n"),
		record(t, "2026-06-15T14:10:06Z", 1, 1, logv2.StreamStdout, `{"level":"error","msg":"e3"}`+"\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	fillSpool(t, c)
	return &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
}

func TestWalScanSkipsAggregatedRegion(t *testing.T) {
	m := spoolAggFixture(t)
	c := m.collectors[testDeploymentID]
	fromN := mustTime(t, "2026-06-15T14:00:00Z").UnixNano()
	tillN := mustTime(t, "2026-06-15T14:20:00Z").UnixNano()
	counts := make([][]int64, len(levelOrder))
	agg := &thinAgg{fromN: fromN, tillN: tillN, bucketFrom: fromN, bucketStep: 2 * int64(time.Minute), bucketN: 10, counts: counts}
	ret := &retainHeap{capacity: 1, newest: true}
	q := &queryParams{newestFirst: true}
	trace := &queryTrace{}
	if err := c.engine().scanWalTwoPass(context.Background(), StreamMarker{}, q, agg, ret, 1, trace); err != nil {
		t.Fatal(err)
	}
	if trace.walAggRows != 7 || trace.walRows != 3 {
		t.Fatalf("agg rows = %d, raw rows = %d", trace.walAggRows, trace.walRows)
	}
	if agg.scanned != 10 || agg.matched != 10 {
		t.Fatalf("scanned = %d, matched = %d", agg.scanned, agg.matched)
	}
	if counts[levelIndex("INFO")][0] != 2 || counts[levelIndex("ERROR")][0] != 1 ||
		counts[levelIndex("WARN")][1] != 1 || counts[levelIndex("ERROR")][1] != 1 ||
		counts[levelIndex("INFO")][2] != 1 || counts[levelIndex("ERROR")][3] != 2 ||
		counts[levelIndex("DEBUG")][5] != 1 || counts[levelIndex("ERROR")][5] != 1 {
		t.Fatalf("counts = %#v", counts)
	}
	retained := ret.sorted()
	if len(retained) != 1 || !bytes.Contains(retained[0].rec.Line, []byte(`"msg":"e3"`)) {
		t.Fatalf("retained = %+v", retained)
	}
}

func TestWalScanAggHeadSegment(t *testing.T) {
	m := spoolAggFixture(t)
	c := m.collectors[testDeploymentID]
	fromN := mustTime(t, "2026-06-15T14:00:30Z").UnixNano()
	tillN := mustTime(t, "2026-06-15T14:20:00Z").UnixNano()
	agg := &thinAgg{fromN: fromN, tillN: tillN}
	ret := &retainHeap{capacity: 1, newest: true}
	q := &queryParams{newestFirst: true}
	trace := &queryTrace{}
	if err := c.engine().scanWalTwoPass(context.Background(), StreamMarker{}, q, agg, ret, 1, trace); err != nil {
		t.Fatal(err)
	}
	if trace.walAggRows != 6 || trace.walRows != 4 {
		t.Fatalf("agg rows = %d, raw rows = %d", trace.walAggRows, trace.walRows)
	}
	if agg.scanned != 10 || agg.matched != 10 {
		t.Fatalf("scanned = %d, matched = %d", agg.scanned, agg.matched)
	}
	retained := ret.sorted()
	if len(retained) != 1 || retained[0].rec.Time != mustTime(t, "2026-06-15T14:10:06Z").UnixNano() {
		t.Fatalf("retained = %+v", retained)
	}
}

func TestWalScanBulkVisitsMatchMinutesOnly(t *testing.T) {
	m := spoolAggFixture(t)
	c := m.collectors[testDeploymentID]
	fromN := mustTime(t, "2026-06-15T14:00:00Z").UnixNano()
	tillN := mustTime(t, "2026-06-15T14:20:00Z").UnixNano()
	counts := make([][]int64, len(levelOrder))
	filters := mustCompile(t, "level", "eq", "warn")
	agg := &thinAgg{fromN: fromN, tillN: tillN, filters: filters, counts: counts}
	ret := &retainHeap{capacity: 5, newest: true}
	q := &queryParams{newestFirst: true, filters: filters}
	trace := &queryTrace{}
	if err := c.engine().scanWalTwoPass(context.Background(), StreamMarker{}, q, agg, ret, 5000, trace); err != nil {
		t.Fatal(err)
	}
	if trace.walAggRows != 8 || trace.walRows != 3 {
		t.Fatalf("agg rows = %d, raw rows = %d", trace.walAggRows, trace.walRows)
	}
	if agg.scanned != 10 || agg.matched != 1 {
		t.Fatalf("scanned = %d, matched = %d", agg.scanned, agg.matched)
	}
	retained := ret.sorted()
	// a level-only filter resolves just the level on the scan; msg is filled
	// in from the line when the response is built
	if len(retained) != 1 || retained[0].level != "WARN" || !bytes.Contains(retained[0].rec.Line, []byte(`"msg":"w1"`)) {
		t.Fatalf("retained = %+v", retained)
	}
}

func TestQueryWalAggMatchesFullScan(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 1
	t.Cleanup(func() { fieldStatsSample = old })
	m := spoolAggFixture(t)
	req := func() *apigen.LogQueryRequest {
		return &apigen.LogQueryRequest{
			DeploymentID:     testDeploymentID,
			TimeStart:        mustTime(t, "2026-06-15T14:00:00Z"),
			TimeEnd:          mustTime(t, "2026-06-15T14:20:00Z"),
			Limit:            -1,
			HistogramBuckets: 10,
		}
	}
	fast, err := m.Query(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	fieldStatsSample = 1 << 40
	full, err := m.Query(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if fast.Stats.MatchedRows != 10 || full.Stats.MatchedRows != 10 {
		t.Fatalf("matched = %d vs %d", fast.Stats.MatchedRows, full.Stats.MatchedRows)
	}
	if fast.Histogram == nil || full.Histogram == nil || fast.Histogram.BucketMs != 120_000 {
		t.Fatalf("histograms = %+v vs %+v", fast.Histogram, full.Histogram)
	}
	series := func(h *apigen.LogHistogram) map[string][]int64 {
		out := map[string][]int64{}
		for _, s := range h.Series {
			out[s.Level] = s.Counts
		}
		return out
	}
	if !reflect.DeepEqual(series(fast.Histogram), series(full.Histogram)) {
		t.Fatalf("series = %#v vs %#v", series(fast.Histogram), series(full.Histogram))
	}
}

func TestQueryWalAggFilteredRecords(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 1
	t.Cleanup(func() { fieldStatsSample = old })
	m := spoolAggFixture(t)
	resp, err := m.Query(context.Background(), &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		TimeStart:    mustTime(t, "2026-06-15T14:00:00Z"),
		TimeEnd:      mustTime(t, "2026-06-15T14:20:00Z"),
		Limit:        2,
		Filters:      []*apigen.LogFilter{{Field: "level", Op: "eq", Value: "error"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.MatchedRows != 5 {
		t.Fatalf("stats = %+v", resp.Stats)
	}
	got := make([]string, 0, len(resp.Records))
	for _, r := range resp.Records {
		got = append(got, r.Msg)
	}
	if !equalStrings(got, []string{"e3", "e5"}) {
		t.Fatalf("records = %v", got)
	}
}

func mustCompile(t *testing.T, field, op, value string) []compiledFilter {
	t.Helper()
	fs, err := compileFilters([]*apigen.LogFilter{{Field: field, Op: op, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestAggBinsMatchStringFilterSemantics(t *testing.T) {
	cases := []struct {
		field, op, value string
		level            string
		match            bool
	}{
		{"level", "eq", "error", "ERROR", true},
		{"level", "eq", "Error", "ERROR", true},
		{"level", "eq", "error", "WARN", false},
		{"level", "eq", "fatal", "FATAL", true},
		{"level", "eq", "error", "FATAL", false},
		{"level", "neq", "error", "FATAL", true},
		{"level", "neq", "error", "", true},
		{"level", "exists", "", "FATAL", true},
		{"level", "exists", "", "", false},
		{"level", "not_exists", "", "", true},
		{"level", "contains", "err", "ERROR", true},
		{"level", "contains", "r", "WARN", true},
		{"level", "contains", "r", "INFO", false},
	}
	for _, tc := range cases {
		a := &thinAgg{filters: mustCompile(t, tc.field, tc.op, tc.value)}
		if got := a.bin([]byte(tc.level)).match; got != tc.match {
			t.Fatalf("%s %s %q on %q: match = %v, want %v", tc.field, tc.op, tc.value, tc.level, got, tc.match)
		}
	}
	a := &thinAgg{}
	if b := a.bin([]byte("FATAL")); !b.match || b.li != levelIndex("") {
		t.Fatalf("no-filter FATAL bin = %+v", b)
	}
	if b := a.bin(nil); !b.match || b.li != levelIndex("") {
		t.Fatalf("null-level bin = %+v", b)
	}
	if b := a.bin([]byte("ERROR")); b.li != levelIndex("ERROR") {
		t.Fatalf("ERROR bin = %+v", b)
	}
	if len(a.bins) != 3 {
		t.Fatalf("bins = %d, want 3", len(a.bins))
	}
	fs, err := compileFilters([]*apigen.LogFilter{{Field: "level", Op: "in", Values: []string{"ERROR", ""}}})
	if err != nil {
		t.Fatal(err)
	}
	in := &thinAgg{filters: fs}
	if !in.bin([]byte("ERROR")).match || !in.bin(nil).match || in.bin([]byte("WARN")).match {
		t.Fatalf("in bins = %+v", in.bins)
	}
}

func TestScanArchiveColumnsDecodesLevelsAndPositions(t *testing.T) {
	m := thinFixture(t)
	files := listFiles(t, m.db)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	levels := map[string]int{}
	rows := 0
	for _, f := range files {
		filePositions := map[int64][]byte{}
		err := scanArchiveColumns(context.Background(), archiveFilePath(testDeploymentID, f), 0, columnNeeds{ints: true}, func(b *cheapBatch, n int, baseRow int64, sorted bool) bool {
			for i := 0; i < n; i++ {
				rows++
				levels[string(b.levels[i].ByteArray())]++
				filePositions[baseRow+int64(i)] = bytes.Clone(b.levels[i].ByteArray())
				if b.times[i] == 0 {
					t.Fatal("time column not decoded")
				}
			}
			return false
		})
		if err != nil {
			t.Fatal(err)
		}
		idxs := make([]int64, 0, len(filePositions))
		for idx := range filePositions {
			idxs = append(idxs, idx)
		}
		slices.Sort(idxs)
		fetched, err := fetchArchiveRows(archiveFilePath(testDeploymentID, f), idxs)
		if err != nil || len(fetched) != len(idxs) {
			t.Fatalf("fetched = %d, err = %v", len(fetched), err)
		}
		for k, idx := range idxs {
			if fetched[k].Level != string(filePositions[idx]) {
				t.Fatalf("row %d level = %q, want %q", idx, fetched[k].Level, filePositions[idx])
			}
		}
	}
	if rows != 5 || levels["ERROR"] != 2 || levels["WARN"] != 1 || levels["INFO"] != 2 {
		t.Fatalf("rows = %d, levels = %#v", rows, levels)
	}
}

func TestQueryCaptureNeverDropsRetainedRecords(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 1
	t.Cleanup(func() { fieldStatsSample = old })
	m := thinFixture(t)
	got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 2,
		Filters: []*apigen.LogFilter{{Field: "level", Op: "eq", Value: "error"}}}))
	if !equalStrings(got, []string{"e2", "e1"}) {
		t.Fatalf("msgs = %#v", got)
	}
}

func TestQueryFieldStatsTopNAndOther(t *testing.T) {
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	// 3× "hot" plus 11 distinct singles: top 10 holds hot(3) + 9 singles,
	// leaving 2 sampled occurrences in other.
	recs := [][]byte{}
	for i := 0; i < 3; i++ {
		recs = append(recs, record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, `{"msg":"x","u":"hot"}`+"\n"))
	}
	for _, v := range []string{"b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		recs = append(recs, record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, `{"msg":"x","u":"`+v+`"}`+"\n"))
	}
	writeBucket(t, walDir, "20260615_1430", recs...)
	c := NewLogStreamCollector(testDeploymentID, db)
	fillSpool(t, c)
	m := &Manager{db: c.db, collectors: map[int32]*LogStreamCollector{testDeploymentID: c}}
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID}))
	if err != nil {
		t.Fatal(err)
	}
	u := fieldStatsByName(resp)["u"]
	if u == nil || u.Distinct != 12 || len(u.Top) != 10 {
		t.Fatalf("u stats = %+v", u)
	}
	if u.Top[0].Value != "hot" || u.Top[0].Count != 3 || u.Other != 2 {
		t.Fatalf("top = %#v, other = %d", u.Top, u.Other)
	}
}
