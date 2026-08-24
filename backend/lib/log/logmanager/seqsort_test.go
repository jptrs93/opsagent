package logmanager

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
	"github.com/parquet-go/parquet-go"
)

func legacyRecord(t *testing.T, at string, m logv2.RecordMeta, line string) []byte {
	t.Helper()
	payloadLen := logv2.RecordLegacyPayloadHeaderLen + len(line)
	rec := make([]byte, logv2.RecordOverheadLen+payloadLen)
	rec[0] = logv2.RecordMagicLegacy
	binary.BigEndian.PutUint32(rec[logv2.RecordMagicLen:logv2.RecordHeaderLen], uint32(payloadLen))
	p := rec[logv2.RecordHeaderLen : logv2.RecordHeaderLen+payloadLen]
	binary.BigEndian.PutUint64(p[0:], uint64(mustTime(t, at).UnixNano()))
	binary.BigEndian.PutUint32(p[8:], uint32(m.Version))
	binary.BigEndian.PutUint32(p[12:], uint32(m.Run))
	binary.BigEndian.PutUint32(p[16:], uint32(m.Deployment))
	binary.BigEndian.PutUint32(p[20:], uint32(m.Node))
	binary.BigEndian.PutUint32(p[24:], uint32(m.InstanceOrdinal))
	p[28] = byte(m.Stream)
	copy(p[logv2.RecordLegacyPayloadHeaderLen:], line)
	crcAt := logv2.RecordHeaderLen + payloadLen
	binary.BigEndian.PutUint32(rec[crcAt:], logv2.PayloadCRC(p))
	binary.BigEndian.PutUint32(rec[crcAt+logv2.RecordCRCLen:], uint32(payloadLen))
	return rec
}

func TestParseWalRecordBothFormats(t *testing.T) {
	meta := logv2.RecordMeta{Version: 3, Run: 2, Deployment: testDeploymentID, Node: testNodeID, InstanceOrdinal: 1, Stream: logv2.StreamStderr}
	leg := legacyRecord(t, "2026-06-15T14:30:00Z", meta, "old\n")
	cur := logv2.EncodeRecord(mustTime(t, "2026-06-15T14:30:01Z"), meta, 7, []byte("now\n"))
	buf := append(append([]byte{0x01}, leg...), cur...)
	if i := nextMagicIndex(buf); i != 1 {
		t.Fatalf("nextMagicIndex = %d", i)
	}
	rec, size, status := parseWalRecord(buf[1:])
	if status != parseOK || rec.Seq != 0 || string(rec.Line) != "old\n" || rec.Node != testNodeID || rec.Version != 3 {
		t.Fatalf("legacy = %+v status %d", rec, status)
	}
	rec2, _, status := parseWalRecord(buf[1+size:])
	if status != parseOK || rec2.Seq != 7 || string(rec2.Line) != "now\n" || rec2.InstanceOrdinal != 1 || rec2.Stream != 1 {
		t.Fatalf("current = %+v status %d", rec2, status)
	}
}

func TestAppenderSeqMonotonic(t *testing.T) {
	dir := t.TempDir()
	a, err := logv2.NewAppender(dir, logv2.RecordMeta{Version: 1, Run: 1, Deployment: 1, Stream: logv2.StreamStdout})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	a.Append(at, []byte("one\n"))
	a.Append(at, []byte("two\n"))
	a.Append(at, []byte("three\n"))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v err = %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for len(data) > 0 {
		rec, size, status := parseWalRecord(data)
		if status != parseOK {
			t.Fatalf("status = %d", status)
		}
		seqs = append(seqs, rec.Seq)
		data = data[size:]
	}
	if len(seqs) != 3 || seqs[0] != 0 || seqs[1] != 1 || seqs[2] != 2 {
		t.Fatalf("seqs = %v", seqs)
	}
}

