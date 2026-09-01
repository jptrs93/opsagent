package logmanager

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

const (
	maxHistogramBuckets = 300
	maxFieldNames       = 200
	fieldStatsTopN      = 10
)

var forceFullScan = false

// bucketLadderMs is the set of allowed histogram bucket widths; the requested
// bucket count only picks a target, the actual width snaps up to the next rung
// and bucket edges align to multiples of it so edges stay put as the query
// window slides.
var bucketLadderMs = []int64{
	10, 20, 50, 100, 200, 500,
	1_000, 2_000, 5_000, 10_000, 30_000,
	60_000, 120_000, 300_000, 600_000, 1_800_000,
	3_600_000, 3 * 3_600_000, 6 * 3_600_000, 12 * 3_600_000, 24 * 3_600_000,
}

func snapBucketMs(ideal int64) int64 {
	for _, w := range bucketLadderMs {
		if w >= ideal {
			return w
		}
	}
	const day = 24 * 3_600_000
	return (ideal + day - 1) / day * day
}

type queryEngine struct {
	deploymentID int32
	db           *logdb.Queries
	spool        *LiveSegmentSpool
}

func (i *LogStreamCollector) engine() *queryEngine {
	return &queryEngine{deploymentID: i.deploymentID, db: i.db, spool: i.liveSpool}
}

func (i *LogStreamCollector) runQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	return i.engine().runQuery(ctx, q)
}

type queryParams struct {
	from, till  time.Time
	limit       int
	newestFirst bool
	includeRaw  bool
	buckets     int
	specVersion int32
	filters     []compiledFilter
}

func resolveQueryScope(timeStart, timeEnd time.Time) (from, till time.Time, err error) {
	till = timeEnd
	if till.IsZero() {
		till = clock()
	}
	from = timeStart
	if from.IsZero() {
		from = till.Add(-defaultSearchWindow)
	}
	if !from.Before(till) {
		return from, till, apigen.NewApiErr("Invalid time range", "invalid_time_range", http.StatusBadRequest)
	}
	return from, till, nil
}

func (e *queryEngine) snapshot(ctx context.Context) (StreamMarker, []logdb.LogFile, error) {
	s := e.spool
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := e.db.ListLogFilesNewestFirst(ctx, int64(e.deploymentID))
	if err != nil {
		return StreamMarker{}, nil, err
	}
	return s.committed, files, nil
}

func (e *queryEngine) runQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	if forceFullScan || !filtersColumnSafe(q.filters) {
		return e.runFullQuery(ctx, q)
	}
	return e.runTwoPassQuery(ctx, q)
}

type bucketPlan struct {
	fromN int64
	stepN int64
	ms    int64
	n     int
}

func planBuckets(buckets int, fromN, tillN int64) bucketPlan {
	if buckets <= 0 {
		return bucketPlan{fromN: fromN}
	}
	rangeMs := (tillN - fromN + 1e6 - 1) / 1e6
	ms := snapBucketMs((rangeMs + int64(buckets) - 1) / int64(buckets))
	step := ms * 1e6
	from := fromN - fromN%step
	return bucketPlan{fromN: from, stepN: step, ms: ms, n: int((tillN - from + step - 1) / step)}
}

func buildHistogram(b bucketPlan, counts [][]int64) *apigen.LogHistogram {
	h := &apigen.LogHistogram{BucketMs: b.ms, StartTime: time.Unix(0, b.fromN).UTC()}
	for li, level := range levelOrder {
		if level == "" && counts[li] == nil {
			continue // no unleveled lines: omit the "" series entirely
		}
		c := counts[li]
		if c == nil {
			c = make([]int64, b.n)
		}
		h.Series = append(h.Series, &apigen.LogHistogramSeries{Level: level, Counts: c})
	}
	return h
}

type fileScan struct {
	name string
	rows int64
	mode string
	dur  time.Duration
}

type queryTrace struct {
	snapshotDur time.Duration
	walDur      time.Duration
	walRows     int64
	walAggRows  int64
	fetchRows   int64
	fetchDur    time.Duration
	filesPruned int
	files       []fileScan
}

func (t *queryTrace) summary(took time.Duration, scanned int64) string {
	const maxEntries = 24
	var b strings.Builder
	fmt.Fprintf(&b, "log query took %v, scanned %d rows, %d files:", took.Round(time.Millisecond), scanned, len(t.files))
	for i := range t.files {
		if i == maxEntries {
			fmt.Fprintf(&b, "\n+%d more files", len(t.files)-i)
			break
		}
		f := &t.files[i]
		fmt.Fprintf(&b, "\n%s: %d rows, %v, %s", f.name, f.rows, f.dur.Round(time.Millisecond), f.mode)
	}
	if t.walAggRows > 0 {
		fmt.Fprintf(&b, "\nlive wal: %d rows, %d agg rows, %v", t.walRows, t.walAggRows, t.walDur.Round(time.Millisecond))
	} else {
		fmt.Fprintf(&b, "\nlive wal: %d rows, %v", t.walRows, t.walDur.Round(time.Millisecond))
	}
	if t.fetchRows > 0 {
		fmt.Fprintf(&b, "\nfetch: %d rows, %v", t.fetchRows, t.fetchDur.Round(time.Millisecond))
	}
	return b.String()
}

type retainedRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   map[string]string
	shredded bool
	pending  bool
	fileIdx  int
	rowIdx   int64
}

