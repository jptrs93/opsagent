package logmanager

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
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
	field      string
	op         string
	value      string // pre-lowercased for contains ops
	valueBytes []byte // value as bytes for the allocation-free fold
	asciiValue bool   // value is pure ASCII, so the byte fold is exact
	values     []string
	origValue  string
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
		value := strings.ToLower(f.Value)
		out = append(out, compiledFilter{
			field:      f.Field,
			op:         f.Op,
			value:      value,
			valueBytes: []byte(value),
			asciiValue: isASCII(value),
			values:     f.Values,
			origValue:  f.Value,
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

func filtersColumnSafe(fs []compiledFilter) bool {
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

// visitRec is one record as seen by the query scan. level/msg come from the
// parquet columns when shredded is set; otherwise they are resolved through
// the scanner's lineView first and parseLine only when the view declines.
// The full fields map is parsed lazily so records that are only counted or
// filtered never pay for a map decode.
type visitRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   map[string]string
	shredded bool // level and msg are authoritative
	parsed   bool // fields were materialised by parseLine

	sc      *lineScanner // shared by the scan loop; allocated on demand otherwise
	view    lineView
	viewed  bool
	levelOK bool // level resolved from the view
	msgOK   bool // msg resolved from the view
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

func (v *visitRec) msgValue() string {
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

// fieldBytes is the allocation-free form of fieldValue for values that sit
// on the line as bytes: the message and shredded JSON scalars of a record
// that has not been parsed or shredded yet. fast is false when the caller
// must use fieldValue instead, which already holds a string for those cases.
// The slice is valid until the next scanner call.
func (v *visitRec) fieldBytes(field string) (b []byte, ok bool, fast bool) {
	switch field {
	case "", "msg", "message":
		if v.shredded || v.msgOK {
			return nil, false, false
		}
		view := v.ensureView()
		b, ok = v.sc.msgBytes(view)
		return b, true, ok
	case "level", "version", "node", "run", "instance", "stream":
		return nil, false, false
	}
	if v.parsed {
		return nil, false, false
	}
	view := v.ensureView()
	if view.fallback {
		return nil, false, false
	}
	if !view.isJSON {
		return nil, false, true
	}
	sp, found := view.lookup(field)
	if !found {
		return nil, false, true
	}
	b, ok = v.sc.valueBytes(view, sp)
	return b, true, ok
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
		if !v.parsed {
			if view := v.ensureView(); !view.fallback {
				if !view.isJSON {
					return "", false
				}
				sp, found := view.lookup(field)
				if !found {
					return "", false
				}
				if b, ok := v.sc.valueBytes(view, sp); ok {
					return string(b), true
				}
			}
		}
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
	if f.op == "contains" || f.op == "not_contains" {
		if matched, fast := f.matchContainsBytes(rec); fast {
			return matched
		}
	}
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
		return ok && f.containsValue(rec, v)
	case "not_contains":
		return !(ok && f.containsValue(rec, v))
	}
	return false
}

// containsValue is the contains test over a value already fetched as a
// string. The ASCII fold runs without allocating; anything else takes the
// strings.ToLower path so Unicode case rules stay exactly as before.
func (f *compiledFilter) containsValue(rec *visitRec, v string) bool {
	if f.asciiValue {
		if found, ok := containsFold(&rec.scanner().lower, v, f.valueBytes); ok {
			return found
		}
	}
	return strings.Contains(strings.ToLower(v), f.value)
}

// matchContainsBytes evaluates a contains filter against the raw field bytes
// when the record can supply them, so the message never becomes a string
// just to be searched. fast is false when the caller should fall through to
// match.
func (f *compiledFilter) matchContainsBytes(rec *visitRec) (matched, fast bool) {
	if !f.asciiValue {
		return false, false
	}
	b, ok, fast := rec.fieldBytes(f.field)
	if !fast {
		return false, false
	}
	found := false
	if ok {
		var folded bool
		if found, folded = containsFold(&rec.scanner().lower, b, f.valueBytes); !folded {
			return false, false
		}
	}
	if f.op == "not_contains" {
		return !found, true
	}
	return found, true
}
