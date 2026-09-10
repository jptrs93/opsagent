package logmanager

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unsafe"
)

type fieldType uint8

const (
	typeInt fieldType = iota
	typeFloat
	typeBool
	typeStr
	fieldTypes           = 4
	typeNull   fieldType = 255
)

const (
	maxFlattenDepth = 8
	maxKeyLen       = 256
)

var fieldTypeSuffix = [fieldTypes]string{"i", "f", "b", "s"}

type fieldValue struct {
	typ  fieldType
	i    int64
	f    float64
	b    bool
	text []byte
}

type shredField struct {
	key []byte
	val fieldValue
}

func bstr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

func strb(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

func (v fieldValue) textBytes(scratch *[]byte) []byte {
	if v.text != nil {
		return v.text
	}
	switch v.typ {
	case typeInt:
		*scratch = strconv.AppendInt((*scratch)[:0], v.i, 10)
	case typeFloat:
		*scratch = strconv.AppendFloat((*scratch)[:0], v.f, 'g', -1, 64)
	case typeBool:
		if v.b {
			return trueBytes
		}
		return falseBytes
	default:
		return nil
	}
	return *scratch
}

func (v fieldValue) String() string {
	var scratch []byte
	return string(v.textBytes(&scratch))
}

func (v fieldValue) owned() fieldValue {
	v.text = bytes.Clone(v.text)
	return v
}

func (v fieldValue) asFloat() float64 {
	if v.typ == typeInt {
		return float64(v.i)
	}
	return v.f
}

func classifyNumber(text []byte) fieldValue {
	isFloat := false
	for _, c := range text {
		if c == '.' || c == 'e' || c == 'E' {
			isFloat = true
			break
		}
	}
	if !isFloat {
		if i, ok := parseInt64(text); ok {
			return fieldValue{typ: typeInt, i: i, text: text}
		}
	}
	f, _ := strconv.ParseFloat(bstr(text), 64)
	return fieldValue{typ: typeFloat, f: f, text: text}
}

func parseInt64(text []byte) (int64, bool) {
	if len(text) == 0 {
		return 0, false
	}
	neg := false
	i := 0
	if text[0] == '-' {
		neg = true
		i = 1
	} else if text[0] == '+' {
		i = 1
	}
	if i >= len(text) {
		return 0, false
	}
	var n uint64
	for ; i < len(text); i++ {
		c := text[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (1<<63)/10 {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
		if n > 1<<63 {
			return 0, false
		}
	}
	if neg {
		if n > 1<<63 {
			return 0, false
		}
		return int64(-n), true
	}
	if n > 1<<63-1 {
		return 0, false
	}
	return int64(n), true
}

func stringValue(text []byte) fieldValue {
	return fieldValue{typ: typeStr, text: text}
}

var (
	trueBytes  = []byte("true")
	falseBytes = []byte("false")
)

func boolValue(b bool) fieldValue {
	if b {
		return fieldValue{typ: typeBool, b: true, text: trueBytes}
	}
	return fieldValue{typ: typeBool, b: false, text: falseBytes}
}

func isLiftedKey(key []byte) bool {
	switch bstr(key) {
	case "level", "msg", "message":
		return true
	}
	return false
}

func parseLine(line []byte) (level, msg string, fields []shredField) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return "", string(line), nil
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	var top json.RawMessage
	if dec.Decode(&top) != nil || len(top) == 0 || top[0] != '{' {
		return "", string(line), nil
	}
	p := lineParser{}
	if err := p.object(top, nil, 1); err != nil {
		return "", string(line), nil
	}
	level = normalizeLevel(p.level)
	if p.hasMsg {
		msg = p.msg
	} else {
		msg = p.message
	}
	return level, msg, dedupeFields(p.fields)
}

type lineParser struct {
	level   string
	msg     string
	hasMsg  bool
	message string
	fields  []shredField
}

func (p *lineParser) object(raw []byte, prefix []byte, depth int) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errBadLine
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return err
		}
		if len(val) == 0 {
			return errBadLine
		}
		if depth == 1 && (key == "level" || key == "msg" || key == "message") {
			text, present := scalarText(val)
			switch key {
			case "level":
				if present {
					p.level = text
				} else {
					p.level = ""
				}
			case "msg":
				p.hasMsg = present
				p.msg = text
			case "message":
				p.message = text
			}
			continue
		}
		full := make([]byte, 0, len(prefix)+1+len(key))
		if depth > 1 {
			full = append(full, prefix...)
			full = append(full, '.')
		}
		full = append(full, key...)
		if len(full) > maxKeyLen {
			continue
		}
		switch val[0] {
		case '{':
			if depth < maxFlattenDepth {
				if err := p.object(val, full, depth+1); err != nil {
					return err
				}
				continue
			}
			p.fields = append(p.fields, shredField{key: full, val: stringValue(bytes.Clone(val))})
		case 'n':
			p.fields = append(p.fields, shredField{key: full, val: fieldValue{typ: typeNull}})
		default:
			text, _ := scalarText(val)
			p.fields = append(p.fields, shredField{key: full, val: typedFromRaw(val, text)})
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

func typedFromRaw(raw []byte, text string) fieldValue {
	switch raw[0] {
	case '"':
		return stringValue([]byte(text))
	case 't':
		return boolValue(true)
	case 'f':
		return boolValue(false)
	case '[', '{':
		return stringValue(bytes.Clone(raw))
	default:
		return classifyNumber(bytes.Clone(raw))
	}
}

func scalarText(raw []byte) (string, bool) {
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case 'n':
		return "", false
	default:
		return string(raw), true
	}
}

type badLine struct{}

func (badLine) Error() string { return "malformed log line" }

var errBadLine = badLine{}

func dedupeFields(fields []shredField) []shredField {
	if len(fields) == 0 {
		return fields
	}
	seen := make(map[string]struct{}, len(fields))
	out := make([]shredField, 0, len(fields))
	for i := len(fields) - 1; i >= 0; i-- {
		k := bstr(fields[i].key)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		if fields[i].val.typ == typeNull {
			continue
		}
		out = append(out, fields[i])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func lookupField(fields []shredField, key string) (fieldValue, bool) {
	for i := range fields {
		if bstr(fields[i].key) == key {
			return fields[i].val, true
		}
	}
	return fieldValue{}, false
}

func fieldsToDisplay(fields []shredField) map[string]string {
	if fields == nil {
		return nil
	}
	out := make(map[string]string, len(fields))
	for i := range fields {
		out[string(fields[i].key)] = fields[i].val.String()
	}
	return out
}

func jsonArrayElements(v []byte) []string {
	if len(v) == 0 || v[0] != '[' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(v))
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
