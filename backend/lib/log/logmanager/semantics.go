package logmanager

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var levelOrder = []string{"ERROR", "WARN", "INFO", "DEBUG", ""}

func levelIndex(level string) int {
	for i, l := range levelOrder {
		if l == level {
			return i
		}
	}
	return len(levelOrder) - 1
}

type literal struct {
	text   string
	bytes  []byte
	lower  []byte
	ascii  bool
	num    bool
	isInt  bool
	i      int64
	f      float64
	isBool bool
	b      bool
}

func parseLiteral(s string, textOnly bool) literal {
	lower := strings.ToLower(s)
	l := literal{text: s, bytes: []byte(s), lower: []byte(lower), ascii: isASCII(lower)}
	if textOnly {
		return l
	}
	t := strings.TrimSpace(s)
	if i, ok := parseInt64(strb(t)); ok {
		l.num, l.isInt, l.i = true, true, i
		return l
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		l.num, l.f = true, f
		return l
	}
	switch lower {
	case "true":
		l.isBool, l.b = true, true
	case "false":
		l.isBool, l.b = true, false
	}
	return l
}

func (l *literal) asFloat() float64 {
	if l.isInt {
		return float64(l.i)
	}
	return l.f
}

func (l *literal) equals(v fieldValue) bool {
	switch v.typ {
	case typeInt:
		if !l.num {
			return false
		}
		if l.isInt {
			return v.i == l.i
		}
		return float64(v.i) == l.f
	case typeFloat:
		return l.num && v.f == l.asFloat()
	case typeBool:
		return l.isBool && v.b == l.b
	default:
		if bytes.EqualFold(v.text, l.bytes) {
			return true
		}
		for _, e := range jsonArrayElements(v.text) {
			if strings.EqualFold(e, l.text) {
				return true
			}
		}
		return false
	}
}

func (l *literal) compare(v fieldValue, op string) bool {
	if !l.num {
		return false
	}
	var c int
	switch v.typ {
	case typeInt:
		if l.isInt {
			c = cmp.Compare(v.i, l.i)
		} else {
			c = cmp.Compare(float64(v.i), l.f)
		}
	case typeFloat:
		c = cmp.Compare(v.f, l.asFloat())
	default:
		return false
	}
	switch op {
	case "gt":
		return c > 0
	case "gte":
		return c >= 0
	case "lt":
		return c < 0
	case "lte":
		return c <= 0
	}
	return false
}

func (l *literal) contains(v fieldValue, sc *lineScanner) bool {
	text := v.textBytes(&sc.fmtBuf)
	if l.ascii {
		if found, ok := containsFold(&sc.lower, text, l.lower); ok {
			return found
		}
	}
	return strings.Contains(strings.ToLower(bstr(text)), bstr(l.lower))
}

type compiledFilter struct {
	field string
	op    string
	lit   literal
	lits  []literal
}

func isRangeOp(op string) bool {
	switch op {
	case "gt", "gte", "lt", "lte":
		return true
	}
	return false
}

