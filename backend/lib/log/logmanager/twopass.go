package logmanager

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

var fetchParallelism = 4

// thinAgg accumulates histogram and match counts. Each distinct level string
// gets one lazily built bin holding its histogram series index and its
// verdict from the real compiled-filter match code, so level-only fast paths
// can never disagree with the row-visiting path.
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
	str   string
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
	v := visitRec{level: level, shredded: true, parsed: true}
	match := true
	for i := range a.filters {
		if !a.filters[i].match(&v) {
			match = false
			break
		}
	}
	a.bins = append(a.bins, levelBin{val: []byte(level), str: level, li: levelIndex(level), match: match})
	return &a.bins[len(a.bins)-1]
}

func (a *thinAgg) addBucket(t int64, li int) {
	if a.bucketN <= 0 {
		return
	}
	c := a.counts[li]
	if c == nil {
		c = make([]int64, a.bucketN)
		a.counts[li] = c
	}
	bi := int((t - a.bucketFrom) / a.bucketStep)
	if bi >= a.bucketN {
		bi = a.bucketN - 1
	}
	c[bi]++
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

type walPlan struct {
	aggLoN       int64
	aggHiEndN    int64
	head         bool
	headSeek     StreamMarker
	headResume   bool
	tailSeek     StreamMarker
	aggMinutes   []MinuteAggregate
	levels       []string
	skipAll      bool
	matchMinutes map[int64]bool
}

func (p *walPlan) covers(t int64) bool {
	return t >= p.aggLoN && t < p.aggHiEndN
}

func planWal(snap aggSnapshot, committed StreamMarker, fromN, tillN, walNeed int64, newestFirst bool, agg *thinAgg) *walPlan {
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
	if hiI < loI {
		return nil
	}
	p := &walPlan{aggLoN: aggLo * minuteN, levels: snap.levels}
	setHead := func() {
		p.head = true
		p.headSeek, p.headResume = committed, true
		j, f := slices.BinarySearchFunc(snap.minutes, fromN/minuteN-2, byMinute)
		if !f {
			j--
		}
		if j >= 0 {
			p.headSeek, p.headResume = snap.minutes[j].start, false
		}
	}
	if newestFirst && walNeed > 0 {
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
		if excluded >= walNeed && cut >= loI && cut >= 1 {
			p.skipAll = true
			p.aggHiEndN = (snap.minutes[cut].minute + 1) * minuteN
			p.tailSeek = snap.minutes[cut-1].start
			p.aggMinutes = snap.minutes[loI : cut+1]
			if fromN < p.aggLoN {
				setHead()
			}
			return p
		}
	}
	p.aggHiEndN = (snap.minutes[hiI].minute + 1) * minuteN
	p.aggMinutes = snap.minutes[loI : hiI+1]
	p.matchMinutes = map[int64]bool{}
	for i := range p.aggMinutes {
		for id, n := range p.aggMinutes[i].levelCounts {
			if n > 0 && id < len(match) && match[id] {
				p.matchMinutes[p.aggMinutes[i].minute] = true
				break
			}
		}
	}
	setHead()
	return p
}

func (e *queryEngine) scanWalTwoPass(ctx context.Context, committed StreamMarker, q *queryParams, agg *thinAgg, ret *retainHeap, captureK int64, trace *queryTrace) error {
	if !(committed.isZero() || agg.tillN > committed.time-reorderGraceWindow.Nanoseconds()) {
		return nil
	}
	walStart := clock()
	defer func() { trace.walDur = clock().Sub(walStart) }()
	sealed, cancel := context.WithCancel(context.Background())
	cancel()
	minuteN := int64(time.Minute)
	var plan *walPlan
	if filtersLevelOnly(q.filters) && q.specVersion == 0 && (agg.bucketN == 0 || agg.bucketStep >= minuteN) {
		if snap, ok := e.spool.aggSnapshot(); ok {
			plan = planWal(snap, committed, agg.fromN, agg.tillN, captureK, q.newestFirst, agg)
		}
	}
	if plan != nil {
		for _, a := range plan.aggMinutes {
			agg.scanned += a.count
			trace.walAggRows += a.count
			for id, n := range a.levelCounts {
				if n > 0 {
					agg.consumeBulk(a.minute*minuteN, plan.levels[id], n)
				}
			}
		}
	}
	n := 0
	visit := func(r WrappedRecord, retentionOnly bool) error {
		n++
		if n&1023 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		trace.walRows++
		v := visitRec{rec: r.record}
		if !retentionOnly {
			agg.scanned++
			if v.rec.Time < agg.fromN || v.rec.Time >= agg.tillN {
				return nil
			}
			if q.specVersion > 0 && v.rec.Version != q.specVersion {
				return nil
			}
		}
		for fi := range q.filters {
			if !q.filters[fi].match(&v) {
				return nil
			}
		}
		if !retentionOnly {
			agg.matched++
			if agg.bucketN > 0 {
				agg.addBucket(v.rec.Time, levelIndex(v.levelValue()))
			}
		}
		ret.offer(retainedRec{rec: v.rec, level: v.level, msg: v.msg, fields: v.fields, shredded: v.shredded, fileIdx: -1})
		return nil
	}
	if plan == nil || plan.head {
		seek, resume := committed, true
		if plan != nil {
			seek, resume = plan.headSeek, plan.headResume
		}
		for r, err := range streamRecords(sealed, e.deploymentID, seek, resume) {
			if err != nil {
				return err
			}
			if plan != nil && plan.skipAll {
				if !r.m.before(plan.tailSeek) {
					break
				}
				if r.record.Time >= plan.aggLoN+reorderGraceWindow.Nanoseconds() {
					break
				}
				if plan.covers(r.record.Time) {
					continue
				}
			} else if plan != nil && plan.covers(r.record.Time) {
				if !plan.matchMinutes[r.record.Time/minuteN] {
					continue
				}
				if err := visit(r, true); err != nil {
					return err
				}
				continue
			}
			if err := visit(r, false); err != nil {
				return err
			}
		}
	}
	if plan != nil && plan.skipAll {
		for r, err := range streamRecords(sealed, e.deploymentID, plan.tailSeek, false) {
			if err != nil {
				return err
			}
			if plan.covers(r.record.Time) {
				continue
			}
			if err := visit(r, false); err != nil {
				return err
			}
		}
	}
	return nil
}

type archiveEval struct {
	deploymentID int32
	q            *queryParams
	agg          *thinAgg
	ret          *retainHeap
	levelOnly    bool
	needMsg      bool
	capture      bool
	fileIdx      int
	rows         int64
}

func (ev *archiveEval) consume(b *cheapBatch, n int, baseRow int64, sorted bool) bool {
	for i := 0; i < n; i++ {
		t := b.times[i]
		if sorted && t >= ev.agg.tillN {
			return true
		}
		ev.rows++
		ev.agg.scanned++
		if t < ev.agg.fromN || t >= ev.agg.tillN {
			continue
		}
		bin := ev.agg.bin(b.levels[i].ByteArray())
		if ev.levelOnly {
			if !bin.match {
				continue
			}
		} else {
			v := visitRec{rec: apigen.RawLogLine{Time: t, Deployment: ev.deploymentID}, level: bin.str, shredded: true, parsed: true}
			if b.versions != nil {
				v.rec.Version = b.versions[i]
				v.rec.Node = b.nodes[i]
				v.rec.InstanceOrdinal = b.instances[i]
				v.rec.Run = b.runs[i]
				v.rec.Stream = b.streams[i]
				v.rec.Seq = b.seqs[i]
			}
			if ev.needMsg {
				v.msg = string(b.msgs[i].ByteArray())
			}
			if ev.q.specVersion > 0 && v.rec.Version != ev.q.specVersion {
				continue
			}
			ok := true
			for fi := range ev.q.filters {
				if !ev.q.filters[fi].match(&v) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		ev.agg.matched++
		ev.agg.addBucket(t, bin.li)
		if ev.capture {
			ev.ret.offer(retainedRec{
				rec: apigen.RawLogLine{
					Time:            t,
					Node:            b.nodes[i],
					InstanceOrdinal: b.instances[i],
					Run:             b.runs[i],
					Stream:          b.streams[i],
					Seq:             b.seqs[i],
					Deployment:      ev.deploymentID,
				},
				level:   bin.str,
				pending: true,
				fileIdx: ev.fileIdx,
				rowIdx:  baseRow + int64(i),
			})
		}
	}
	return false
}

func (e *queryEngine) runTwoPassQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	start := clock()
	fromN, tillN := q.from.UnixNano(), q.till.UnixNano()
	b := planBuckets(q.buckets, fromN, tillN)
	counts := make([][]int64, len(levelOrder))
	agg := &thinAgg{fromN: fromN, tillN: tillN, bucketFrom: b.fromN, bucketStep: b.stepN, bucketN: b.n, filters: q.filters, counts: counts}
	captureK := q.limit
	if captureK < int(fieldStatsSample) {
		captureK = int(fieldStatsSample)
	}
	ret := &retainHeap{capacity: captureK, newest: q.newestFirst}
	trace := &queryTrace{}
	snapStart := clock()
	committed, files, err := e.snapshot(ctx)
	trace.snapshotDur = clock().Sub(snapStart)
	if err != nil {
		return nil, err
	}
	if err := e.scanWalTwoPass(ctx, committed, &q, agg, ret, int64(captureK), trace); err != nil {
		return nil, err
	}
	var warnings []string
	needMsg := filtersNeedMsg(q.filters)
	levelOnly := filtersLevelOnly(q.filters) && q.specVersion == 0
	metaFiltered := filtersReferenceMeta(q.filters)
	for fi := range files {
		f := files[fi]
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if f.MaxTime < fromN || f.MinTime >= tillN {
			trace.filesPruned++
			continue
		}
		capture := len(ret.recs) < ret.capacity
		if !capture && len(ret.recs) > 0 {
			root := &ret.recs[0].rec
			if q.newestFirst {
				capture = f.MaxTime >= root.Time
			} else {
				capture = f.MinTime <= root.Time
			}
		}
		needs := columnNeeds{msg: needMsg, ints: capture || metaFiltered || q.specVersion > 0}
		ev := &archiveEval{deploymentID: e.deploymentID, q: &q, agg: agg, ret: ret, levelOnly: levelOnly, needMsg: needMsg, capture: capture, fileIdx: fi}
		path := archiveFilePath(e.deploymentID, f)
		fs := fileScan{name: archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq), mode: "agg"}
		if capture {
			fs.mode = "capture"
		}
		fileStart := clock()
		scanErr := scanArchiveColumns(ctx, path, fromN, needs, ev.consume)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if scanErr != nil {
			warnings = append(warnings, fmt.Sprintf("skipped unreadable archive file %s: %v", fs.name, scanErr))
		}
		fs.rows = ev.rows
		fs.dur = clock().Sub(fileStart)
		trace.files = append(trace.files, fs)
	}
	e.resolvePending(ret, files, trace, &warnings)
	retained := ret.sorted()
	fieldAccums := map[string]*fieldAccum{}
	limit := q.limit
	if limit > len(retained) {
		limit = len(retained)
	}
	records := make([]*apigen.LogRecord, 0, limit)
	var sampled int64
	for idx := range retained {
		r := &retained[idx]
		if r.pending {
			continue
		}
		level, msg, fields := r.level, r.msg, r.fields
		if fields == nil && len(r.rec.Line) > 0 {
			pl, pm, pf := parseLine(r.rec.Line)
			fields = pf
			if !r.shredded {
				level, msg = pl, pm
			}
		}
		if sampled < fieldStatsSample {
			sampled++
			if level != "" {
				accumField(fieldAccums, "level", level, true)
			}
			accumField(fieldAccums, "version", strconv.Itoa(int(r.rec.Version)), true)
			accumField(fieldAccums, "node", strconv.Itoa(int(r.rec.Node)), true)
			accumField(fieldAccums, "run", strconv.Itoa(int(r.rec.Run)), true)
			accumField(fieldAccums, "instance", strconv.Itoa(int(r.rec.InstanceOrdinal)), true)
			accumField(fieldAccums, "stream", streamName(r.rec.Stream), true)
			for k, val := range fields {
				if !isMetaFieldName(k) {
					accumField(fieldAccums, k, val, true)
				}
			}
		} else if len(fieldAccums) < maxFieldNames {
			for k, val := range fields {
				if !isMetaFieldName(k) {
					accumField(fieldAccums, k, val, false)
				}
			}
		}
		if len(records) < q.limit {
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
	}
	resp := queryResponse(&q, start, agg.scanned, agg.matched, sampled, records, fieldAccums, warnings, b, counts)
	slog.InfoContext(ctx, trace.summary(time.Duration(resp.Stats.TookMs)*time.Millisecond, agg.scanned),
		"deployment", e.deploymentID)
	return resp, nil
}

func (e *queryEngine) resolvePending(ret *retainHeap, files []logdb.LogFile, trace *queryTrace, warnings *[]string) {
	byFile := map[int][]int{}
	for i := range ret.recs {
		if ret.recs[i].pending {
			byFile[ret.recs[i].fileIdx] = append(byFile[ret.recs[i].fileIdx], i)
		}
	}
	if len(byFile) == 0 {
		return
	}
	fetchStart := clock()
	sem := make(chan struct{}, fetchParallelism)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for fi, idxs := range byFile {
		wg.Add(1)
		go func(fi int, idxs []int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			slices.SortFunc(idxs, func(a, b int) int { return cmp.Compare(ret.recs[a].rowIdx, ret.recs[b].rowIdx) })
			rows := make([]int64, len(idxs))
			for k, i := range idxs {
				rows[k] = ret.recs[i].rowIdx
			}
			name := archiveFileName(int(files[fi].Level), files[fi].MinTime, files[fi].MaxTime, int32(files[fi].Node), files[fi].Seq)
			fetched, err := fetchArchiveRows(archiveFilePath(e.deploymentID, files[fi]), rows)
			if err != nil {
				mu.Lock()
				*warnings = append(*warnings, fmt.Sprintf("failed loading matched records from archive file %s: %v", name, err))
				mu.Unlock()
				return
			}
			for k, i := range idxs {
				row := fetched[k]
				rec := rowToRawLogLine(row, e.deploymentID)
				r := &ret.recs[i]
				if cmpRecordKey(&rec, &r.rec) != 0 {
					mu.Lock()
					*warnings = append(*warnings, fmt.Sprintf("archive file %s row %d changed underneath the query", name, r.rowIdx))
					mu.Unlock()
					continue
				}
				r.rec = rec
				r.level, r.msg = row.Level, row.Msg
				r.shredded = row.Level != "" || row.Msg != "" || len(row.RawMessage) == 0
				r.pending = false
			}
			mu.Lock()
			trace.fetchRows += int64(len(idxs))
			mu.Unlock()
		}(fi, idxs)
	}
	wg.Wait()
	trace.fetchDur = clock().Sub(fetchStart)
}
