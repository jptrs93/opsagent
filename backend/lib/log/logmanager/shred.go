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

// normalizeLevel maps the level spellings in the wild onto the four canonical
// severities. Anything unrecognized is treated as no level.
func normalizeLevel(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE", "TRC", "DBG", "DEBUG":
		return "DEBUG"
	case "INFO", "INF":
		return "INFO"
	case "WARN", "WARNING", "WRN":
		return "WARN"
	case "ERROR", "ERR", "FATAL", "PANIC", "CRIT", "CRITICAL":
		return "ERROR"
	default:
		return ""
	}
}

// shredFields extracts the level and msg columns for the parquet commit path.
func shredFields(r apigen.RawLogLine) (level string, msg string) {
	level, msg, _ = parseLine(r.Line)
	return level, msg
}
