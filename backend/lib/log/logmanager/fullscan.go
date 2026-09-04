package logmanager

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (e *queryEngine) runFullQuery(ctx context.Context, q queryParams) (*apigen.LogQueryResponse, error) {
	start := clock()
	fromN, tillN := q.from.UnixNano(), q.till.UnixNano()
	b := planBuckets(q.buckets, fromN, tillN)
	counts := make([][]int64, len(levelOrder))
	ret := &retainHeap{capacity: q.limit, newest: q.newestFirst}
	fieldAccums := map[string]*fieldAccum{}
	var scanned, matched, sampled int64
	trace := &queryTrace{}
	warnings, err := e.scanRangeFull(ctx, fromN, tillN, trace, func(v *visitRec) bool {
		scanned++
		if v.rec.Time < fromN || v.rec.Time >= tillN {
			return true
		}
		if q.specVersion > 0 && v.rec.Version != q.specVersion {
			return true
		}
		for fi := range q.filters {
			if !q.filters[fi].match(v) {
				return true
			}
		}
		matched++
		if b.n > 0 {
			li := levelIndex(v.levelValue())
			if counts[li] == nil {
				counts[li] = make([]int64, b.n)
			}
			bi := int((v.rec.Time - b.fromN) / b.stepN)
			if bi >= b.n {
				bi = b.n - 1
			}
			counts[li][bi]++
		}
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
		ret.offer(retainedRec{rec: v.rec, level: v.level, msg: v.msg, fields: v.fields, shredded: v.shredded})
		return true
	})
	if err != nil {
		return nil, err
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
	resp := queryResponse(&q, start, scanned, matched, sampled, records, fieldAccums, warnings, b, counts)
	slog.InfoContext(ctx, trace.summary(time.Duration(resp.Stats.TookMs)*time.Millisecond, scanned),
		"deployment", e.deploymentID)
	return resp, nil
}

func (e *queryEngine) scanRangeFull(ctx context.Context, fromN, tillN int64, trace *queryTrace, visit func(*visitRec) bool) ([]string, error) {
	snapStart := clock()
	committed, files, err := e.snapshot(ctx)
	trace.snapshotDur = clock().Sub(snapStart)
	if err != nil {
		return nil, err
	}
	var warnings []string
	n := 0
	sc := &lineScanner{}
	cancelled := func() bool {
		n++
		return n&1023 == 0 && ctx.Err() != nil
	}
	if committed.isZero() || tillN > committed.time-reorderGraceWindow.Nanoseconds() {
		walStart := clock()
		sealed, cancel := context.WithCancel(context.Background())
		cancel()
		for r, err := range streamRecords(sealed, e.deploymentID, committed, true) {
			if err != nil {
				trace.walDur = clock().Sub(walStart)
				return warnings, err
			}
			if cancelled() {
				trace.walDur = clock().Sub(walStart)
				return warnings, ctx.Err()
			}
			trace.walRows++
			v := visitRec{rec: r.record, sc: sc}
			if !visit(&v) {
				trace.walDur = clock().Sub(walStart)
				return warnings, nil
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
		path := archiveFilePath(e.deploymentID, f)
		fs := fileScan{name: archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq), mode: "full"}
		fileStart := clock()
		stop := false
		for row, rerr := range readArchiveRowsRange(path, fromN, tillN, func(r *logRow) int64 { return r.Time }) {
			if rerr != nil {
				warnings = append(warnings, fmt.Sprintf("skipped unreadable archive file %s: %v", fs.name, rerr))
				break
			}
			if cancelled() {
				return warnings, ctx.Err()
			}
			fs.rows++
			v := visitRec{
				rec:      rowToRawLogLine(row, e.deploymentID),
				level:    row.Level,
				msg:      row.Msg,
				shredded: row.Level != "" || row.Msg != "" || len(row.RawMessage) == 0,
				sc:       sc,
			}
			if !visit(&v) {
				stop = true
				break
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
