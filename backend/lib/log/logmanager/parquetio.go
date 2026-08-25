package logmanager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/storage/logdb"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	archiveLevelBatch = 0
	rowGroupRows      = 128 * 1024
	writeBatchRows    = 4096
	resortBufferRows  = 32 * 1024
	archiveExt        = ".parquet"
	tmpExt            = ".tmp"
	metadataSortedKey = "sorted"
	metadataSortedVal = "1"
)

// TODO: parse JSON lines into StructuredLogLine fields and shred them into
// their own columns. Until then every line is stored verbatim in raw_message,
// which is also what unparseable lines will always fall back to.
// Node is per row, not just per file: an L0 file is node-local and repeats one
// value, but a cross-node compaction merges rows from several nodes into one
// file, where the node in the file name and in log_files can no longer describe
// the contents.
type logRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	Seq             int64  `parquet:"seq"`
	Level           string `parquet:"level,optional"`
	Msg             string `parquet:"msg,optional"`
	RawMessage      []byte `parquet:"raw_message"`
}

func cmpLogRowKey(a, b *logRow) int {
	if a.Time != b.Time {
		return cmp.Compare(a.Time, b.Time)
	}
	if a.Node != b.Node {
		return cmp.Compare(a.Node, b.Node)
	}
	if a.InstanceOrdinal != b.InstanceOrdinal {
		return cmp.Compare(a.InstanceOrdinal, b.InstanceOrdinal)
	}
	if a.Run != b.Run {
		return cmp.Compare(a.Run, b.Run)
	}
	if a.Stream != b.Stream {
		return cmp.Compare(a.Stream, b.Stream)
	}
	return cmp.Compare(a.Seq, b.Seq)
}

func sortingColumns() []parquet.SortingColumn {
	return []parquet.SortingColumn{
		parquet.Ascending("time"),
		parquet.Ascending("node"),
		parquet.Ascending("instance_ordinal"),
		parquet.Ascending("run"),
		parquet.Ascending("stream"),
		parquet.Ascending("seq"),
	}
}

type archiveWriter struct {
	file     *os.File
	writer   *parquet.GenericWriter[logRow]
	pending  []logRow
	count    int64
	minTime  int64
	maxTime  int64
	last     logRow
	unsorted bool
}

func newArchiveWriter(path string) (*archiveWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	w := parquet.NewGenericWriter[logRow](f,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
	)
	return &archiveWriter{file: f, writer: w, pending: make([]logRow, 0, writeBatchRows)}, nil
}

func (w *archiveWriter) append(row logRow) error {
	if w.count == 0 || row.Time < w.minTime {
		w.minTime = row.Time
	}
	if w.count == 0 || row.Time > w.maxTime {
		w.maxTime = row.Time
	}
	if w.count > 0 && cmpLogRowKey(&row, &w.last) < 0 {
		w.unsorted = true
	}
	w.last = logRow{
		Time:            row.Time,
		Node:            row.Node,
		InstanceOrdinal: row.InstanceOrdinal,
		Run:             row.Run,
		Stream:          row.Stream,
		Seq:             row.Seq,
	}
	w.count++
	w.pending = append(w.pending, row)
	if len(w.pending) >= writeBatchRows {
		return w.flush()
	}
	return nil
}

func (w *archiveWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	if _, err := w.writer.Write(w.pending); err != nil {
		return err
	}
	w.pending = w.pending[:0]
	return nil
}

func (w *archiveWriter) finish(metadata map[string]string) error {
	if err := w.flush(); err != nil {
		return err
	}
	for k, v := range metadata {
		w.writer.SetKeyValueMetadata(k, v)
	}
	if err := w.writer.Close(); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	return w.file.Close()
}

func (w *archiveWriter) abort() {
	_ = w.file.Close()
}

