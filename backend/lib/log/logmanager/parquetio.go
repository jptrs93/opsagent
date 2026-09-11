package logmanager

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
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
	archiveLevelShredded = 1
	archiveLevelRollup   = 2
	rowGroupRows         = 128 * 1024
	writeBatchRows       = 4096
	writeBatchValues     = 1 << 18
	resortBufferRows     = 32 * 1024
	archiveExt           = ".parquet"
	tmpExt               = ".tmp"
	metadataSortedKey    = "sorted"
	metadataSortedVal    = "1"
)

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

type rowKey struct {
	time     int64
	node     int32
	instance int32
	run      int32
	stream   int32
	seq      int64
}

func cmpRowKey(a, b *rowKey) int {
	if a.time != b.time {
		return cmp.Compare(a.time, b.time)
	}
	if a.node != b.node {
		return cmp.Compare(a.node, b.node)
	}
	if a.instance != b.instance {
		return cmp.Compare(a.instance, b.instance)
	}
	if a.run != b.run {
		return cmp.Compare(a.run, b.run)
	}
	if a.stream != b.stream {
		return cmp.Compare(a.stream, b.stream)
	}
	return cmp.Compare(a.seq, b.seq)
}

func cmpLogRowKey(a, b *logRow) int {
	ka := rowKey{a.Time, a.Node, a.InstanceOrdinal, a.Run, a.Stream, a.Seq}
	kb := rowKey{b.Time, b.Node, b.InstanceOrdinal, b.Run, b.Stream, b.Seq}
	return cmpRowKey(&ka, &kb)
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
	writer   *parquet.GenericWriter[any]
	as       *archiveSchema
	rb       *rowBuilder
	keys     *keyTally
	count    int64
	minTime  int64
	maxTime  int64
	last     rowKey
	unsorted bool
}

func newArchiveWriter(path string, as *archiveSchema) (*archiveWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	w := parquet.NewGenericWriter[any](f,
		as.schema,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
	)
	return &archiveWriter{file: f, writer: w, as: as, rb: newRowBuilder(as), keys: newKeyTally(0)}, nil
}

func (w *archiveWriter) append(time int64, version, run, node, instance, stream int32, seq int64, level, msg string, raw []byte, fields []shredField) error {
	if w.count == 0 || time < w.minTime {
		w.minTime = time
	}
	if w.count == 0 || time > w.maxTime {
		w.maxTime = time
	}
	key := rowKey{time, node, instance, run, stream, seq}
	if w.count > 0 && cmpRowKey(&key, &w.last) < 0 {
		w.unsorted = true
	}
	w.last = key
	w.count++
	w.keys.add(fields)
	w.rb.add(time, version, run, node, instance, stream, seq, level, msg, raw, fields)
	if len(w.rb.rows) >= writeBatchRows || w.rb.values >= writeBatchValues {
		return w.flush()
	}
	return nil
}

func (w *archiveWriter) appendRow(row logRow, sc *lineScanner) error {
	level, msg, fields := sc.shred(row.RawMessage)
	return w.append(row.Time, row.Version, row.Run, row.Node, row.InstanceOrdinal, row.Stream, row.Seq, level, msg, row.RawMessage, fields)
}

