package logmanager

import (
	"bytes"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// lineView is an allocation-free view of one log line: the top-level keys of a
// JSON object located as byte spans, with nothing materialised until asked
// for. It answers the same questions parseLine does (level, msg, one field)
// for the common shapes and hands anything it is not sure about back to
// parseLine, so a filter gives the same answer whichever path evaluates it.
//
// The scanner accepts a strict subset of what encoding/json accepts. Anything
// it rejects, and anything it accepts but cannot render exactly (a nested
// object or array, an escaped key), is reported through fallback rather than
// guessed at. The only deliberate divergence is duplicate keys, where lookup
// returns the last occurrence like a map decode does; that is documented but
// not something the fuzz test exercises.
//
// Spans alias the scanner's scratch and are valid until the next view call on
// the same scanner.
type lineView struct {
	line     []byte   // trimmed line
	spans    []kvSpan // top-level key/value spans; nil for a non-object line
	isJSON   bool     // line is an object the scanner accepted
	fallback bool     // scanner declined; parseLine is the authority for this line
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
	key  span // key content, without quotes
	val  span // string content without quotes; the raw literal otherwise
	kind jsonKind
	esc  bool // value contains a backslash escape
	high bool // value contains a byte >= 0x80
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

// lineScanner owns the scratch a lineView aliases plus a small intern table of
// level values, so a scan loop that only asks for levels allocates nothing in
// steady state. Not safe for concurrent use.
type lineScanner struct {
	spans   []kvSpan
	levels  []internedLevel
	scratch []byte // unescaped value bytes, valid until the next valueBytes call
	lower   []byte // lowered haystack for containsFold
}

func (s *lineScanner) view(raw []byte) lineView {
	line := bytes.TrimSpace(raw)
	v := lineView{line: line}
	if len(line) == 0 || line[0] != '{' {
		return v
	}
	s.spans = s.spans[:0]
	if !s.scanTop(line) {
		v.fallback = true
		return v
	}
	v.spans = s.spans
	v.isJSON = true
	return v
}

// lookup returns the last span whose key equals key.
func (v *lineView) lookup(key string) (kvSpan, bool) {
	for i := len(v.spans) - 1; i >= 0; i-- {
		sp := v.spans[i]
		if string(v.line[sp.key.start:sp.key.end]) == key {
			return sp, true
		}
	}
	return kvSpan{}, false
}

// valueBytes renders a scalar value the way jsonValueString would: string
// content unescaped, other literals as their source text. ok is false for an
// object or array value, which parseLine re-serialises and this view does not
// reproduce. The returned slice aliases either the line or the scanner
// scratch.
func (s *lineScanner) valueBytes(v *lineView, sp kvSpan) ([]byte, bool) {
	raw := v.line[sp.val.start:sp.val.end]
	switch sp.kind {
	case kindString:
		if !sp.esc && !sp.high {
			return raw, true
		}
		s.scratch = unquoteAppend(s.scratch[:0], raw)
		return s.scratch, true
	case kindObject, kindArray:
		return nil, false
	default:
		return raw, true
	}
}

// levelFrom resolves the level as parseLine would. ok is false when the line
// needs the fallback.
func (s *lineScanner) levelFrom(v *lineView) (string, bool) {
	if v.fallback {
		return "", false
	}
	if !v.isJSON {
		return "", true
	}
	sp, found := v.lookup("level")
	if !found {
		return "", true
	}
	b, ok := s.valueBytes(v, sp)
	if !ok {
		return "", false
	}
	return s.internLevel(b), true
}

// msgFrom resolves the message as parseLine would: the msg key if present,
// else message, else empty; the whole line when it is not a JSON object.
func (s *lineScanner) msgFrom(v *lineView) (string, bool) {
	if v.fallback {
		return "", false
	}
	if !v.isJSON {
		return string(v.line), true
	}
	sp, found := v.lookup("msg")
	if !found {
		sp, found = v.lookup("message")
	}
	if !found {
		return "", true
	}
	b, ok := s.valueBytes(v, sp)
	if !ok {
		return "", false
	}
	return string(b), true
}

// msgBytes is msgFrom without the string allocation; the slice is valid until
// the next scanner call.
func (s *lineScanner) msgBytes(v *lineView) ([]byte, bool) {
	if v.fallback {
		return nil, false
	}
	if !v.isJSON {
		return v.line, true
	}
	sp, found := v.lookup("msg")
	if !found {
		sp, found = v.lookup("message")
	}
	if !found {
		return nil, true
	}
	return s.valueBytes(v, sp)
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

// levelOf is the write-path entry point: the level of one line with the
// parseLine fallback folded in.
func (s *lineScanner) levelOf(line []byte) string {
	v := s.view(line)
	if lvl, ok := s.levelFrom(&v); ok {
		return lvl
	}
	lvl, _, _ := parseLine(line)
	return lvl
}

// shred is the commit-path entry point: level and msg for the parquet columns.
func (s *lineScanner) shred(line []byte) (level, msg string) {
	v := s.view(line)
	lvl, ok := s.levelFrom(&v)
	if ok {
		var m string
		if m, ok = s.msgFrom(&v); ok {
			return lvl, m
		}
	}
	level, msg, _ = parseLine(line)
	return level, msg
}

// containsFold reports whether hay contains needle ignoring ASCII case, with
// needle already lowercased and pure ASCII. ok is false when hay has a byte
// outside ASCII, where strings.ToLower has rules this does not replicate.
// scratch is reused for the lowered haystack.
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

// scanTop scans the top-level object of line into s.spans. It returns false
// when the line is not JSON this scanner accepts or when a key needs
// unescaping, both of which the caller treats as fallback.
func (s *lineScanner) scanTop(b []byte) bool {
	n := len(b)
	i := skipWS(b, 1)
	if i < n && b[i] == '}' {
		return true
	}
	for {
		if i >= n || b[i] != '"' {
			return false
		}
		kStart := i + 1
		next, flags, ok := scanString(b, kStart)
		if !ok || flags&strFlagEsc != 0 {
			return false
		}
		key := span{int32(kStart), int32(next - 1)}
		if flags&strFlagHigh != 0 && !utf8.Valid(b[key.start:key.end]) {
			return false
		}
		i = skipWS(b, next)
		if i >= n || b[i] != ':' {
			return false
		}
		i = skipWS(b, i+1)
		vStart := i
		kind, vflags, next, ok := scanValue(b, i, 0)
		if !ok {
			return false
		}
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
		})
		i = skipWS(b, next)
		if i >= n {
			return false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case '}':
			return true
		default:
			return false
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

// scanString scans string content starting after the opening quote and
// returns the index after the closing quote.
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

// scanValue validates one value starting at i. The delimiter after it is the
// caller's to check.
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

// scanObject validates a nested object starting after its opening brace.
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

// scanArray validates a nested array starting after its opening bracket.
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

// unquoteAppend decodes already-validated JSON string content with the same
// rules as encoding/json: escapes resolved, surrogate pairs combined, lone
// surrogates and invalid UTF-8 bytes replaced by U+FFFD.
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

// getu4 decodes four hex digits, returning -1 if any is not hex.
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
