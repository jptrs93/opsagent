package logmanager

import (
	"bytes"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

type lineView struct {
	line     []byte
	spans    []kvSpan
	isJSON   bool
	fallback bool
}

type jsonKind uint8

const (
	kindString jsonKind = iota + 1
	kindNumber
	kindTrue
	kindFalse
	kindNull
	kindObject
	kindArray
)

type span struct{ start, end int32 }

type kvSpan struct {
	key  span
	val  span
	kind jsonKind
	esc  bool
	high bool
	top  bool
}

const (
	strFlagEsc  = 1
	strFlagHigh = 2

	maxViewDepth   = 32
	maxLevelIntern = 64
)

type internedLevel struct {
	raw  []byte
	norm string
}

type lineScanner struct {
	spans   []kvSpan
	keyBuf  []byte
	levels  []internedLevel
	scratch []byte
	lower   []byte
	fmtBuf  []byte
	fields  []shredField
}

func (s *lineScanner) view(raw []byte) lineView {
	line := bytes.TrimSpace(raw)
	v := lineView{line: line}
	if len(line) == 0 || line[0] != '{' {
		return v
	}
	s.spans = s.spans[:0]
	s.keyBuf = s.keyBuf[:0]
	if _, ok := s.scanObjectFlat(line, 1, 1, span{}); !ok {
		v.fallback = true
		return v
	}
	v.spans = s.spans
	v.isJSON = true
	return v
}

func (s *lineScanner) keyOf(sp kvSpan) []byte {
	return s.keyBuf[sp.key.start:sp.key.end]
}

func (s *lineScanner) lookup(v *lineView, key string) (kvSpan, bool) {
	for i := len(v.spans) - 1; i >= 0; i-- {
		sp := v.spans[i]
		if bstr(s.keyBuf[sp.key.start:sp.key.end]) == key {
			return sp, true
		}
	}
	return kvSpan{}, false
}

func (s *lineScanner) valueBytes(v *lineView, sp kvSpan) []byte {
	raw := v.line[sp.val.start:sp.val.end]
	if sp.kind == kindString && (sp.esc || sp.high) {
		s.scratch = unquoteAppend(s.scratch[:0], raw)
		return s.scratch
	}
	return raw
}

func (s *lineScanner) typedValue(v *lineView, sp kvSpan) (fieldValue, bool) {
	switch sp.kind {
	case kindNull:
		return fieldValue{}, false
	case kindString:
		return stringValue(s.valueBytes(v, sp)), true
	case kindNumber:
		return classifyNumber(v.line[sp.val.start:sp.val.end]), true
	case kindTrue:
		return boolValue(true), true
	case kindFalse:
		return boolValue(false), true
	default:
		return stringValue(v.line[sp.val.start:sp.val.end]), true
	}
}

func (s *lineScanner) typedField(v *lineView, key string) (fieldValue, bool) {
	sp, found := s.lookup(v, key)
	if !found {
		return fieldValue{}, false
	}
	return s.typedValue(v, sp)
}

func (s *lineScanner) levelFrom(v *lineView) (string, bool) {
	if v.fallback {
		return "", false
	}
	if !v.isJSON {
		return "", true
	}
	sp, found := s.lookup(v, "level")
	if !found || sp.kind == kindNull {
		return "", true
	}
	return s.internLevel(s.valueBytes(v, sp)), true
}

func (s *lineScanner) msgBytes(v *lineView) ([]byte, bool) {
	if v.fallback {
		return nil, false
	}
	if !v.isJSON {
		return v.line, true
	}
	sp, found := s.lookup(v, "msg")
	if !found || sp.kind == kindNull {
		sp, found = s.lookup(v, "message")
	}
	if !found || sp.kind == kindNull {
		return nil, true
	}
	return s.valueBytes(v, sp), true
}

func (s *lineScanner) msgFrom(v *lineView) (string, bool) {
	b, ok := s.msgBytes(v)
	if !ok {
		return "", false
	}
	return string(b), true
}

func (s *lineScanner) internLevel(raw []byte) string {
	for i := range s.levels {
		if bytes.Equal(s.levels[i].raw, raw) {
			return s.levels[i].norm
		}
	}
	norm := normalizeLevel(string(raw))
	if len(s.levels) < maxLevelIntern {
		s.levels = append(s.levels, internedLevel{raw: bytes.Clone(raw), norm: norm})
	}
	return norm
}

func (s *lineScanner) levelOf(line []byte) string {
	v := s.view(line)
	if lvl, ok := s.levelFrom(&v); ok {
		return lvl
	}
	lvl, _, _ := parseLine(line)
	return lvl
}

func (s *lineScanner) shadowed(v *lineView, i int) bool {
	sp := v.spans[i]
	klen := sp.key.end - sp.key.start
	for j := len(v.spans) - 1; j > i; j-- {
		o := v.spans[j]
		if o.key.end-o.key.start != klen {
			continue
		}
		if bytes.Equal(s.keyBuf[sp.key.start:sp.key.end], s.keyBuf[o.key.start:o.key.end]) {
			return true
		}
	}
	return false
}

func (s *lineScanner) materialize(v *lineView) []shredField {
	s.fields = s.fields[:0]
	if cap(s.scratch) < len(v.line) {
		s.scratch = make([]byte, 0, len(v.line))
	}
	s.scratch = s.scratch[:0]
	for i := range v.spans {
		sp := v.spans[i]
		if sp.kind == kindNull || (sp.top && isLiftedKey(s.keyOf(sp))) {
			continue
		}
		if s.shadowed(v, i) {
			continue
		}
		var val fieldValue
		switch sp.kind {
		case kindString:
			raw := v.line[sp.val.start:sp.val.end]
			if sp.esc || sp.high {
				at := len(s.scratch)
				s.scratch = unquoteAppend(s.scratch, raw)
				val = stringValue(s.scratch[at:len(s.scratch):len(s.scratch)])
			} else {
				val = stringValue(raw)
			}
		case kindNumber:
			val = classifyNumber(v.line[sp.val.start:sp.val.end])
		case kindTrue:
			val = boolValue(true)
		case kindFalse:
			val = boolValue(false)
		default:
			val = stringValue(v.line[sp.val.start:sp.val.end])
		}
		s.fields = append(s.fields, shredField{key: s.keyOf(sp), val: val})
	}
	return s.fields
}

func (s *lineScanner) shred(line []byte) (level, msg string, fields []shredField) {
	v := s.view(line)
	if v.fallback {
		return parseLine(line)
	}
	level, _ = s.levelFrom(&v)
	msg, _ = s.msgFrom(&v)
	if !v.isJSON {
		return level, msg, nil
	}
	return level, msg, s.materialize(&v)
}

func containsFold[T ~string | ~[]byte](scratch *[]byte, hay T, needle []byte) (found, ok bool) {
	buf := (*scratch)[:0]
	for i := 0; i < len(hay); i++ {
		c := hay[i]
		if c >= utf8.RuneSelf {
			*scratch = buf
			return false, false
		}
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf = append(buf, c)
	}
	*scratch = buf
	return bytes.Contains(buf, needle), true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (s *lineScanner) scanObjectFlat(b []byte, i int, depth int, prefix span) (int, bool) {
	n := len(b)
	i = skipWS(b, i)
	if i < n && b[i] == '}' {
		return i + 1, true
	}
	for {
		if i >= n || b[i] != '"' {
			return 0, false
		}
		kStart := i + 1
		next, flags, ok := scanString(b, kStart)
		if !ok {
			return 0, false
		}
		keyStart := len(s.keyBuf)
		if depth > 1 {
			s.keyBuf = append(s.keyBuf, s.keyBuf[prefix.start:prefix.end]...)
			s.keyBuf = append(s.keyBuf, '.')
		}
		rawKey := b[kStart : next-1]
		if flags&(strFlagEsc|strFlagHigh) != 0 {
			s.keyBuf = unquoteAppend(s.keyBuf, rawKey)
		} else {
			s.keyBuf = append(s.keyBuf, rawKey...)
		}
		key := span{int32(keyStart), int32(len(s.keyBuf))}
		keep := int(key.end-key.start) <= maxKeyLen
		i = skipWS(b, next)
		if i >= n || b[i] != ':' {
			return 0, false
		}
		i = skipWS(b, i+1)
		if i >= n {
			return 0, false
		}
		vStart := i
		if b[i] == '{' && keep && depth < maxFlattenDepth && !(depth == 1 && isLiftedKey(s.keyBuf[key.start:key.end])) {
			next, ok = s.scanObjectFlat(b, i+1, depth+1, key)
			if !ok {
				return 0, false
			}
		} else {
			kind, vflags, vnext, ok := scanValue(b, i, 0)
			if !ok {
				return 0, false
			}
			next = vnext
			if keep {
				val := span{int32(vStart), int32(next)}
				if kind == kindString {
					val = span{int32(vStart + 1), int32(next - 1)}
				}
				s.spans = append(s.spans, kvSpan{
					key:  key,
					val:  val,
					kind: kind,
					esc:  vflags&strFlagEsc != 0,
					high: vflags&strFlagHigh != 0,
					top:  depth == 1,
				})
			}
		}
		i = skipWS(b, next)
		if i >= n {
			return 0, false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case '}':
			return i + 1, true
		default:
			return 0, false
		}
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func scanString(b []byte, i int) (next int, flags int, ok bool) {
	n := len(b)
	for i < n {
		c := b[i]
		switch {
		case c == '"':
			return i + 1, flags, true
		case c == '\\':
			flags |= strFlagEsc
			i++
			if i >= n {
				return 0, 0, false
			}
			switch b[i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i++
			case 'u':
				if i+4 >= n {
					return 0, 0, false
				}
				for k := 1; k <= 4; k++ {
					if !isHex(b[i+k]) {
						return 0, 0, false
					}
				}
				i += 5
			default:
				return 0, 0, false
			}
		case c < 0x20:
			return 0, 0, false
		case c >= utf8.RuneSelf:
			flags |= strFlagHigh
			i++
		default:
			i++
		}
	}
	return 0, 0, false
}

func scanNumber(b []byte, i int) (int, bool) {
	n := len(b)
	if i < n && b[i] == '-' {
		i++
	}
	if i >= n {
		return 0, false
	}
	switch {
	case b[i] == '0':
		i++
	case '1' <= b[i] && b[i] <= '9':
		for i < n && isDigit(b[i]) {
			i++
		}
	default:
		return 0, false
	}
	if i < n && b[i] == '.' {
		i++
		if i >= n || !isDigit(b[i]) {
			return 0, false
		}
		for i < n && isDigit(b[i]) {
			i++
		}
	}
	if i < n && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < n && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= n || !isDigit(b[i]) {
			return 0, false
		}
		for i < n && isDigit(b[i]) {
			i++
		}
	}
	return i, true
}

func scanLiteral(b []byte, i int, lit string) (int, bool) {
	if len(b)-i < len(lit) || string(b[i:i+len(lit)]) != lit {
		return 0, false
	}
	return i + len(lit), true
}

func scanValue(b []byte, i int, depth int) (kind jsonKind, flags int, next int, ok bool) {
	if i >= len(b) {
		return 0, 0, 0, false
	}
	switch c := b[i]; {
	case c == '"':
		next, flags, ok = scanString(b, i+1)
		return kindString, flags, next, ok
	case c == '{':
		if depth >= maxViewDepth {
			return 0, 0, 0, false
		}
		next, ok = scanObject(b, i+1, depth+1)
		return kindObject, 0, next, ok
	case c == '[':
		if depth >= maxViewDepth {
			return 0, 0, 0, false
		}
		next, ok = scanArray(b, i+1, depth+1)
		return kindArray, 0, next, ok
	case c == 't':
		next, ok = scanLiteral(b, i, "true")
		return kindTrue, 0, next, ok
	case c == 'f':
		next, ok = scanLiteral(b, i, "false")
		return kindFalse, 0, next, ok
	case c == 'n':
		next, ok = scanLiteral(b, i, "null")
		return kindNull, 0, next, ok
	case c == '-' || isDigit(c):
		next, ok = scanNumber(b, i)
		return kindNumber, 0, next, ok
	}
	return 0, 0, 0, false
}

func scanObject(b []byte, i int, depth int) (int, bool) {
	n := len(b)
	i = skipWS(b, i)
	if i < n && b[i] == '}' {
		return i + 1, true
	}
	for {
		if i >= n || b[i] != '"' {
			return 0, false
		}
		next, _, ok := scanString(b, i+1)
		if !ok {
			return 0, false
		}
		i = skipWS(b, next)
		if i >= n || b[i] != ':' {
			return 0, false
		}
		i = skipWS(b, i+1)
		_, _, next, ok = scanValue(b, i, depth)
		if !ok {
			return 0, false
		}
		i = skipWS(b, next)
		if i >= n {
			return 0, false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case '}':
			return i + 1, true
		default:
			return 0, false
		}
	}
}

func scanArray(b []byte, i int, depth int) (int, bool) {
	n := len(b)
	i = skipWS(b, i)
	if i < n && b[i] == ']' {
		return i + 1, true
	}
	for {
		_, _, next, ok := scanValue(b, i, depth)
		if !ok {
			return 0, false
		}
		i = skipWS(b, next)
		if i >= n {
			return 0, false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case ']':
			return i + 1, true
		default:
			return 0, false
		}
	}
}

func unquoteAppend(dst, s []byte) []byte {
	for r := 0; r < len(s); {
		c := s[r]
		switch {
		case c == '\\':
			r++
			switch s[r] {
			case '"', '\\', '/':
				dst = append(dst, s[r])
				r++
			case 'b':
				dst = append(dst, '\b')
				r++
			case 'f':
				dst = append(dst, '\f')
				r++
			case 'n':
				dst = append(dst, '\n')
				r++
			case 'r':
				dst = append(dst, '\r')
				r++
			case 't':
				dst = append(dst, '\t')
				r++
			case 'u':
				rr := getu4(s[r+1 : r+5])
				r += 5
				if utf16.IsSurrogate(rr) {
					rr1 := rune(-1)
					if r+6 <= len(s) && s[r] == '\\' && s[r+1] == 'u' {
						rr1 = getu4(s[r+2 : r+6])
					}
					if dec := utf16.DecodeRune(rr, rr1); dec != unicode.ReplacementChar {
						dst = utf8.AppendRune(dst, dec)
						r += 6
						continue
					}
					rr = unicode.ReplacementChar
				}
				dst = utf8.AppendRune(dst, rr)
			}
		case c < utf8.RuneSelf:
			dst = append(dst, c)
			r++
		default:
			rr, size := utf8.DecodeRune(s[r:])
			if rr == utf8.RuneError && size == 1 {
				dst = utf8.AppendRune(dst, utf8.RuneError)
			} else {
				dst = append(dst, s[r:r+size]...)
			}
			r += size
		}
	}
	return dst
}

func getu4(s []byte) rune {
	var r rune
	for _, c := range s[:4] {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}