func (w *archiveWriter) flush() error {
	if len(w.rb.rows) == 0 {
		return nil
	}
	_, err := w.writer.WriteRows(w.rb.rows)
	w.rb.reset()
	return err
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

func (w *archiveWriter) catalogKeys() []logdb.InsertLogFileKeyParams {
	variants := w.keys.variants()
	out := make([]logdb.InsertLogFileKeyParams, 0, len(variants))
	for _, v := range variants {
		placement := int64(placementSpill)
		if idx, ok := w.as.denseIdx[v.key]; ok && idx[v.typ] >= 0 {
			placement = placementDense
		}
		out = append(out, logdb.InsertLogFileKeyParams{Key: v.key, Type: int64(v.typ), Placement: placement, RowCount: v.rows})
	}
	return out
}

func openArchive(f *os.File) (*parquet.File, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return parquet.OpenFile(f, st.Size(), parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
}

func resortArchiveFile(path string, metadata map[string]string) error {
	base := strings.TrimSuffix(path, archiveExt+tmpExt)
	unsortedPath := base + ".unsorted" + archiveExt + tmpExt
	if err := os.Rename(path, unsortedPath); err != nil {
		return err
	}
	in, err := os.Open(unsortedPath)
	if err != nil {
		return err
	}
	defer in.Close()
	pf, err := openArchive(in)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	w := parquet.NewSortingWriter[any](f,
		resortBufferRows,
		pf.Schema(),
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
	buf := make([]parquet.Row, 1024)
	for _, rg := range pf.RowGroups() {
		rows := rg.Rows()
		for {
			n, rerr := rows.ReadRows(buf)
			if n > 0 {
				if _, err := w.WriteRows(buf[:n]); err != nil {
					_ = rows.Close()
					return fail(err)
				}
				for i := range buf[:n] {
					buf[i] = buf[i][:0]
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					_ = rows.Close()
					return fail(rerr)
				}
				break
			}
		}
		if err := rows.Close(); err != nil {
			return fail(err)
		}
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

func logFileName(f logdb.LogFile) string {
	return archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq)
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

type fieldNeed struct {
	key   string
	typ   fieldType
	dense bool
}

type rowGroupPrune struct {
	needs []int
	lit   *literal
	op    string
}

type columnNeeds struct {
	msg    bool
	ints   bool
	fields []fieldNeed
	prune  []rowGroupPrune
}

type cheapBatch struct {
	times     []int64
	levels    []parquet.Value
	msgs      []parquet.Value
	versions  []int32
	nodes     []int32
	instances []int32
	runs      []int32
	streams   []int32
	seqs      []int64
	fields    [][]parquet.Value
}

type cheapCols struct {
	time, level, msg                     int
	version, node, instance, run, stream int
	seq                                  int
	dense                                []int
	spill                                [fieldTypes]spillCols
	spillUsed                            [fieldTypes]bool
}

func scanArchiveColumns(ctx context.Context, path string, fromN int64, needs columnNeeds, consume func(b *cheapBatch, n int, baseRow int64, sorted bool) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	pf, err := openArchive(f)
	if err != nil {
		return err
	}
	schema := pf.Schema()
	lookup := func(name ...string) (int, error) {
		c, ok := schema.Lookup(name...)
		if !ok {
			return 0, fmt.Errorf("missing column %s", strings.Join(name, "."))
		}
		return c.ColumnIndex, nil
	}
	var cols cheapCols
	if cols.time, err = lookup("time"); err != nil {
		return err
	}
	if cols.level, err = lookup("level"); err != nil {
		return err
	}
	if needs.msg {
		if cols.msg, err = lookup("msg"); err != nil {
			return err
		}
	}
	if needs.ints {
		for _, c := range []struct {
			name string
			dst  *int
		}{
			{"version", &cols.version}, {"node", &cols.node}, {"instance_ordinal", &cols.instance},
			{"run", &cols.run}, {"stream", &cols.stream}, {"seq", &cols.seq},
		} {
			if *c.dst, err = lookup(c.name); err != nil {
				return err
			}
		}
	}
	cols.dense = make([]int, len(needs.fields))
	for i, fn := range needs.fields {
		if fn.dense {
			if cols.dense[i], err = lookup(denseColumnName(fn.key, fn.typ)); err != nil {
				return err
			}
			continue
		}
		cols.dense[i] = -1
		if !cols.spillUsed[fn.typ] {
			cols.spillUsed[fn.typ] = true
			if cols.spill[fn.typ].key, err = lookup(spillColNames[fn.typ], "key_value", "key"); err != nil {
				return err
			}
			if cols.spill[fn.typ].val, err = lookup(spillColNames[fn.typ], "key_value", "value"); err != nil {
				return err
			}
		}
	}
	sortedVal, _ := pf.Lookup(metadataSortedKey)
	sorted := sortedVal == metadataSortedVal
	b := &cheapBatch{
		times:  make([]int64, writeBatchRows),
		levels: make([]parquet.Value, writeBatchRows),
	}
	if needs.msg {
		b.msgs = make([]parquet.Value, writeBatchRows)
	}
	if needs.ints {
		b.versions = make([]int32, writeBatchRows)
		b.nodes = make([]int32, writeBatchRows)
		b.instances = make([]int32, writeBatchRows)
		b.runs = make([]int32, writeBatchRows)
		b.streams = make([]int32, writeBatchRows)
		b.seqs = make([]int64, writeBatchRows)
	}
	b.fields = make([][]parquet.Value, len(needs.fields))
	for i := range b.fields {
		b.fields[i] = make([]parquet.Value, writeBatchRows)
	}
	base := int64(0)
	for _, rg := range pf.RowGroups() {
		nrows := rg.NumRows()
		if maxTime, ok := archiveGroupMaxTime(rg, cols.time); ok && maxTime < fromN {
			base += nrows
			continue
		}
		if pruneRowGroup(rg, cols, needs) {
			base += nrows
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		done, err := scanColumnsRowGroup(rg, cols, needs, base, sorted, b, consume)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		base += nrows
	}
	return nil
}

func pruneRowGroup(rg parquet.RowGroup, cols cheapCols, needs columnNeeds) bool {
	if len(needs.prune) == 0 {
		return false
	}
	chunks := rg.ColumnChunks()
	for _, p := range needs.prune {
		canMatch := false
		for _, ni := range p.needs {
			lo, hi, ok := chunkNumericBounds(chunks[cols.dense[ni]])
			if !ok {
				continue
			}
			lit := p.lit.asFloat()
			var possible bool
			switch p.op {
			case "eq":
				possible = lit >= lo && lit <= hi
			case "gt":
				possible = hi > lit
			case "gte":
				possible = hi >= lit
			case "lt":
				possible = lo < lit
			case "lte":
				possible = lo <= lit
			default:
				possible = true
			}
			if possible {
				canMatch = true
				break
			}
		}
		if !canMatch {
			return true
		}
	}
	return false
}

func chunkNumericBounds(chunk parquet.ColumnChunk) (lo, hi float64, ok bool) {
	ci, err := chunk.ColumnIndex()
	if err != nil || ci == nil {
		return 0, 0, false
	}
	found := false
	for i := 0; i < ci.NumPages(); i++ {
		if ci.NullPage(i) {
			continue
		}
		mn, mx := numericStat(ci.MinValue(i)), numericStat(ci.MaxValue(i))
		if math.IsNaN(mn) || math.IsNaN(mx) {
			return 0, 0, false
		}
		if !found {
			lo, hi, found = mn, mx, true
			continue
		}
		lo, hi = min(lo, mn), max(hi, mx)
	}
	return lo, hi, found
}

func numericStat(v parquet.Value) float64 {
	switch v.Kind() {
	case parquet.Int64:
		return float64(v.Int64())
	case parquet.Double:
		return v.Double()
	case parquet.Int32:
		return float64(v.Int32())
	case parquet.Float:
		return float64(v.Float())
	}
	return math.NaN()
}

func scanColumnsRowGroup(rg parquet.RowGroup, cols cheapCols, needs columnNeeds, base int64, sorted bool, b *cheapBatch, consume func(b *cheapBatch, n int, baseRow int64, sorted bool) bool) (bool, error) {
	chunks := rg.ColumnChunks()
	tp := chunks[cols.time].Pages()
	defer tp.Close()
	tc := &int64Cursor{pages: tp, vbuf: make([]parquet.Value, writeBatchRows)}
	lp := chunks[cols.level].Pages()
	defer lp.Close()
	lc := &valueCursor{pages: lp}
	var mc *valueCursor
	var vc, nc, ic, rc, sc *int32Cursor
	var qc *int64Cursor
	if needs.msg {
		mp := chunks[cols.msg].Pages()
		defer mp.Close()
		mc = &valueCursor{pages: mp}
	}
	if needs.ints {
		open32 := func(idx int) *int32Cursor {
			p := chunks[idx].Pages()
			return &int32Cursor{pages: p, vbuf: make([]parquet.Value, writeBatchRows)}
		}
		vc, nc, ic, rc, sc = open32(cols.version), open32(cols.node), open32(cols.instance), open32(cols.run), open32(cols.stream)
		defer vc.pages.Close()
		defer nc.pages.Close()
		defer ic.pages.Close()
		defer rc.pages.Close()
		defer sc.pages.Close()
		qp := chunks[cols.seq].Pages()
		defer qp.Close()
		qc = &int64Cursor{pages: qp, vbuf: make([]parquet.Value, writeBatchRows)}
	}
	dense := make([]*valueCursor, len(needs.fields))
	var spill [fieldTypes]*spillCursor
	for i, fn := range needs.fields {
		if fn.dense {
			p := chunks[cols.dense[i]].Pages()
			defer p.Close()
			dense[i] = &valueCursor{pages: p}
			continue
		}
		sp := spill[fn.typ]
		if sp == nil {
			kp := chunks[cols.spill[fn.typ].key].Pages()
			defer kp.Close()
			vp := chunks[cols.spill[fn.typ].val].Pages()
			defer vp.Close()
			sp = &spillCursor{keys: levelCursor{pages: kp}, vals: levelCursor{pages: vp}, wants: map[string]int{}}
			spill[fn.typ] = sp
		}
		sp.wants[fn.key] = i
	}
	fillValues := func(c *valueCursor, dst []parquet.Value, n int, name string) error {
		for got := 0; got < n; {
			k, err := c.read(dst[got:n])
			if err != nil {
				if errors.Is(err, io.EOF) {
					return fmt.Errorf("%s column shorter than time column", name)
				}
				return err
			}
			got += k
		}
		return nil
	}
	fillInt32 := func(c *int32Cursor, dst []int32, n int, name string) error {
		for got := 0; got < n; {
			k, err := c.read(dst[got:n])
			if err != nil {
				if errors.Is(err, io.EOF) {
					return fmt.Errorf("%s column shorter than time column", name)
				}
				return err
			}
			got += k
		}
		return nil
	}
	fillInt64 := func(c *int64Cursor, dst []int64, n int, name string) error {
		for got := 0; got < n; {
			k, err := c.read(dst[got:n])
			if err != nil {
				if errors.Is(err, io.EOF) {
					return fmt.Errorf("%s column shorter than time column", name)
				}
				return err
			}
			got += k
		}
		return nil
	}
	consumed := int64(0)
	for {
		n, err := tc.read(b.times)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
		if err := fillValues(lc, b.levels, n, "level"); err != nil {
			return false, err
		}
		if mc != nil {
			if err := fillValues(mc, b.msgs, n, "msg"); err != nil {
				return false, err
			}
		}
		if vc != nil {
			if err := fillInt32(vc, b.versions, n, "version"); err != nil {
				return false, err
			}
			if err := fillInt32(nc, b.nodes, n, "node"); err != nil {
				return false, err
			}
			if err := fillInt32(ic, b.instances, n, "instance_ordinal"); err != nil {
				return false, err
			}
			if err := fillInt32(rc, b.runs, n, "run"); err != nil {
				return false, err
			}
			if err := fillInt32(sc, b.streams, n, "stream"); err != nil {
				return false, err
			}
			if err := fillInt64(qc, b.seqs, n, "seq"); err != nil {
				return false, err
			}
		}
		for i, fn := range needs.fields {
			if fn.dense {
				if err := fillValues(dense[i], b.fields[i], n, denseColumnName(fn.key, fn.typ)); err != nil {
					return false, err
				}
				continue
			}
			clear(b.fields[i][:n])
		}
		for typ := range spill {
			if spill[typ] == nil {
				continue
			}
			if err := spill[typ].fillRows(b, n); err != nil {
				return false, fmt.Errorf("%s: %w", spillColNames[typ], err)
			}
		}
		if consume(b, n, base+consumed, sorted) {
			return true, nil
		}
		consumed += int64(n)
	}
}

var fetchColumnSetters = []struct {
	name string
	set  func(*logRow, parquet.Value)
}{
	{"time", func(r *logRow, v parquet.Value) { r.Time = v.Int64() }},
	{"version", func(r *logRow, v parquet.Value) { r.Version = v.Int32() }},
	{"run", func(r *logRow, v parquet.Value) { r.Run = v.Int32() }},
	{"node", func(r *logRow, v parquet.Value) { r.Node = v.Int32() }},
	{"instance_ordinal", func(r *logRow, v parquet.Value) { r.InstanceOrdinal = v.Int32() }},
	{"stream", func(r *logRow, v parquet.Value) { r.Stream = v.Int32() }},
	{"seq", func(r *logRow, v parquet.Value) { r.Seq = v.Int64() }},
	{"level", func(r *logRow, v parquet.Value) { r.Level = string(v.ByteArray()) }},
	{"msg", func(r *logRow, v parquet.Value) { r.Msg = string(v.ByteArray()) }},
	{"raw_message", func(r *logRow, v parquet.Value) { r.RawMessage = bytes.Clone(v.ByteArray()) }},
}

func fetchArchiveRows(path string, rowIdxs []int64) ([]logRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pf, err := openArchive(f)
	if err != nil {
		return nil, err
	}
	schema := pf.Schema()
	cols := make([]int, len(fetchColumnSetters))
	for i, s := range fetchColumnSetters {
		c, ok := schema.Lookup(s.name)
		if !ok {
			return nil, fmt.Errorf("missing column %s", s.name)
		}
		cols[i] = c.ColumnIndex
	}
	out := make([]logRow, len(rowIdxs))
	base := int64(0)
	ti := 0
	var vbuf [1]parquet.Value
	for _, rg := range pf.RowGroups() {
		nrows := rg.NumRows()
		if ti >= len(rowIdxs) {
			break
		}
		lo := ti
		for ti < len(rowIdxs) && rowIdxs[ti] < base+nrows {
			ti++
		}
		if lo == ti {
			base += nrows
			continue
		}
		chunks := rg.ColumnChunks()
		for ci := range fetchColumnSetters {
			if fc, ok := chunks[cols[ci]].(*parquet.FileColumnChunk); ok {
				if _, err := fc.OffsetIndex(); err != nil && !errors.Is(err, parquet.ErrMissingOffsetIndex) {
					return nil, err
				}
			}
			pages := chunks[cols[ci]].Pages()
			ferr := func() error {
				for k := lo; k < ti; k++ {
					if err := pages.SeekToRow(rowIdxs[k] - base); err != nil {
						return err
					}
					p, err := pages.ReadPage()
					if err != nil {
						return err
					}
					n, err := p.Values().ReadValues(vbuf[:])
					if n == 0 {
						if err != nil && !errors.Is(err, io.EOF) {
							return err
						}
						return fmt.Errorf("row %d out of range", rowIdxs[k])
					}
					fetchColumnSetters[ci].set(&out[k], vbuf[0])
				}
				return nil
			}()
			cerr := pages.Close()
			if ferr != nil {
				return nil, ferr
			}
			if cerr != nil {
				return nil, cerr
			}
		}
		base += nrows
	}
	if ti != len(rowIdxs) {
		return nil, fmt.Errorf("rows out of range: located %d of %d", ti, len(rowIdxs))
	}
	return out, nil
}

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

type int32Cursor struct {
	pages parquet.Pages
	ir    parquet.Int32Reader
	vr    parquet.ValueReader
	vbuf  []parquet.Value
}

func (c *int32Cursor) read(buf []int32) (int, error) {
	for {
		if c.ir != nil {
			n, err := c.ir.ReadInt32s(buf)
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
				buf[i] = c.vbuf[i].Int32()
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
		if ir, ok := vals.(parquet.Int32Reader); ok {
			c.ir, c.vr = ir, nil
		} else {
			c.ir, c.vr = nil, vals
		}
	}
}

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

type levelCursor struct {
	pages parquet.Pages
	vr    parquet.ValueReader
	buf   [256]parquet.Value
	pos   int
	n     int
	eof   bool
}

func (c *levelCursor) fill() error {
	for !c.eof {
		if c.vr != nil {
			n, err := c.vr.ReadValues(c.buf[:])
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if errors.Is(err, io.EOF) {
				c.vr = nil
			}
			if n > 0 {
				c.pos, c.n = 0, n
				return nil
			}
			continue
		}
		p, err := c.pages.ReadPage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.eof = true
				return nil
			}
			return err
		}
		c.vr = p.Values()
	}
	return nil
}

func (c *levelCursor) peek() (parquet.Value, bool, error) {
	if c.pos >= c.n {
		if err := c.fill(); err != nil {
			return parquet.Value{}, false, err
		}
		if c.pos >= c.n {
			return parquet.Value{}, false, nil
		}
	}
	return c.buf[c.pos], true, nil
}

func (c *levelCursor) next() (parquet.Value, bool, error) {
	v, ok, err := c.peek()
	if ok {
		c.pos++
	}
	return v, ok, err
}

type spillCursor struct {
	keys  levelCursor
	vals  levelCursor
	wants map[string]int
}

func (c *spillCursor) fillRows(b *cheapBatch, n int) error {
	rows := 0
	for {
		kv, ok, err := c.keys.peek()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if kv.RepetitionLevel() == 0 {
			if rows == n {
				break
			}
			rows++
		}
		c.keys.next()
		vv, vok, err := c.vals.next()
		if err != nil {
			return err
		}
		if !vok {
			return errors.New("map value column shorter than key column")
		}
		if kv.IsNull() {
			continue
		}
		if idx, want := c.wants[bstr(kv.ByteArray())]; want {
			b.fields[idx][rows-1] = vv
		}
	}
	if rows != n {
		return fmt.Errorf("map column has %d rows, want %d", rows, n)
	}
	return nil
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
		pf, err := openArchive(f)
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
		pf, err := openArchive(f)
		if err != nil {
			yield(logRow{}, err)
			return
		}
		r := parquet.NewGenericReader[logRow](pf)
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
