package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jptrs93/goutil/logu"
)

type SlogHandler struct {
	out    io.Writer
	mu     *sync.Mutex
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

func NewSlogHandler(out io.Writer, level slog.Level) *SlogHandler {
	return &SlogHandler{out: out, mu: &sync.Mutex{}, level: level}
}

func (h *SlogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	appendLogfmtField(&b, "time", r.Time.UTC().Format(time.RFC3339Nano))
	appendLogfmtField(&b, "level", r.Level.String())
	if lc := logu.GetLogContext(ctx); lc != nil {
		for _, item := range lc.Items {
			value := any(true)
			if item.Value != nil {
				value = *item.Value
			}
			appendLogfmtField(&b, item.Name, fmtValue(value))
		}
	}

	appendLogfmtField(&b, "msg", r.Message)
	for _, attr := range h.attrs {
		h.appendAttr(&b, attr)
	}
	r.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(&b, attr)
		return true
	})
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func fmtValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("value")
}

func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

func (h *SlogHandler) appendAttr(b *strings.Builder, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	key := groupedLogKey(h.groups, attr.Key)
	appendLogfmtValue(b, key, attr.Value)
}

func appendLogfmtValue(b *strings.Builder, key string, value slog.Value) {
	if value.Kind() == slog.KindGroup {
		for _, attr := range value.Group() {
			attr.Value = attr.Value.Resolve()
			appendLogfmtValue(b, groupedLogKey([]string{key}, attr.Key), attr.Value)
		}
		return
	}
	appendLogfmtField(b, key, value.String())
}

func appendLogfmtField(b *strings.Builder, key string, value string) {
	key = sanitizeLogKey(key)
	if key == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(formatLogfmtFieldValue(value))
}

func groupedLogKey(groups []string, key string) string {
	if len(groups) == 0 {
		return key
	}
	parts := append(append([]string{}, groups...), key)
	return strings.Join(parts, ".")
}

func sanitizeLogKey(key string) string {
	return strings.Map(func(r rune) rune {
		if r == '=' || unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, key)
}

func formatLogfmtFieldValue(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\r=\\\"") {
		return value
	}
	return quoteLogfmtValue(value)
}

func quoteLogfmtValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
