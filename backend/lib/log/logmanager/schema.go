package logmanager

import (
	"cmp"
	"slices"

	"github.com/parquet-go/parquet-go"
)

var (
	denseLeafBudget = 400
	maxTallyKeys    = 4096
)

const (
	placementDense = 0
	placementSpill = 1
	denseColPrefix = "f_"
	spillColPrefix = "spill_"
)

var spillColNames = [fieldTypes]string{"spill_int", "spill_float", "spill_bool", "spill_str"}

func denseColumnName(key string, typ fieldType) string {
	return denseColPrefix + key + "__" + fieldTypeSuffix[typ]
}

type keyCount struct {
	key  string
	rows [fieldTypes]int64
}

func (k *keyCount) total() int64 {
	var t int64
	for _, n := range k.rows {
		t += n
	}
	return t
}

func (k *keyCount) variants() int {
	n := 0
	for _, r := range k.rows {
		if r > 0 {
			n++
		}
	}
	return n
}

type keyTally struct {
	keys   map[string]*keyCount
	order  []*keyCount
	limit  int
	capped bool
}

func newKeyTally(limit int) *keyTally {
	return &keyTally{keys: map[string]*keyCount{}, limit: limit}
}

func (t *keyTally) add(fields []shredField) {
	for i := range fields {
		f := &fields[i]
		kc := t.keys[bstr(f.key)]
		if kc == nil {
			if t.limit > 0 && len(t.keys) >= t.limit {
				t.capped = true
				continue
			}
			kc = &keyCount{key: string(f.key)}
			t.keys[kc.key] = kc
			t.order = append(t.order, kc)
		}
		kc.rows[f.val.typ]++
	}
}

type keyVariant struct {
	key  string
	typ  fieldType
	rows int64
}

func (t *keyTally) planDense(budget int) []keyVariant {
	ranked := slices.Clone(t.order)
	slices.SortFunc(ranked, func(a, b *keyCount) int {
		if ta, tb := a.total(), b.total(); ta != tb {
			return cmp.Compare(tb, ta)
		}
		return cmp.Compare(a.key, b.key)
	})
	var dense []keyVariant
	used := 0
	for _, kc := range ranked {
		n := kc.variants()
		if n == 0 || used+n > budget {
			continue
		}
		used += n
		for typ := fieldType(0); typ < fieldTypes; typ++ {
			if kc.rows[typ] > 0 {
				dense = append(dense, keyVariant{key: kc.key, typ: typ, rows: kc.rows[typ]})
			}
		}
	}
	slices.SortFunc(dense, cmpKeyVariant)
	return dense
}

func cmpKeyVariant(a, b keyVariant) int {
	if a.key != b.key {
		return cmp.Compare(a.key, b.key)
	}
	return cmp.Compare(a.typ, b.typ)
}

func (t *keyTally) variants() []keyVariant {
	var out []keyVariant
	for _, kc := range t.order {
		for typ := fieldType(0); typ < fieldTypes; typ++ {
			if kc.rows[typ] > 0 {
				out = append(out, keyVariant{key: kc.key, typ: typ, rows: kc.rows[typ]})
			}
		}
	}
	slices.SortFunc(out, cmpKeyVariant)
	return out
}

type denseCol struct {
	key string
	typ fieldType
	col int
}

type spillCols struct {
	key, val int
}

type archiveSchema struct {
	schema   *parquet.Schema
	ncols    int
	time     int
	version  int
	run      int
	node     int
	instance int
	stream   int
	seq      int
	level    int
	msg      int
	raw      int
	dense    []denseCol
	denseIdx map[string][fieldTypes]int32
	spill    [fieldTypes]spillCols
}

func typeNode(typ fieldType) parquet.Node {
	switch typ {
	case typeInt:
		return parquet.Int(64)
	case typeFloat:
		return parquet.Leaf(parquet.DoubleType)
	case typeBool:
		return parquet.Leaf(parquet.BooleanType)
	default:
		return parquet.String()
	}
}

func buildArchiveSchema(dense []keyVariant) *archiveSchema {
	g := parquet.Group{
		"time":             parquet.Int(64),
		"version":          parquet.Int(32),
		"run":              parquet.Int(32),
		"node":             parquet.Int(32),
		"instance_ordinal": parquet.Int(32),
		"stream":           parquet.Int(32),
		"seq":              parquet.Int(64),
		"level":            parquet.Optional(parquet.String()),
		"msg":              parquet.Optional(parquet.String()),
		"raw_message":      parquet.Leaf(parquet.ByteArrayType),
	}
	for _, d := range dense {
		g[denseColumnName(d.key, d.typ)] = parquet.Optional(typeNode(d.typ))
	}
	for typ := fieldType(0); typ < fieldTypes; typ++ {
		g[spillColNames[typ]] = parquet.Map(parquet.String(), typeNode(typ))
	}
	s := parquet.NewSchema("log", g)
	col := func(path ...string) int {
		lc, ok := s.Lookup(path...)
		if !ok {
			panic("archive schema missing column " + path[0])
		}
		return lc.ColumnIndex
	}
	as := &archiveSchema{
		schema:   s,
		ncols:    len(s.Columns()),
		time:     col("time"),
		version:  col("version"),
		run:      col("run"),
		node:     col("node"),
		instance: col("instance_ordinal"),
		stream:   col("stream"),
		seq:      col("seq"),
		level:    col("level"),
		msg:      col("msg"),
		raw:      col("raw_message"),
		denseIdx: make(map[string][fieldTypes]int32, len(dense)),
	}
	for _, d := range dense {
		c := col(denseColumnName(d.key, d.typ))
		as.dense = append(as.dense, denseCol{key: d.key, typ: d.typ, col: c})
		idx, ok := as.denseIdx[d.key]
		if !ok {
			idx = [fieldTypes]int32{-1, -1, -1, -1}
		}
		idx[d.typ] = int32(c)
		as.denseIdx[d.key] = idx
	}
	for typ := fieldType(0); typ < fieldTypes; typ++ {
		as.spill[typ] = spillCols{
			key: col(spillColNames[typ], "key_value", "key"),
			val: col(spillColNames[typ], "key_value", "value"),
		}
	}
	return as
}

