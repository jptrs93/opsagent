package logmanager

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"net/http"
	"slices"
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

// levelOrder is the canonical series order for histograms; "" collects lines
// with no parsed level.
var levelOrder = []string{"ERROR", "WARN", "INFO", "DEBUG", ""}

func levelIndex(level string) int {
	for i, l := range levelOrder {
		if l == level {
			return i
		}
	}
	return len(levelOrder) - 1
}

type compiledFilter struct {
	field     string
	op        string
	value     string   // pre-lowercased for contains ops
	values    []string // for op = in
	origValue string
}

func compileFilters(fs []*apigen.LogFilter) ([]compiledFilter, error) {
	out := make([]compiledFilter, 0, len(fs))
	for _, f := range fs {
		if f == nil {
			continue
		}
		switch f.Op {
		case "eq", "neq", "in", "exists", "not_exists", "contains", "not_contains":
		default:
			return nil, apigen.NewApiErr(fmt.Sprintf("Unknown filter op %q", f.Op), "invalid_filter", http.StatusBadRequest)
		}
		out = append(out, compiledFilter{
			field:     f.Field,
			op:        f.Op,
			value:     strings.ToLower(f.Value),
			values:    f.Values,
			origValue: f.Value,
		})
	}
	return out, nil
}

// recFieldValue addresses one logical column of a parsed record. An empty
// field name means the message text; "level" and "msg" address the parsed
// columns; anything else is a shredded JSON field.
func recFieldValue(level, msg string, fields map[string]string, field string) (string, bool) {
	switch field {
	case "", "msg", "message":
		return msg, true
	case "level":
		return level, level != ""
	default:
		v, ok := fields[field]
		return v, ok
	}
}

// valueEquals is the equality used by eq/neq/in: the whole value, or — when
// the value is a shredded JSON array — any one of its elements.
func valueEquals(v, want string) bool {
	if strings.EqualFold(v, want) {
		return true
	}
	for _, e := range jsonArrayElements(v) {
		if strings.EqualFold(e, want) {
			return true
		}
	}
	return false
}

func (f *compiledFilter) match(level, msg string, fields map[string]string) bool {
	v, ok := recFieldValue(level, msg, fields, f.field)
	switch f.op {
	case "exists":
		return ok
	case "not_exists":
		return !ok
	case "eq":
		return ok && valueEquals(v, f.origValue)
	case "neq":
		return !(ok && valueEquals(v, f.origValue))
	case "in":
		// A missing field compares as "" so an empty want can select records
		// without the field (e.g. level in ["ERROR", ""] includes unleveled
		// lines).
		if !ok {
			v = ""
		}
		for _, want := range f.values {
			if valueEquals(v, want) {
				return true
			}
		}
		return false
	case "contains":
		return ok && strings.Contains(strings.ToLower(v), f.value)
	case "not_contains":
		return !(ok && strings.Contains(strings.ToLower(v), f.value))
	}
	return false
}

type queryParams struct {
	from, till    time.Time
	limit         int
	newestFirst   bool
	includeRaw    bool
	buckets       int
	configVersion int32
	filters       []compiledFilter
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

// scanRange streams every record of the WAL tail plus the time-pruned parquet
// files, invoking visit per record; visit returns false to stop early. Records
// outside [fromN, tillN) may still be visited (whole tail buckets, file
// boundaries) — visitors do their own range check. An unreadable archive file
// is skipped with a warning rather than failing the scan.
func (i *LogStreamCollector) scanRange(ctx context.Context, fromN, tillN int64, visit func(rec *apigen.RawLogLine) bool) ([]string, error) {
	committed, files, err := i.searchSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var warnings []string
	n := 0
	cancelled := func() bool {
		n++
		return n&1023 == 0 && ctx.Err() != nil
	}
	if committed.isZero() || tillN > committed.time-reorderGraceWindow.Nanoseconds() {
		sealed, cancel := context.WithCancel(context.Background())
		cancel()
		for r, err := range StreamDeploymentLogRecords(sealed, i.deploymentID, committed) {
			if err != nil {
				return warnings, err
			}
			if cancelled() {
				return warnings, ctx.Err()
			}
			if !visit(&r.record) {
				return warnings, nil
			}
		}
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return warnings, ctx.Err()
		}
		if f.MaxTime < fromN || f.MinTime >= tillN {
			continue
		}
		stop := false
		for row, err := range readArchiveRows(archiveFilePath(i.deploymentID, f), 0) {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("skipped unreadable archive file %s: %v", archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq), err))
				break
			}
			if cancelled() {
				return warnings, ctx.Err()
			}
			rec := rowToRawLogLine(row, i.deploymentID)
			if !visit(&rec) {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	return warnings, nil
}

type retainedRec struct {
	rec    apigen.RawLogLine
	level  string
	msg    string
	fields map[string]string
	seq    int64
}

// retainHeap keeps the newest (or oldest) capacity records seen so far. For
// newest-keep it is a min-heap on (time, seq) so the evictable record is at
// the root, and vice versa for oldest-keep.
type retainHeap struct {
	recs     []retainedRec
	capacity int
	newest   bool
}