// retainHeap keeps the newest (or oldest) capacity records seen so far. For
// newest-keep it is a min-heap on the record key so the evictable record is at
// the root, and vice versa for oldest-keep.
type retainHeap struct {
	recs     []retainedRec
	capacity int
	newest   bool
}

func (h *retainHeap) Len() int { return len(h.recs) }
func (h *retainHeap) Less(a, b int) bool {
	c := cmpRecordKey(&h.recs[a].rec, &h.recs[b].rec)
	if h.newest {
		return c < 0
	}
	return c > 0
}
func (h *retainHeap) Swap(a, b int) { h.recs[a], h.recs[b] = h.recs[b], h.recs[a] }
func (h *retainHeap) Push(x any)    { h.recs = append(h.recs, x.(retainedRec)) }
func (h *retainHeap) Pop() any      { r := h.recs[len(h.recs)-1]; h.recs = h.recs[:len(h.recs)-1]; return r }
func (h *retainHeap) evictable(r *retainedRec) bool {
	c := cmpRecordKey(&r.rec, &h.recs[0].rec)
	if h.newest {
		return c > 0
	}
	return c < 0
}

func (h *retainHeap) offer(r retainedRec) {
	if h.capacity <= 0 {
		return
	}
	if len(h.recs) < h.capacity {
		heap.Push(h, r)
		return
	}
	if h.evictable(&r) {
		h.recs[0] = r
		heap.Fix(h, 0)
	}
}

// sorted drains the heap into display order: newest-first when newest is set,
// oldest-first otherwise.
func (h *retainHeap) sorted() []retainedRec {
	recs := h.recs
	h.recs = nil
	sort.Slice(recs, func(a, b int) bool {
		c := cmpRecordKey(&recs[a].rec, &recs[b].rec)
		if h.newest {
			return c > 0
		}
		return c < 0
	})
	return recs
}

func queryResponse(q *queryParams, start time.Time, scanned, matched, sampled int64, records []*apigen.LogRecord, fieldAccums map[string]*fieldAccum, warnings []string, b bucketPlan, counts [][]int64) *apigen.LogQueryResponse {
	resp := &apigen.LogQueryResponse{
		Stats: &apigen.LogQueryStats{
			TimeStart:    q.from,
			TimeEnd:      q.till,
			ScannedRows:  scanned,
			MatchedRows:  matched,
			ReturnedRows: int32(len(records)),
			Truncated:    matched > int64(len(records)),
			TookMs:       int32(clock().Sub(start).Milliseconds()),
			SampledRows:  sampled,
		},
		Fields:   fieldStatsList(fieldAccums, sampled),
		Records:  records,
		Warnings: warnings,
	}
	if b.n > 0 {
		resp.Histogram = buildHistogram(b, counts)
	}
	return resp
}

// fieldAccum tallies one parsed field over the sampled matched records. A
// field first seen outside the sample still registers (so it lists in the
// sidebar) but keeps zeroed counts.
type fieldAccum struct {
	withField int64
	values    map[string]int64
}

func accumField(m map[string]*fieldAccum, name, value string, inSample bool) {
	acc := m[name]
	if acc == nil {
		if len(m) >= maxFieldNames {
			return
		}
		acc = &fieldAccum{values: map[string]int64{}}
		m[name] = acc
	}
	if !inSample {
		return
	}
	acc.withField++
	// A shredded JSON array counts per element, so multi-valued fields like
	// _tags list each item with its own share instead of one row per distinct
	// combination.
	if elems := jsonArrayElements(value); elems != nil {
		for _, e := range elems {
			acc.values[e]++
		}
		return
	}
	acc.values[value]++
}

func fieldStatsList(m map[string]*fieldAccum, sampled int64) []*apigen.LogFieldStats {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	type vc struct {
		value string
		count int64
	}
	out := make([]*apigen.LogFieldStats, 0, len(names))
	for _, name := range names {
		acc := m[name]
		fs := &apigen.LogFieldStats{Field: name, Distinct: int64(len(acc.values))}
		if sampled > 0 {
			fs.Coverage = float64(acc.withField) / float64(sampled)
		}
		all := make([]vc, 0, len(acc.values))
		for v, c := range acc.values {
			all = append(all, vc{v, c})
		}
		sort.Slice(all, func(a, b int) bool {
			if all[a].count != all[b].count {
				return all[a].count > all[b].count
			}
			return all[a].value < all[b].value
		})
		// Other is the value occurrences outside the top-N. Summing the counts
		// (rather than starting from withField) keeps it correct for
		// multi-valued fields, where one record contributes several tallies.
		var other int64
		for _, v := range all {
			other += v.count
		}
		for idx := 0; idx < len(all) && idx < fieldStatsTopN; idx++ {
			fs.Top = append(fs.Top, &apigen.LogFieldValueCount{Value: all[idx].value, Count: all[idx].count})
			other -= all[idx].count
		}
		fs.Other = other
		out = append(out, fs)
	}
	return out
}

func rowToRawLogLine(row logRow, deploymentID int32) apigen.RawLogLine {
	return apigen.RawLogLine{
		Time:            row.Time,
		Version:         row.Version,
		Run:             row.Run,
		Stream:          row.Stream,
		Seq:             row.Seq,
		Line:            bytes.Clone(row.RawMessage),
		Deployment:      deploymentID,
		Node:            row.Node,
		InstanceOrdinal: row.InstanceOrdinal,
	}
}
