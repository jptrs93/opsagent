package logmanager

import (
	"container/heap"
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

type rowSource interface {
	tally() iter.Seq2[logRow, error]
	rows() iter.Seq2[logRow, error]
}

func recordToLogRow(r apigen.RawLogLine) logRow {
	return logRow{
		Time:            r.Time,
		Version:         r.Version,
		Run:             r.Run,
		Node:            r.Node,
		InstanceOrdinal: r.InstanceOrdinal,
		Stream:          r.Stream,
		Seq:             r.Seq,
		RawMessage:      r.Line,
	}
}

type walSource struct {
	deploymentID int32
	start, end   StreamMarker
}

func (s walSource) tally() iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		for r, err := range StreamDeploymentLogRecordsRange(s.deploymentID, s.start, s.end) {
			if err != nil {
				yield(logRow{}, err)
				return
			}
			if !yield(recordToLogRow(r.record), nil) {
				return
			}
		}
	}
}

func (s walSource) rows() iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		for r, err := range sortedByTime(StreamDeploymentLogRecordsRange(s.deploymentID, s.start, s.end)) {
			if err != nil {
				yield(logRow{}, err)
				return
			}
			if !yield(recordToLogRow(r.record), nil) {
				return
			}
		}
	}
}

type mergeSource struct {
	paths []string
}

func (s mergeSource) tally() iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		for _, p := range s.paths {
			for row, err := range readArchiveRows(p, 0) {
				if !yield(row, err) {
					return
				}
				if err != nil {
					return
				}
			}
		}
	}
}

type mergeHead struct {
	row  logRow
	next func() (logRow, error, bool)
	stop func()
}

type mergeHeap []*mergeHead

func (h mergeHeap) Len() int           { return len(h) }
func (h mergeHeap) Less(a, b int) bool { return cmpLogRowKey(&h[a].row, &h[b].row) < 0 }
func (h mergeHeap) Swap(a, b int)      { h[a], h[b] = h[b], h[a] }
func (h *mergeHeap) Push(x any)        { *h = append(*h, x.(*mergeHead)) }
func (h *mergeHeap) Pop() any          { o := *h; x := o[len(o)-1]; *h = o[:len(o)-1]; return x }

func (s mergeSource) rows() iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		var h mergeHeap
		defer func() {
			for _, m := range h {
				m.stop()
			}
		}()
		for _, p := range s.paths {
			next, stop := iter.Pull2(readArchiveRows(p, 0))
			row, err, ok := next()
			if err != nil {
				stop()
				yield(logRow{}, err)
				return
			}
			if !ok {
				stop()
				continue
			}
			h = append(h, &mergeHead{row: row, next: next, stop: stop})
		}
		heap.Init(&h)
		for len(h) > 0 {
			m := h[0]
			if !yield(m.row, nil) {
				return
			}
			row, err, ok := m.next()
			if err != nil {
				yield(logRow{}, err)
				return
			}
			if !ok {
				m.stop()
				heap.Pop(&h)
				continue
			}
			m.row = row
			heap.Fix(&h, 0)
		}
	}
}

type archiveOutput struct {
	seq         int64
	provisional string
	count       int64
	minTime     int64
	maxTime     int64
	node        int32
	size        int64
	keys        []logdb.InsertLogFileKeyParams
}