func (h *retainHeap) Len() int { return len(h.recs) }
func (h *retainHeap) Less(a, b int) bool {
	x, y := &h.recs[a], &h.recs[b]
	lt := x.rec.Time < y.rec.Time || (x.rec.Time == y.rec.Time && x.seq < y.seq)
	if h.newest {
		return lt
	}
	return !lt
}
func (h *retainHeap) Swap(a, b int)   { h.recs[a], h.recs[b] = h.recs[b], h.recs[a] }
func (h *retainHeap) Push(x any)      { h.recs = append(h.recs, x.(retainedRec)) }
func (h *retainHeap) Pop() any        { r := h.recs[len(h.recs)-1]; h.recs = h.recs[:len(h.recs)-1]; return r }
func (h *retainHeap) evictable(r *retainedRec) bool {
	root := &h.recs[0]
	newer := r.rec.Time > root.rec.Time || (r.rec.Time == root.rec.Time && r.seq > root.seq)
	if h.newest {
		return newer
	}
	return !newer
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
		x, y := &recs[a], &recs[b]
		lt := x.rec.Time < y.rec.Time || (x.rec.Time == y.rec.Time && x.seq < y.seq)
		if h.newest {
			return !lt
		}
		return lt
	})
	return recs
}

func (i *LogStreamCollector) runQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	start := clock()
	fromN, tillN := q.from.UnixNano(), q.till.UnixNano()
	var bucketMs int64
	bucketN := 0
	if q.buckets > 0 {
		rangeMs := (tillN - fromN + 1e6 - 1) / 1e6
		bucketMs = (rangeMs + int64(q.buckets) - 1) / int64(q.buckets)
		if bucketMs < 1 {
			bucketMs = 1
		}
		bucketN = int((rangeMs + bucketMs - 1) / bucketMs)
	}
	counts := make([][]int64, len(levelOrder))
	ret := &retainHeap{capacity: q.limit, newest: q.newestFirst}
	fieldAccums := map[string]*fieldAccum{}
	var scanned, matched, sampled, seq int64
	warnings, err := i.scanRange(ctx, fromN, tillN, func(rec *apigen.RawLogLine) bool {
		scanned++
		if rec.Time < fromN || rec.Time >= tillN {
			return true
		}
		if q.configVersion > 0 && rec.Version != q.configVersion {
			return true
		}
		level, msg, fields := parseLine(rec.Line)
		for fi := range q.filters {
			if !q.filters[fi].match(level, msg, fields) {
				return true
			}
		}
		matched++
		if bucketN > 0 {
			li := levelIndex(level)
			if counts[li] == nil {
				counts[li] = make([]int64, bucketN)
			}
			bi := int((rec.Time - fromN) / (bucketMs * 1e6))
			if bi >= bucketN {
				bi = bucketN - 1
			}
			counts[li][bi]++
		}
		// Value counts come from the newest fieldStatsSample matched records
		// (the scan visits the WAL tail then parquet newest-first); the field
		// name union keeps growing over the whole range so later-only fields
		// still show up in the sidebar, with zeroed stats.
		inSample := sampled < fieldStatsSample
		if inSample {
			sampled++
		}
		if inSample || len(fieldAccums) < maxFieldNames {
			if level != "" {
				accumField(fieldAccums, "level", level, inSample)
			}
			for k, v := range fields {
				accumField(fieldAccums, k, v, inSample)
			}
		}
		seq++
		ret.offer(retainedRec{rec: *rec, level: level, msg: msg, fields: fields, seq: seq})
		return true
	})
	if err != nil {
		return nil, err
	}
	retained := ret.sorted()
	records := make([]*apigen.LogRecord, 0, len(retained))
	for idx := range retained {
		r := &retained[idx]
		out := &apigen.LogRecord{
			Time:            r.rec.Time,
			Level:           r.level,
			Msg:             r.msg,
			Fields:          r.fields,
			Version:         r.rec.Version,
			Stream:          r.rec.Stream,
			InstanceOrdinal: r.rec.InstanceOrdinal,
		}
		if q.includeRaw {
			out.Raw = bytes.Clone(r.rec.Line)
		}
		records = append(records, out)
	}
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
	if bucketN > 0 {
		h := &apigen.LogHistogram{BucketMs: bucketMs, StartTime: q.from}
		for li, level := range levelOrder {
			if level == "" && counts[li] == nil {
				continue // no unleveled lines: omit the "" series entirely
			}
			c := counts[li]
			if c == nil {
				c = make([]int64, bucketN)
			}
			h.Series = append(h.Series, &apigen.LogHistogramSeries{Level: level, Counts: c})
		}
		resp.Histogram = h
	}
	return resp, nil
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
	slices.Sort(names)
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

func (i *LogStreamCollector) searchSnapshot(ctx context.Context) (StreamMarker, []logdb.LogFile, error) {
	s := i.liveSpool
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := i.db.ListLogFilesNewestFirst(ctx, int64(i.deploymentID))
	if err != nil {
		return StreamMarker{}, nil, err
	}
	return s.committed, files, nil
}

func rowToRawLogLine(row logRow, deploymentID int32) apigen.RawLogLine {
	return apigen.RawLogLine{
		Time:            row.Time,
		Version:         row.Version,
		Run:             row.Run,
		Stream:          row.Stream,
		Line:            bytes.Clone(row.RawMessage),
		Deployment:      deploymentID,
		Node:            row.Node,
		InstanceOrdinal: row.InstanceOrdinal,
	}
}
