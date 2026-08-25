package logmanager

import (
	"bytes"
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
	"github.com/parquet-go/parquet-go"
)

const (
	maxHistogramBuckets = 300
	maxFieldNames       = 200
	fieldStatsTopN      = 10
)

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

func isMetaFieldName(field string) bool {
	switch field {
	case "version", "node", "run", "instance", "stream":
		return true
	}
	return false
}

func streamName(stream int32) string {
	switch stream {
	case 0:
		return "stdout"
	case 1:
		return "stderr"
	default:
		return strconv.Itoa(int(stream))
	}
}

func filtersNarrowSafe(fs []compiledFilter) bool {
	for i := range fs {
		switch fs[i].field {
		case "", "msg", "message", "level":
		default:
			if !isMetaFieldName(fs[i].field) {
				return false
			}
		}
	}
	return true
}

func filtersThinSafe(fs []compiledFilter) bool {
	for i := range fs {
		if fs[i].field != "level" {
			return false
		}
	}
	return true
}

type projection int

const (
	projFull projection = iota
	projNarrow
	projThin
	projAgg
)

func (p projection) String() string {
	switch p {
	case projNarrow:
		return "narrow"
	case projThin:
		return "thin"
	case projAgg:
		return "agg"
	}
	return "full"
}

// thinAgg accumulates histogram and match counts straight from the time and
// level columns, bypassing row visiting entirely. Each distinct level string
// gets one lazily built bin holding its histogram series index and its
// verdict from the real compiled-filter match code, so the fast path can
// never disagree with the row-visiting path. counts shares runQuery's
// per-level series slice; series stay nil until a row lands in them so the
// response keeps omitting empty series.
type thinAgg struct {
	fromN, tillN int64
	bucketFrom   int64
	bucketStep   int64
	bucketN      int
	filters      []compiledFilter
	counts       [][]int64
	bins         []levelBin
	scanned      int64
	matched      int64
}

type levelBin struct {
	val   []byte
	li    int
	match bool
}

func (a *thinAgg) bin(val []byte) *levelBin {
	for i := range a.bins {
		if bytes.Equal(a.bins[i].val, val) {
			return &a.bins[i]
		}
	}
	level := string(val)
	v := visitRec{level: level, shredded: true, parsed: true, narrow: true}
	match := true
	for i := range a.filters {
		if !a.filters[i].match(&v) {
			match = false
			break
		}
	}
	a.bins = append(a.bins, levelBin{val: []byte(level), li: levelIndex(level), match: match})
	return &a.bins[len(a.bins)-1]
}

func (a *thinAgg) consume(times []int64, levels []parquet.Value, sorted bool) bool {
	for i, t := range times {
		if sorted && t >= a.tillN {
			return true
		}
		a.scanned++
		if t < a.fromN || t >= a.tillN {
			continue
		}
		b := a.bin(levels[i].ByteArray())
		if !b.match {
			continue
		}
		a.matched++
		if a.bucketN > 0 {
			c := a.counts[b.li]
			if c == nil {
				c = make([]int64, a.bucketN)
				a.counts[b.li] = c
			}
			bi := int((t - a.bucketFrom) / a.bucketStep)
			if bi >= a.bucketN {
				bi = a.bucketN - 1
			}
			c[bi]++
		}
	}
	return false
}

func (a *thinAgg) consumeBulk(minuteStartN int64, level string, n int64) {
	b := a.bin([]byte(level))
	if !b.match {
		return
	}
	a.matched += n
	if a.bucketN > 0 {
		c := a.counts[b.li]
		if c == nil {
			c = make([]int64, a.bucketN)
			a.counts[b.li] = c
		}
		bi := int((minuteStartN - a.bucketFrom) / a.bucketStep)
		if bi >= a.bucketN {
			bi = a.bucketN - 1
		}
		c[bi] += n
	}
}

type walSkipPlan struct {
	aggLoN     int64
	aggHiEndN  int64
	head       bool
	headSeek   StreamMarker
	headResume bool
	tailSeek   StreamMarker
	aggMinutes []MinuteAggregate
	levels     []string
}