func writeArchiveFiles(ctx context.Context, dayDir string, deploymentID int32, src rowSource, maxRows int64) (outs []archiveOutput, err error) {
	if err := os.MkdirAll(dayDir, 0o750); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			for _, o := range outs {
				_ = os.Remove(o.provisional)
			}
			outs = nil
		}
	}()
	sc := &lineScanner{}
	tally := newKeyTally(maxTallyKeys)
	n := 0
	for row, rerr := range src.tally() {
		if rerr != nil {
			return outs, rerr
		}
		n++
		if n&4095 == 0 && ctx.Err() != nil {
			return outs, ctx.Err()
		}
		_, _, fields := sc.shred(row.RawMessage)
		tally.add(fields)
	}
	if n == 0 {
		return outs, errNoRows
	}
	as := buildArchiveSchema(tally.planDense(denseLeafBudget))
	metadata := map[string]string{"deployment": strconv.Itoa(int(deploymentID))}
	var w *archiveWriter
	var cur *archiveOutput
	finish := func() error {
		if w == nil {
			return nil
		}
		md := map[string]string{"deployment": metadata["deployment"]}
		if !w.unsorted {
			md[metadataSortedKey] = metadataSortedVal
		}
		if err := w.finish(md); err != nil {
			return err
		}
		if w.unsorted {
			if err := resortArchiveFile(cur.provisional, metadata); err != nil {
				return err
			}
		}
		info, err := os.Stat(cur.provisional)
		if err != nil {
			return err
		}
		cur.count, cur.minTime, cur.maxTime, cur.size = w.count, w.minTime, w.maxTime, info.Size()
		cur.keys = w.catalogKeys()
		w, cur = nil, nil
		return nil
	}
	n = 0
	for row, rerr := range src.rows() {
		if rerr != nil {
			if w != nil {
				w.abort()
			}
			return outs, rerr
		}
		n++
		if n&4095 == 0 && ctx.Err() != nil {
			w.abort()
			return outs, ctx.Err()
		}
		if w == nil {
			seq := newArchiveSeq()
			outs = append(outs, archiveOutput{seq: seq, provisional: filepath.Join(dayDir, provisionalFileName(seq)), node: row.Node})
			cur = &outs[len(outs)-1]
			if w, err = newArchiveWriter(cur.provisional, as); err != nil {
				return outs, err
			}
		}
		if err := w.appendRow(row, sc); err != nil {
			w.abort()
			return outs, err
		}
		if maxRows > 0 && w.count >= maxRows {
			if err := finish(); err != nil {
				return outs, err
			}
		}
	}
	if err := finish(); err != nil {
		return outs, err
	}
	if len(outs) == 0 {
		return outs, errNoRows
	}
	return outs, nil
}

type noRows struct{}

func (noRows) Error() string { return "no rows in source" }

var errNoRows = noRows{}

func insertArchiveOutput(ctx context.Context, q *logdb.Queries, deploymentID int32, day int32, level int, o *archiveOutput) error {
	id, err := q.InsertLogFile(ctx, logdb.InsertLogFileParams{
		DeploymentID: int64(deploymentID),
		Day:          int64(day),
		Level:        int64(level),
		Node:         int64(o.node),
		Seq:          o.seq,
		MinTime:      o.minTime,
		MaxTime:      o.maxTime,
		RowCount:     o.count,
		ByteSize:     o.size,
		CreatedAt:    clock().UnixMilli(),
	})
	if err != nil {
		return err
	}
	for _, k := range o.keys {
		k.FileID = id
		if err := q.InsertLogFileKey(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func finalizeOutput(dayDir string, level int, o *archiveOutput) (string, error) {
	final := filepath.Join(dayDir, archiveFileName(level, o.minTime, o.maxTime, o.node, o.seq))
	if err := os.Rename(o.provisional, final); err != nil {
		return "", err
	}
	return final, nil
}

func verifyRewrite(inputs []logdb.LogFile, outs []archiveOutput) error {
	var inRows, outRows int64
	inMin, inMax := int64(-1), int64(-1)
	for _, f := range inputs {
		inRows += f.RowCount
		if inMin < 0 || f.MinTime < inMin {
			inMin = f.MinTime
		}
		if f.MaxTime > inMax {
			inMax = f.MaxTime
		}
	}
	outMin, outMax := int64(-1), int64(-1)
	for _, o := range outs {
		outRows += o.count
		if outMin < 0 || o.minTime < outMin {
			outMin = o.minTime
		}
		if o.maxTime > outMax {
			outMax = o.maxTime
		}
	}
	if inRows != outRows {
		return fmt.Errorf("rewrite row count %d does not match inputs %d", outRows, inRows)
	}
	if inMin != outMin || inMax != outMax {
		return fmt.Errorf("rewrite time bounds %d..%d do not match inputs %d..%d", outMin, outMax, inMin, inMax)
	}
	return nil
}