func compileFilters(fs []*apigen.LogFilter) ([]compiledFilter, error) {
	out := make([]compiledFilter, 0, len(fs))
	for _, f := range fs {
		if f == nil {
			continue
		}
		switch f.Op {
		case "eq", "neq", "in", "exists", "not_exists", "contains", "not_contains", "gt", "gte", "lt", "lte":
		default:
			return nil, apigen.NewApiErr(fmt.Sprintf("Unknown filter op %q", f.Op), "invalid_filter", http.StatusBadRequest)
		}
		c := compiledFilter{field: f.Field, op: f.Op, lit: parseLiteral(f.Value, f.Text)}
		if isRangeOp(f.Op) && !c.lit.num {
			return nil, apigen.NewApiErr(fmt.Sprintf("Filter op %q needs a numeric value, got %q", f.Op, f.Value), "invalid_filter", http.StatusBadRequest)
		}
		if f.Op == "in" {
			c.lits = make([]literal, 0, len(f.Values))
			for _, v := range f.Values {
				c.lits = append(c.lits, parseLiteral(v, f.Text))
			}
		}
		out = append(out, c)
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

func isColumnFieldName(field string) bool {
	switch field {
	case "", "msg", "message", "level":
		return true
	}
	return isMetaFieldName(field)
}

var (
	stdoutBytes = []byte("stdout")
	stderrBytes = []byte("stderr")
)

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

func streamValue(stream int32) fieldValue {
	switch stream {
	case 0:
		return stringValue(stdoutBytes)
	case 1:
		return stringValue(stderrBytes)
	default:
		return stringValue([]byte(strconv.Itoa(int(stream))))
	}
}

func fieldFilterIdx(fs []compiledFilter) []int {
	var out []int
	for i := range fs {
		if !isColumnFieldName(fs[i].field) {
			out = append(out, i)
		}
	}
	return out
}

func filtersLevelOnly(fs []compiledFilter) bool {
	for i := range fs {
		if fs[i].field != "level" {
			return false
		}
	}
	return true
}

func filtersNeedMsg(fs []compiledFilter) bool {
	for i := range fs {
		switch fs[i].field {
		case "", "msg", "message":
			return true
		}
	}
	return false
}

func filtersReferenceMeta(fs []compiledFilter) bool {
	for i := range fs {
		if isMetaFieldName(fs[i].field) {
			return true
		}
	}
	return false
}

type visitRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   []shredField
	shredded bool
	parsed   bool

	sc      *lineScanner
	view    lineView
	viewed  bool
	levelOK bool
	msgOK   bool

	msgRaw    []byte
	hasMsgRaw bool
}

func (v *visitRec) scanner() *lineScanner {
	if v.sc == nil {
		v.sc = &lineScanner{}
	}
	return v.sc
}

func (v *visitRec) ensureView() *lineView {
	if !v.viewed {
		v.view = v.scanner().view(v.rec.Line)
		v.viewed = true
	}
	return &v.view
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
	if v.shredded || v.levelOK {
		return v.level
	}
	view := v.ensureView()
	if lvl, ok := v.sc.levelFrom(view); ok {
		v.level, v.levelOK = lvl, true
		return lvl
	}
	v.ensureParsed()
	return v.level
}

func (v *visitRec) msgBytes() []byte {
	if v.hasMsgRaw {
		return v.msgRaw
	}
	if v.shredded || v.msgOK {
		return strb(v.msg)
	}
	view := v.ensureView()
	if b, ok := v.sc.msgBytes(view); ok {
		return b
	}
	v.ensureParsed()
	return strb(v.msg)
}

func (v *visitRec) msgValue() string {
	if v.hasMsgRaw && !v.msgOK {
		v.msg, v.msgOK = string(v.msgRaw), true
	}
	if v.shredded || v.msgOK {
		return v.msg
	}
	view := v.ensureView()
	if msg, ok := v.sc.msgFrom(view); ok {
		v.msg, v.msgOK = msg, true
		return msg
	}
	v.ensureParsed()
	return v.msg
}

func (v *visitRec) typedField(field string) (fieldValue, bool) {
	switch field {
	case "", "msg", "message":
		return stringValue(v.msgBytes()), true
	case "level":
		l := v.levelValue()
		return stringValue(strb(l)), l != ""
	case "version":
		return fieldValue{typ: typeInt, i: int64(v.rec.Version)}, true
	case "node":
		return fieldValue{typ: typeInt, i: int64(v.rec.Node)}, true
	case "run":
		return fieldValue{typ: typeInt, i: int64(v.rec.Run)}, true
	case "instance":
		return fieldValue{typ: typeInt, i: int64(v.rec.InstanceOrdinal)}, true
	case "stream":
		return streamValue(v.rec.Stream), true
	default:
		if !v.parsed {
			if view := v.ensureView(); !view.fallback {
				if !view.isJSON {
					return fieldValue{}, false
				}
				return v.sc.typedField(view, field)
			}
		}
		v.ensureParsed()
		return lookupField(v.fields, field)
	}
}

func (f *compiledFilter) match(rec *visitRec) bool {
	v, ok := rec.typedField(f.field)
	return f.matchValue(v, ok, rec.scanner())
}

func (f *compiledFilter) matchValue(v fieldValue, ok bool, sc *lineScanner) bool {
	switch f.op {
	case "exists":
		return ok
	case "not_exists":
		return !ok
	case "eq":
		return ok && f.lit.equals(v)
	case "neq":
		return !(ok && f.lit.equals(v))
	case "in":
		if !ok {
			v = fieldValue{typ: typeStr}
		}
		for i := range f.lits {
			if f.lits[i].equals(v) {
				return true
			}
		}
		return false
	case "contains":
		return ok && f.lit.contains(v, sc)
	case "not_contains":
		return !(ok && f.lit.contains(v, sc))
	case "gt", "gte", "lt", "lte":
		return ok && f.lit.compare(v, f.op)
	}
	return false
}