func planWalSkip(snap aggSnapshot, committed StreamMarker, fromN, tillN, walNeed int64, agg *thinAgg) *walSkipPlan {
	minuteN := int64(time.Minute)
	graceN := reorderGraceWindow.Nanoseconds()
	if len(snap.minutes) == 0 {
		return nil
	}
	aggLo := (fromN + minuteN - 1) / minuteN
	if !snap.committed.isZero() {
		aggLo = max(aggLo, snap.committed.time/minuteN+2)
	}
	hi := min((snap.maxAdded-graceN)/minuteN-1, tillN/minuteN-1)
	if hi < aggLo {
		return nil
	}
	match := make([]bool, len(snap.levels))
	for id, level := range snap.levels {
		match[id] = agg.bin([]byte(level)).match
	}
	byMinute := func(a MinuteAggregate, target int64) int { return cmp.Compare(a.minute, target) }
	loI, _ := slices.BinarySearchFunc(snap.minutes, aggLo, byMinute)
	hiI, found := slices.BinarySearchFunc(snap.minutes, hi, byMinute)
	if !found {
		hiI--
	}
	excluded := int64(0)
	cut := hiI
	for cut >= loI && excluded < walNeed {
		for id, n := range snap.minutes[cut].levelCounts {
			if match[id] {
				excluded += n
			}
		}
		cut--
	}
	if excluded < walNeed || cut < loI || cut < 1 {
		return nil
	}
	p := &walSkipPlan{
		aggLoN:     aggLo * minuteN,
		aggHiEndN:  (snap.minutes[cut].minute + 1) * minuteN,
		tailSeek:   snap.minutes[cut-1].start,
		aggMinutes: snap.minutes[loI : cut+1],
		levels:     snap.levels,
	}
	if fromN < p.aggLoN {
		p.head = true
		p.headSeek = committed
		p.headResume = true
		j, f := slices.BinarySearchFunc(snap.minutes, fromN/minuteN-2, byMinute)
		if !f {
			j--
		}
		if j >= 0 {
			p.headSeek = snap.minutes[j].start
			p.headResume = false
		}
	}
	return p
}

func (p *walSkipPlan) covers(t int64) bool {
	return t >= p.aggLoN && t < p.aggHiEndN
}

// visitRec is one record as seen by the query scan. level/msg come from the
// parquet columns when shredded is set and from parseLine otherwise; fields
// are parsed lazily so records that are only counted never pay for JSON
// parsing. narrow records carry no line bytes and can never be retained.
type visitRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   map[string]string
	shredded bool
	parsed   bool
	narrow   bool
}

func (v *visitRec) ensureParsed() {
	if v.parsed {
		return
	}
	v.parsed = true
	level, msg, fields := parseLine(v.rec.Line)
	v.fields = fields
	if !v.shredded {
		v.level, v.msg = level, msg
		v.shredded = true
	}
}

func (v *visitRec) levelValue() string {
	if !v.shredded {
		v.ensureParsed()
	}
	return v.level
}

func (v *visitRec) msgValue() string {
	if !v.shredded {
		v.ensureParsed()
	}
	return v.msg
}

