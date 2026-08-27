package logu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// JSONLogHandler mirrors the output and semantics of slog.JSONHandler, with one
// addition: KV pairs from the LogContext stored in ctx are written as top-level
// fields on every line, and tags are written as a list under tagsKey. Context
// fields stay at the top level even when groups are open.
type JSONLogHandler struct {
	Writer       io.Writer
	Level        slog.Level
	preformatted []byte
	groups       []string
	openedGroups int
}

func NewJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewJSONLogHandler(w, level))
}

func NewJSONLogHandler(w io.Writer, level slog.Level) slog.Handler {
	return &JSONLogHandler{Writer: w, Level: level}
}

func (h *JSONLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.Level
}

const maxPooledBufSize = 16 << 10

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

func (h *JSONLogHandler) Handle(ctx context.Context, r slog.Record) error {
	bufp := bufPool.Get().(*[]byte)
	b := jsonBuf{buf: append((*bufp)[:0], '{')}
	if !r.Time.IsZero() {
		b.key(slog.TimeKey)
		b.time(r.Time)
	}
	b.key(slog.LevelKey)
	b.buf = appendJSONString(b.buf, r.Level.String())
	b.key(slog.MessageKey)
	b.buf = appendJSONString(b.buf, r.Message)

	if lc := GetContext(ctx); lc != nil {
		b.buf = append(b.buf, lc.jsonFragment...)
	}

	b.buf = append(b.buf, h.preformatted...)
	b.needComma = b.buf[len(b.buf)-1] != '{'

	opened := h.openedGroups
	if r.NumAttrs() > 0 {
		for _, g := range h.groups[opened:] {
			b.openGroup(g)
		}
		opened = len(h.groups)
		r.Attrs(func(a slog.Attr) bool {
			b.attr(a)
			return true
		})
	}
	for range opened {
		b.buf = append(b.buf, '}')
	}
	b.buf = append(b.buf, '}', '\n')

	_, err := h.Writer.Write(b.buf)
	if cap(b.buf) <= maxPooledBufSize {
		*bufp = b.buf[:0]
		bufPool.Put(bufp)
	}
	return err
}

func (h *JSONLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h2 := *h
	n := len(h.preformatted)
	// capacity=n forces the first append to allocate a new underlying array
	b := jsonBuf{buf: h.preformatted[:n:n], needComma: n == 0 || h.preformatted[n-1] != '{'}
	for _, g := range h.groups[h.openedGroups:] {
		b.openGroup(g)
	}
	h2.openedGroups = len(h.groups)
	for _, a := range attrs {
		b.attr(a)
	}
	h2.preformatted = b.buf
	return &h2
}

func (h *JSONLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	n := len(h.groups)
	// capacity=n forces the append to allocate a new underlying array
	h2.groups = append(h.groups[:n:n], name)
	return &h2
}

type jsonBuf struct {
	buf       []byte
	needComma bool
}

func (b *jsonBuf) key(k string) {
	if b.needComma {
		b.buf = append(b.buf, ',')
	}
	b.needComma = true
	b.buf = appendJSONString(b.buf, k)
	b.buf = append(b.buf, ':')
}

func (b *jsonBuf) openGroup(name string) {
	b.key(name)
	b.buf = append(b.buf, '{')
	b.needComma = false
}

func (b *jsonBuf) attr(a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		if len(group) == 0 {
			return
		}
		if a.Key == "" {
			for _, ga := range group {
				b.attr(ga)
			}
			return
		}
		b.openGroup(a.Key)
		for _, ga := range group {
			b.attr(ga)
		}
		b.buf = append(b.buf, '}')
		b.needComma = true
		return
	}
	if a.Equal(slog.Attr{}) {
		return
	}
	b.key(a.Key)
	b.value(a.Value)
}

func (b *jsonBuf) value(v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		b.buf = appendJSONString(b.buf, v.String())
	case slog.KindInt64:
		b.buf = strconv.AppendInt(b.buf, v.Int64(), 10)
	case slog.KindUint64:
		b.buf = strconv.AppendUint(b.buf, v.Uint64(), 10)
	case slog.KindBool:
		b.buf = strconv.AppendBool(b.buf, v.Bool())
	case slog.KindDuration:
		b.buf = strconv.AppendInt(b.buf, int64(v.Duration()), 10)
	case slog.KindTime:
		b.time(v.Time())
	case slog.KindFloat64:
		// json.Marshal is not always equivalent to strconv.AppendFloat
		b.marshal(v.Float64())
	default:
		a := v.Any()
		if e, ok := a.(error); ok {
			if _, jm := a.(json.Marshaler); !jm {
				b.buf = appendJSONString(b.buf, e.Error())
				return
			}
		}
		b.marshal(a)
	}
}

func (b *jsonBuf) time(t time.Time) {
	// same range restriction as json.Marshal of a time.Time
	if y := t.Year(); y < 0 || y >= 10000 {
		b.buf = appendJSONString(b.buf, "!ERROR:time.Time year outside of range [0,9999]")
		return
	}
	b.buf = append(b.buf, '"')
	b.buf = t.AppendFormat(b.buf, time.RFC3339Nano)
	b.buf = append(b.buf, '"')
}

func (b *jsonBuf) marshal(v any) {
	var bb bytes.Buffer
	enc := json.NewEncoder(&bb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		b.buf = appendJSONString(b.buf, fmt.Sprintf("!ERROR:%v", err))
		return
	}
	out := bb.Bytes()
	// Encode appends a trailing newline
	b.buf = append(b.buf, out[:len(out)-1]...)
}

const hexDigits = "0123456789abcdef"

// appendJSONString matches the escaping of encoding/json with SetEscapeHTML(false)
func appendJSONString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= ' ' && c != '"' && c != '\\' {
				i++
				continue
			}
			buf = append(buf, s[start:i]...)
			switch c {
			case '"', '\\':
				buf = append(buf, '\\', c)
			case '\n':
				buf = append(buf, '\\', 'n')
			case '\r':
				buf = append(buf, '\\', 'r')
			case '\t':
				buf = append(buf, '\\', 't')
			default:
				buf = append(buf, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case c == utf8.RuneError && size == 1:
			buf = append(buf, s[start:i]...)
			buf = append(buf, '\\', 'u', 'f', 'f', 'f', 'd')
			i += size
			start = i
		case c == '\u2028' || c == '\u2029':
			buf = append(buf, s[start:i]...)
			buf = append(buf, '\\', 'u', '2', '0', '2', hexDigits[c&0xf])
			i += size
			start = i
		default:
			i += size
		}
	}
	buf = append(buf, s[start:]...)
	return append(buf, '"')
}