func resortArchiveFile(path string, metadata map[string]string) error {
	base := strings.TrimSuffix(path, archiveExt+tmpExt)
	unsortedPath := base + ".unsorted" + archiveExt + tmpExt
	if err := os.Rename(path, unsortedPath); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w := parquet.NewSortingWriter[logRow](f,
		resortBufferRows,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.SortingWriterConfig(
			parquet.SortingColumns(sortingColumns()...),
			parquet.SortingBuffers(parquet.NewFileBufferPool(filepath.Dir(path), "sortbuf-*"+archiveExt+tmpExt)),
		),
	)
	fail := func(err error) error {
		_ = f.Close()
		return err
	}
	batch := make([]logRow, 0, writeBatchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := w.Write(batch)
		batch = batch[:0]
		return err
	}
	for row, err := range readArchiveRows(unsortedPath, 0) {
		if err != nil {
			return fail(err)
		}
		batch = append(batch, row)
		if len(batch) >= writeBatchRows {
			if err := flush(); err != nil {
				return fail(err)
			}
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	for k, v := range metadata {
		w.SetKeyValueMetadata(k, v)
	}
	w.SetKeyValueMetadata(metadataSortedKey, metadataSortedVal)
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(unsortedPath)
}

func archiveFileName(level int, minTime, maxTime int64, node int32, seq int64) string {
	return fmt.Sprintf("L%d_%d-%d_n%d_%d%s", level, minTime/1e6, maxTime/1e6, node, seq, archiveExt)
}

func provisionalFileName(seq int64) string {
	return fmt.Sprintf("%d%s%s", seq, archiveExt, tmpExt)
}

func parseArchiveSeq(name string) (int64, bool) {
	base := strings.TrimSuffix(name, archiveExt)
	if base == name {
		return 0, false
	}
	idx := strings.LastIndexByte(base, '_')
	if idx < 0 {
		return 0, false
	}
	seq, err := strconv.ParseInt(base[idx+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

func archiveFilePath(deploymentID int32, f logdb.LogFile) string {
	return filepath.Join(
		archiveDayDir(deploymentID, int32(f.Day)),
		archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq),
	)
}

func archiveGroupMaxTime(rg parquet.RowGroup, timeCol int) (int64, bool) {
	ci, err := rg.ColumnChunks()[timeCol].ColumnIndex()
	if err != nil || ci == nil {
		return 0, false
	}
	var maxTime int64
	found := false
	for i := 0; i < ci.NumPages(); i++ {
		if ci.NullPage(i) {
			continue
		}
		if v := ci.MaxValue(i).Int64(); !found || v > maxTime {
			maxTime = v
			found = true
		}
	}
	return maxTime, found
}

// scanArchiveAgg aggregates one archive file directly into agg without
// yielding rows, reading the time and level column chunks at the page level
// rather than through row reconstruction. handled is false when the file has
// no level column, in which case the caller falls back to the row-visiting
// thin scan. The returned row count matches what the visiting scan would
// have counted.
func scanArchiveAgg(ctx context.Context, path string, fromN, tillN int64, agg *thinAgg) (rows int64, handled bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, true, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, true, err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		return 0, true, err
	}
	levelCol, ok := pf.Schema().Lookup("level")
	if !ok {
		return 0, false, nil
	}
	timeCol, ok := pf.Schema().Lookup("time")
	if !ok {
		return 0, false, nil
	}
	sortedVal, _ := pf.Lookup(metadataSortedKey)
	sorted := sortedVal == metadataSortedVal
	before := agg.scanned
	times := make([]int64, writeBatchRows)
	tbuf := make([]parquet.Value, writeBatchRows)
	levels := make([]parquet.Value, writeBatchRows)
	for _, rg := range pf.RowGroups() {
		if maxTime, ok := archiveGroupMaxTime(rg, timeCol.ColumnIndex); ok && maxTime < fromN {
			continue
		}
		if ctx.Err() != nil {
			return agg.scanned - before, true, ctx.Err()
		}
		done, err := aggRowGroup(rg, timeCol.ColumnIndex, levelCol.ColumnIndex, sorted, agg, times, tbuf, levels)
		if err != nil {
			return agg.scanned - before, true, err
		}
		if done {
			break
		}
	}
	return agg.scanned - before, true, nil
}

func aggRowGroup(rg parquet.RowGroup, timeIdx, levelIdx int, sorted bool, agg *thinAgg, times []int64, tbuf, levels []parquet.Value) (bool, error) {
	tp := rg.ColumnChunks()[timeIdx].Pages()
	defer tp.Close()
	lp := rg.ColumnChunks()[levelIdx].Pages()
	defer lp.Close()
	tc := &int64Cursor{pages: tp, vbuf: tbuf}
	lc := &valueCursor{pages: lp}
	for {
		n, err := tc.read(times)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
		for got := 0; got < n; {
			k, err := lc.read(levels[got:n])
			if err != nil {
				if errors.Is(err, io.EOF) {
					return false, errors.New("level column shorter than time column")
				}
				return false, err
			}
			got += k
		}
		if agg.consume(times[:n], levels[:n], sorted) {
			return true, nil
		}
	}
}

// int64Cursor streams a required int64 column chunk page by page, using the
// typed reader when the page offers one and boxed values otherwise. read
// never returns (0, nil): it advances pages until it has values or the chunk
// ends with io.EOF.
type int64Cursor struct {
	pages parquet.Pages
	ir    parquet.Int64Reader
	vr    parquet.ValueReader
	vbuf  []parquet.Value
}

func (c *int64Cursor) read(buf []int64) (int, error) {
	for {
		if c.ir != nil {
			n, err := c.ir.ReadInt64s(buf)
			if err != nil && errors.Is(err, io.EOF) {
				c.ir = nil
				err = nil
			}
			if n > 0 || err != nil {
				return n, err
			}
		} else if c.vr != nil {
			k := min(len(buf), len(c.vbuf))
			n, err := c.vr.ReadValues(c.vbuf[:k])
			for i := 0; i < n; i++ {
				buf[i] = c.vbuf[i].Int64()
			}
			if err != nil && errors.Is(err, io.EOF) {
				c.vr = nil
				err = nil
			}
			if n > 0 || err != nil {
				return n, err
			}
		}
		p, err := c.pages.ReadPage()
		if err != nil {
			return 0, err
		}
		vals := p.Values()
		if ir, ok := vals.(parquet.Int64Reader); ok {
			c.ir, c.vr = ir, nil
		} else {
			c.ir, c.vr = nil, vals
		}
	}
}

// valueCursor streams a column chunk as boxed values page by page. For a
// dictionary-encoded chunk the byte-array values point into the dictionary
// buffer, so no per-value allocation happens; nulls (how optional empty
// levels are stored) come through as null values. read never returns
// (0, nil): it advances pages until it has values or the chunk ends with
// io.EOF.
type valueCursor struct {
	pages parquet.Pages
	vr    parquet.ValueReader
}

func (c *valueCursor) read(buf []parquet.Value) (int, error) {
	for {
		if c.vr != nil {
			n, err := c.vr.ReadValues(buf)
			if err != nil && errors.Is(err, io.EOF) {
				c.vr = nil
				err = nil
			}
			if n > 0 || err != nil {
				return n, err
			}
		}
		p, err := c.pages.ReadPage()
		if err != nil {
			return 0, err
		}
		c.vr = p.Values()
	}
}

func readArchiveRowsRange[T any](path string, fromN, tillN int64, rowTime func(*T) int64) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		f, err := os.Open(path)
		if err != nil {
			yield(zero, err)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			yield(zero, err)
			return
		}
		pf, err := parquet.OpenFile(f, st.Size())
		if err != nil {
			yield(zero, err)
			return
		}
		sortedVal, _ := pf.Lookup(metadataSortedKey)
		sorted := sortedVal == metadataSortedVal
		var skip int64
		if timeCol, ok := pf.Schema().Lookup("time"); ok {
			for _, rg := range pf.RowGroups() {
				maxTime, ok := archiveGroupMaxTime(rg, timeCol.ColumnIndex)
				if !ok || maxTime >= fromN {
					break
				}
				skip += rg.NumRows()
			}
		}
		r := parquet.NewGenericReader[T](pf)
		defer r.Close()
		if skip > 0 {
			if err := r.SeekToRow(skip); err != nil {
				yield(zero, err)
				return
			}
		}
		rows := make([]T, writeBatchRows)
		for {
			n, err := r.Read(rows)
			for i := range rows[:n] {
				if sorted && rowTime(&rows[i]) >= tillN {
					return
				}
				if !yield(rows[i], nil) {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(zero, err)
				}
				return
			}
		}
	}
}

func readArchiveRows(path string, skip int64) iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		f, err := os.Open(path)
		if err != nil {
			yield(logRow{}, err)
			return
		}
		defer f.Close()
		r := parquet.NewGenericReader[logRow](f)
		defer r.Close()
		if skip > 0 {
			if err := r.SeekToRow(skip); err != nil {
				yield(logRow{}, err)
				return
			}
		}
		rows := make([]logRow, writeBatchRows)
		for {
			n, err := r.Read(rows)
			for _, row := range rows[:n] {
				if !yield(row, nil) {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(logRow{}, err)
				}
				return
			}
		}
	}
}

func newArchiveSeq() int64 {
	return int64(rand.Uint64() >> 1)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