// fieldValue addresses one logical column of a record. An empty field name
// means the message text; "level" and "msg" address the parsed columns;
// "version", "node", "run", "instance" and "stream" address the record
// metadata and shadow shredded JSON fields of the same name; anything else is
// a shredded JSON field.
func (v *visitRec) fieldValue(field string) (string, bool) {
	switch field {
	case "", "msg", "message":
		return v.msgValue(), true
	case "level":
		l := v.levelValue()
		return l, l != ""
	case "version":
		return strconv.Itoa(int(v.rec.Version)), true
	case "node":
		return strconv.Itoa(int(v.rec.Node)), true
	case "run":
		return strconv.Itoa(int(v.rec.Run)), true
	case "instance":
		return strconv.Itoa(int(v.rec.InstanceOrdinal)), true
	case "stream":
		return streamName(v.rec.Stream), true
	default:
		v.ensureParsed()
		val, ok := v.fields[field]
		return val, ok
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

func (f *compiledFilter) match(rec *visitRec) bool {
	v, ok := rec.fieldValue(f.field)
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

type fileScan struct {
	name string
	rows int64
	proj projection
	dur  time.Duration
}

type queryTrace struct {
	snapshotDur time.Duration
	walDur      time.Duration
	walRows     int64
	walAggRows  int64
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
		fmt.Fprintf(&b, "\n%s: %d rows, %v, %s", f.name, f.rows, f.dur.Round(time.Millisecond), f.proj)
	}
	if t.walAggRows > 0 {
		fmt.Fprintf(&b, "\nlive wal: %d rows, %d agg rows, %v", t.walRows, t.walAggRows, t.walDur.Round(time.Millisecond))
	} else {
		fmt.Fprintf(&b, "\nlive wal: %d rows, %v", t.walRows, t.walDur.Round(time.Millisecond))
	}
	return b.String()
}

type thinRow struct {
	Time  int64  `parquet:"time"`
	Level string `parquet:"level,optional"`
}

type narrowRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	Seq             int64  `parquet:"seq"`
	Level           string `parquet:"level,optional"`
	Msg             string `parquet:"msg,optional"`
}

// scanRange streams every record of the WAL tail plus the time-pruned parquet
// files, invoking visit per record; visit returns false to stop early. Records
// outside [fromN, tillN) may still be visited (whole tail buckets, file
// boundaries) — visitors do their own range check. fileProjection decides per
// parquet file how many columns to decode: narrow skips raw_message, thin
// reads only time and level; neither carries line bytes. When agg is set,
// thin files skip row visiting and aggregate in bulk via scanArchiveAgg. An unreadable archive file is skipped with a warning
// rather than failing the scan.
func (i *LogStreamCollector) scanRange(ctx context.Context, fromN, tillN int64, trace *queryTrace, fileProjection func(logdb.LogFile) projection, agg *thinAgg, walNeed int64, visit func(*visitRec) bool) ([]string, error) {
	snapStart := clock()
	committed, files, err := i.searchSnapshot(ctx)
	trace.snapshotDur = clock().Sub(snapStart)
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
		walStart := clock()
		sealed, cancel := context.WithCancel(context.Background())
		cancel()
		var plan *walSkipPlan
		if agg != nil && walNeed > 0 {
			if snap, ok := i.liveSpool.aggSnapshot(); ok {
				plan = planWalSkip(snap, committed, fromN, tillN, walNeed, agg)
			}
		}
		if plan != nil {
			for _, a := range plan.aggMinutes {
				agg.scanned += a.count
				trace.walAggRows += a.count
				for id, n := range a.levelCounts {
					if n > 0 {
						agg.consumeBulk(a.minute*int64(time.Minute), plan.levels[id], n)
					}
				}
			}
		}
		if plan == nil || plan.head {
			seek, resume := committed, true
			if plan != nil {
				seek, resume = plan.headSeek, plan.headResume
			}
			for r, err := range streamRecords(sealed, i.deploymentID, seek, resume) {
				if err != nil {
					trace.walDur = clock().Sub(walStart)
					return warnings, err
				}
				if plan != nil {
					if !r.m.before(plan.tailSeek) {
						break
					}
					if r.record.Time >= plan.aggLoN+reorderGraceWindow.Nanoseconds() {
						break
					}
					if plan.covers(r.record.Time) {
						continue
					}
				}
				if cancelled() {
					trace.walDur = clock().Sub(walStart)
					return warnings, ctx.Err()
				}
				trace.walRows++
				v := visitRec{rec: r.record}
				if !visit(&v) {
					trace.walDur = clock().Sub(walStart)
					return warnings, nil
				}
			}
		}
		if plan != nil {
			for r, err := range streamRecords(sealed, i.deploymentID, plan.tailSeek, false) {
				if err != nil {
					trace.walDur = clock().Sub(walStart)
					return warnings, err
				}
				if plan.covers(r.record.Time) {
					continue
				}
				if cancelled() {
					trace.walDur = clock().Sub(walStart)
					return warnings, ctx.Err()
				}
				trace.walRows++
				v := visitRec{rec: r.record}
				if !visit(&v) {
					trace.walDur = clock().Sub(walStart)
					return warnings, nil
				}
			}
		}
		trace.walDur = clock().Sub(walStart)
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return warnings, ctx.Err()
		}
		if f.MaxTime < fromN || f.MinTime >= tillN {
			trace.filesPruned++
			continue
		}
		path := archiveFilePath(i.deploymentID, f)
		fs := fileScan{name: archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq)}
		fileStart := clock()
		fileWarning := func(err error) {
			warnings = append(warnings, fmt.Sprintf("skipped unreadable archive file %s: %v", archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq), err))
		}
		proj := projFull
		if fileProjection != nil {
			proj = fileProjection(f)
		}
		fs.proj = proj
		stop := false
		if proj == projThin && agg != nil {
			rowsRead, handled, aggErr := scanArchiveAgg(ctx, path, fromN, tillN, agg)
			if handled {
				if ctx.Err() != nil {
					return warnings, ctx.Err()
				}
				fs.proj = projAgg
				fs.rows = rowsRead
				if aggErr != nil {
					fileWarning(aggErr)
				}
				fs.dur = clock().Sub(fileStart)
				trace.files = append(trace.files, fs)
				continue
			}
		}
		switch proj {
		case projThin:
			for row, err := range readArchiveRowsRange(path, fromN, tillN, func(r *thinRow) int64 { return r.Time }) {
				if err != nil {
					fileWarning(err)
					break
				}
				if cancelled() {
					return warnings, ctx.Err()
				}
				fs.rows++
				v := visitRec{
					rec: apigen.RawLogLine{
						Time:       row.Time,
						Deployment: i.deploymentID,
					},
					level:    row.Level,
					shredded: true,
					parsed:   true,
					narrow:   true,
				}
				if !visit(&v) {
					stop = true
					break
				}
			}
		case projNarrow:
			for row, err := range readArchiveRowsRange(path, fromN, tillN, func(r *narrowRow) int64 { return r.Time }) {
				if err != nil {
					fileWarning(err)
					break
				}
				if cancelled() {
					return warnings, ctx.Err()
				}
				fs.rows++
				v := visitRec{
					rec: apigen.RawLogLine{
						Time:            row.Time,
						Version:         row.Version,
						Run:             row.Run,
						Node:            row.Node,
						InstanceOrdinal: row.InstanceOrdinal,
						Stream:          row.Stream,
						Seq:             row.Seq,
						Deployment:      i.deploymentID,
					},
					level:    row.Level,
					msg:      row.Msg,
					shredded: true,
					parsed:   true,
					narrow:   true,
				}
				if !visit(&v) {
					stop = true
					break
				}
			}
		default:
			for row, err := range readArchiveRowsRange(path, fromN, tillN, func(r *logRow) int64 { return r.Time }) {
				if err != nil {
					fileWarning(err)
					break
				}
				if cancelled() {
					return warnings, ctx.Err()
				}
				fs.rows++
				v := visitRec{
					rec:      rowToRawLogLine(row, i.deploymentID),
					level:    row.Level,
					msg:      row.Msg,
					shredded: row.Level != "" || row.Msg != "" || len(row.RawMessage) == 0,
				}
				if !visit(&v) {
					stop = true
					break
				}
			}
		}
		fs.dur = clock().Sub(fileStart)
		trace.files = append(trace.files, fs)
		if stop {
			break
		}
	}
	return warnings, nil
}

type retainedRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   map[string]string
	shredded bool
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

func (i *LogStreamCollector) runQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	start := clock()
	fromN, tillN := q.from.UnixNano(), q.till.UnixNano()
	var bucketMs int64
	bucketN := 0
	bucketFromN := fromN
	if q.buckets > 0 {
		rangeMs := (tillN - fromN + 1e6 - 1) / 1e6
		bucketMs = snapBucketMs((rangeMs + int64(q.buckets) - 1) / int64(q.buckets))
		step := bucketMs * 1e6
		bucketFromN = fromN - fromN%step
		bucketN = int((tillN - bucketFromN + step - 1) / step)
	}
	counts := make([][]int64, len(levelOrder))
	ret := &retainHeap{capacity: q.limit, newest: q.newestFirst}
	fieldAccums := map[string]*fieldAccum{}
	var scanned, matched, sampled int64
	narrowOK := filtersNarrowSafe(q.filters)
	thinOK := q.configVersion == 0 && filtersThinSafe(q.filters)
	fileProjection := func(f logdb.LogFile) projection {
		if !narrowOK || sampled < fieldStatsSample {
			return projFull
		}
		if ret.capacity > 0 {
			if len(ret.recs) < ret.capacity {
				return projFull
			}
			root := &ret.recs[0].rec
			if q.newestFirst {
				if f.MaxTime >= root.Time {
					return projFull
				}
			} else if f.MinTime <= root.Time {
				return projFull
			}
		}
		if thinOK {
			return projThin
		}
		return projNarrow
	}
	var agg *thinAgg
	if thinOK {
		agg = &thinAgg{fromN: fromN, tillN: tillN, bucketFrom: bucketFromN, bucketStep: bucketMs * 1e6, bucketN: bucketN, filters: q.filters, counts: counts}
	}
	walNeed := int64(0)
	if thinOK && (q.limit <= 0 || q.newestFirst) && (bucketN == 0 || bucketMs >= 60_000) {
		walNeed = fieldStatsSample
		if int64(q.limit) > walNeed {
			walNeed = int64(q.limit)
		}
	}
	trace := &queryTrace{}
	warnings, err := i.scanRange(ctx, fromN, tillN, trace, fileProjection, agg, walNeed, func(v *visitRec) bool {
		scanned++
		if v.rec.Time < fromN || v.rec.Time >= tillN {
			return true
		}
		if q.configVersion > 0 && v.rec.Version != q.configVersion {
			return true
		}
		for fi := range q.filters {
			if !q.filters[fi].match(v) {
				return true
			}
		}
		matched++
		if bucketN > 0 {
			li := levelIndex(v.levelValue())
			if counts[li] == nil {
				counts[li] = make([]int64, bucketN)
			}
			bi := int((v.rec.Time - bucketFromN) / (bucketMs * 1e6))
			if bi >= bucketN {
				bi = bucketN - 1
			}
			counts[li][bi]++
		}
		// Value counts come from the newest fieldStatsSample matched records
		// (the scan visits the WAL tail then parquet newest-first); outside the
		// sample the field name union only grows from records that were parsed
		// anyway, so no record is parsed just to register a name.
		if sampled < fieldStatsSample {
			sampled++
			v.ensureParsed()
			if lvl := v.levelValue(); lvl != "" {
				accumField(fieldAccums, "level", lvl, true)
			}
			accumField(fieldAccums, "version", strconv.Itoa(int(v.rec.Version)), true)
			accumField(fieldAccums, "node", strconv.Itoa(int(v.rec.Node)), true)
			accumField(fieldAccums, "run", strconv.Itoa(int(v.rec.Run)), true)
			accumField(fieldAccums, "instance", strconv.Itoa(int(v.rec.InstanceOrdinal)), true)
			accumField(fieldAccums, "stream", streamName(v.rec.Stream), true)
			for k, val := range v.fields {
				if !isMetaFieldName(k) {
					accumField(fieldAccums, k, val, true)
				}
			}
		} else if v.parsed && len(fieldAccums) < maxFieldNames {
			for k, val := range v.fields {
				if !isMetaFieldName(k) {
					accumField(fieldAccums, k, val, false)
				}
			}
		}
		if !v.narrow {
			ret.offer(retainedRec{rec: v.rec, level: v.level, msg: v.msg, fields: v.fields, shredded: v.shredded})
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if agg != nil {
		scanned += agg.scanned
		matched += agg.matched
	}
	retained := ret.sorted()
	records := make([]*apigen.LogRecord, 0, len(retained))
	for idx := range retained {
		r := &retained[idx]
		level, msg, fields := r.level, r.msg, r.fields
		if fields == nil && len(r.rec.Line) > 0 {
			pl, pm, pf := parseLine(r.rec.Line)
			fields = pf
			if !r.shredded {
				level, msg = pl, pm
			}
			for k, val := range fields {
				if !isMetaFieldName(k) {
					accumField(fieldAccums, k, val, false)
				}
			}
		}
		out := &apigen.LogRecord{
			Time:            r.rec.Time,
			Level:           level,
			Msg:             msg,
			Fields:          fields,
			Version:         r.rec.Version,
			Stream:          r.rec.Stream,
			InstanceOrdinal: r.rec.InstanceOrdinal,
			Run:             r.rec.Run,
			Node:            r.rec.Node,
			Seq:             r.rec.Seq,
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
		h := &apigen.LogHistogram{BucketMs: bucketMs, StartTime: time.Unix(0, bucketFromN).UTC()}
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
	slog.InfoContext(ctx, trace.summary(time.Duration(resp.Stats.TookMs)*time.Millisecond, scanned),
		"deployment", i.deploymentID)
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
		Seq:             row.Seq,
		Line:            bytes.Clone(row.RawMessage),
		Deployment:      deploymentID,
		Node:            row.Node,
		InstanceOrdinal: row.InstanceOrdinal,
	}
}
