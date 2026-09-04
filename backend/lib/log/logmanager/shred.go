package logmanager

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// parseLine is the single parse function shared by the commit path and the
// query path, so a filtered query returns the same result for a line before
// and after it is committed to parquet. JSON objects are shredded one level
// deep into flat string fields; anything else lands whole in msg with no
// level and nil fields.
func parseLine(line []byte) (level, msg string, fields map[string]string) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return "", string(line), nil
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) != nil {
		return "", string(line), nil
	}
	var message string
	hasMsg := false
	fields = make(map[string]string, len(obj))
	for k, v := range obj {
		switch k {
		case "level":
			level = normalizeLevel(jsonValueString(v))
		case "msg":
			msg = jsonValueString(v)
			hasMsg = true
		case "message":
			message = jsonValueString(v)
		default:
			fields[k] = jsonValueString(v)
		}
	}
	if !hasMsg {
		msg = message
	}
	return level, msg, fields
}

// jsonArrayElements returns the elements of v rendered as strings when v is
// the shredded form of a JSON array (parseLine keeps arrays as their raw JSON
// text), or nil for any scalar value. Multi-valued fields like _tags are
// matched and tallied per element rather than as one opaque list string.
func jsonArrayElements(v string) []string {
	if len(v) == 0 || v[0] != '[' {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(v))
	dec.UseNumber()
	var arr []any
	if dec.Decode(&arr) != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		out = append(out, jsonValueString(e))
	}
	return out
}

func jsonValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func normalizeLevel(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// shredFields extracts the level and msg columns for the parquet commit path
// through the scanner's view, with parseLine as the fallback authority.
func shredFields(sc *lineScanner, r apigen.RawLogLine) (level string, msg string) {
	return sc.shred(r.Line)
}
