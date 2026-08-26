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
	field     string
	op        string
	value     string // pre-lowercased for contains ops
	values    []string
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
// parquet columns when shredded is set and from parseLine otherwise; fields
// are parsed lazily so records that are only counted never pay for JSON
// parsing.
type visitRec struct {
	rec      apigen.RawLogLine
	level    string
	msg      string
	fields   map[string]string
	shredded bool
	parsed   bool
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