func TestCommitResortsUnsortedChunk(t *testing.T) {
	prev := sortBufBytesThresh
	sortBufBytesThresh = 10
	t.Cleanup(func() { sortBufBytesThresh = prev })
	streamTiming(t, time.Millisecond, time.Millisecond, time.Millisecond)
	db := archiveEnv(t)
	walDir := walEnv(t)
	writeBucket(t, walDir, "20260615_1430",
		record(t, "2026-06-15T14:30:04Z", 1, 1, logv2.StreamStdout, "d\n"),
		record(t, "2026-06-15T14:30:03Z", 1, 1, logv2.StreamStdout, "c\n"),
		record(t, "2026-06-15T14:30:02Z", 1, 1, logv2.StreamStdout, "b\n"),
		record(t, "2026-06-15T14:30:01Z", 1, 1, logv2.StreamStdout, "a\n"),
	)
	c := NewLogStreamCollector(testDeploymentID, db)
	if err := c.RunCollectorOnce(deadProducer()); err != nil {
		t.Fatal(err)
	}
	files := listFiles(t, db)
	if len(files) != 1 || files[0].RowCount != 4 {
		t.Fatalf("files = %+v", files)
	}
	path := archiveFilePath(testDeploymentID, files[0])
	var rows []logRow
	for row, err := range readArchiveRows(path, 0) {
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if cmpLogRowKey(&rows[i-1], &rows[i]) >= 0 {
			t.Fatalf("rows out of order at %d: %+v then %+v", i, rows[i-1], rows[i])
		}
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := pf.Lookup(metadataSortedKey); v != metadataSortedVal {
		t.Fatalf("sorted metadata = %q", v)
	}
	if v, _ := pf.Lookup("deployment"); v != "42" {
		t.Fatalf("deployment metadata = %q", v)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), tmpExt) {
			t.Fatalf("leftover tmp file %s", e.Name())
		}
	}
}

func TestReadArchiveRowsRangeEarlyBreak(t *testing.T) {
	m := searchFixture(t)
	files := listFiles(t, m.db)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	path := archiveFilePath(testDeploymentID, files[0])
	till := mustTime(t, "2026-06-15T14:30:03Z").UnixNano()
	var msgs []string
	for row, err := range readArchiveRowsRange(path, 0, till, func(r *logRow) int64 { return r.Time }) {
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, strings.TrimSpace(string(row.RawMessage)))
	}
	if !equalStrings(msgs, []string{"a1", "a2"}) {
		t.Fatalf("msgs = %#v", msgs)
	}
}

func TestQueryMetaFieldFiltersAndStats(t *testing.T) {
	m := jsonFixture(t)
	got := queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Filters:      []*apigen.LogFilter{{Field: "stream", Op: "eq", Value: "stderr"}},
	}))
	if !equalStrings(got, []string{"plain panic output"}) {
		t.Fatalf("msgs = %#v", got)
	}
	got = queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Filters: []*apigen.LogFilter{
			{Field: "node", Op: "eq", Value: "7"},
			{Field: "version", Op: "eq", Value: "1"},
			{Field: "run", Op: "eq", Value: "1"},
			{Field: "instance", Op: "eq", Value: "0"},
		},
	}))
	if len(got) != 5 {
		t.Fatalf("msgs = %#v", got)
	}
	got = queryMsgs(t, m, wideRange(t, &apigen.LogQueryRequest{
		DeploymentID: testDeploymentID,
		Filters:      []*apigen.LogFilter{{Field: "node", Op: "eq", Value: "99"}},
	}))
	if len(got) != 0 {
		t.Fatalf("msgs = %#v", got)
	}
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID}))
	if err != nil {
		t.Fatal(err)
	}
	stats := fieldStatsByName(resp)
	if s := stats["stream"]; s == nil || s.Coverage != 1 || s.Distinct != 2 {
		t.Fatalf("stream stats = %+v", s)
	}
	if s := stats["node"]; s == nil || len(s.Top) != 1 || s.Top[0].Value != "7" {
		t.Fatalf("node stats = %+v", s)
	}
	if s := stats["run"]; s == nil || len(s.Top) != 1 || s.Top[0].Value != "1" {
		t.Fatalf("run stats = %+v", s)
	}
	if s := stats["instance"]; s == nil || len(s.Top) != 1 || s.Top[0].Value != "0" {
		t.Fatalf("instance stats = %+v", s)
	}
	if s := stats["version"]; s == nil || len(s.Top) != 1 || s.Top[0].Value != "1" {
		t.Fatalf("version stats = %+v", s)
	}
	for _, r := range resp.Records {
		if r.Node != testNodeID || r.Run != 1 {
			t.Fatalf("record meta = %+v", r)
		}
	}
}

func TestQueryNarrowFilesMatchFull(t *testing.T) {
	old := fieldStatsSample
	fieldStatsSample = 1
	t.Cleanup(func() { fieldStatsSample = old })
	m := searchFixture(t)
	resp, err := m.Query(context.Background(), wideRange(t, &apigen.LogQueryRequest{DeploymentID: testDeploymentID, Limit: 1, HistogramBuckets: 4}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.MatchedRows != 5 || resp.Stats.SampledRows != 1 {
		t.Fatalf("stats = %+v", resp.Stats)
	}
	if len(resp.Records) != 1 || resp.Records[0].Msg != "b2" {
		t.Fatalf("records = %+v", resp.Records)
	}
	var total int64
	for _, s := range resp.Histogram.Series {
		for _, c := range s.Counts {
			total += c
		}
	}
	if total != 5 {
		t.Fatalf("histogram total = %d", total)
	}
}