func (as *archiveSchema) denseVariants() []keyVariant {
	out := make([]keyVariant, 0, len(as.dense))
	for _, d := range as.dense {
		out = append(out, keyVariant{key: d.key, typ: d.typ})
	}
	return out
}

const arenaChunk = 1 << 20

type arena struct {
	chunks [][]byte
	cur    []byte
	next   int
}

func (a *arena) copy(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if len(b) > arenaChunk {
		c := slices.Clone(b)
		a.chunks = append(a.chunks, c)
		return c
	}
	if cap(a.cur)-len(a.cur) < len(b) {
		if a.next < len(a.chunks) {
			a.cur = a.chunks[a.next][:0]
		} else {
			a.cur = make([]byte, 0, arenaChunk)
			a.chunks = append(a.chunks, a.cur)
		}
		a.next++
	}
	at := len(a.cur)
	a.cur = append(a.cur, b...)
	return a.cur[at:len(a.cur):len(a.cur)]
}

func (a *arena) reset() {
	kept := a.chunks[:0]
	for _, c := range a.chunks {
		if cap(c) == arenaChunk {
			kept = append(kept, c[:0])
		}
	}
	a.chunks = kept
	a.next = 0
	a.cur = nil
}

type rowBuilder struct {
	as     *archiveSchema
	cols   [][]parquet.Value
	arena  arena
	rows   []parquet.Row
	values int
}

func newRowBuilder(as *archiveSchema) *rowBuilder {
	return &rowBuilder{as: as, cols: make([][]parquet.Value, as.ncols)}
}

func (rb *rowBuilder) add(time int64, version, run, node, instance, stream int32, seq int64, level, msg string, raw []byte, fields []shredField) {
	as := rb.as
	for c := range rb.cols {
		rb.cols[c] = rb.cols[c][:0]
	}
	set := func(c int, v parquet.Value) { rb.cols[c] = append(rb.cols[c], v.Level(0, 0, c)) }
	set(as.time, parquet.Int64Value(time))
	set(as.version, parquet.Int32Value(version))
	set(as.run, parquet.Int32Value(run))
	set(as.node, parquet.Int32Value(node))
	set(as.instance, parquet.Int32Value(instance))
	set(as.stream, parquet.Int32Value(stream))
	set(as.seq, parquet.Int64Value(seq))
	set(as.raw, parquet.ByteArrayValue(rb.arena.copy(raw)))
	optStr := func(c int, s string) {
		if s == "" {
			rb.cols[c] = append(rb.cols[c], parquet.NullValue().Level(0, 0, c))
			return
		}
		rb.cols[c] = append(rb.cols[c], parquet.ByteArrayValue(rb.arena.copy(strb(s))).Level(0, 1, c))
	}
	optStr(as.level, level)
	optStr(as.msg, msg)
	for _, d := range as.dense {
		rb.cols[d.col] = append(rb.cols[d.col], parquet.NullValue().Level(0, 0, d.col))
	}
	for i := range fields {
		f := &fields[i]
		var pv parquet.Value
		switch f.val.typ {
		case typeInt:
			pv = parquet.Int64Value(f.val.i)
		case typeFloat:
			pv = parquet.DoubleValue(f.val.f)
		case typeBool:
			pv = parquet.BooleanValue(f.val.b)
		default:
			pv = parquet.ByteArrayValue(rb.arena.copy(f.val.text))
		}
		if idx, ok := as.denseIdx[bstr(f.key)]; ok && idx[f.val.typ] >= 0 {
			c := int(idx[f.val.typ])
			rb.cols[c][0] = pv.Level(0, 1, c)
			continue
		}
		sc := as.spill[f.val.typ]
		rep := 1
		if len(rb.cols[sc.key]) == 0 {
			rep = 0
		}
		rb.cols[sc.key] = append(rb.cols[sc.key], parquet.ByteArrayValue(rb.arena.copy(f.key)).Level(rep, 1, sc.key))
		rb.cols[sc.val] = append(rb.cols[sc.val], pv.Level(rep, 1, sc.val))
	}
	for typ := fieldType(0); typ < fieldTypes; typ++ {
		sc := as.spill[typ]
		if len(rb.cols[sc.key]) == 0 {
			rb.cols[sc.key] = append(rb.cols[sc.key], parquet.NullValue().Level(0, 0, sc.key))
			rb.cols[sc.val] = append(rb.cols[sc.val], parquet.NullValue().Level(0, 0, sc.val))
		}
	}
	total := 0
	for c := range rb.cols {
		total += len(rb.cols[c])
	}
	row := make(parquet.Row, 0, total)
	for c := range rb.cols {
		row = append(row, rb.cols[c]...)
	}
	rb.rows = append(rb.rows, row)
	rb.values += total
}

func (rb *rowBuilder) reset() {
	rb.rows = rb.rows[:0]
	rb.values = 0
	rb.arena.reset()
}
