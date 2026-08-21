package logu

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jptrs93/goutil/timeu"
)

type PlainLogHandler struct {
	Writer io.Writer
	Level  slog.Level
	attrs  []slog.Attr
	groups []string
}

func (h *PlainLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.Level
}

func (h *PlainLogHandler) Handle(ctx context.Context, r slog.Record) error {
	var logContext string
	if lc := GetContext(ctx); lc != nil && (len(lc.KVs) > 0 || len(lc.Tags) > 0) {
		parts := make([]string, 0, len(lc.KVs)+len(lc.Tags))
		for _, kv := range lc.KVs {
			parts = append(parts, fmt.Sprintf("%s=%v", kv.K, kv.V))
		}
		parts = append(parts, lc.Tags...)
		logContext = " [" + strings.Join(parts, ", ") + "]"
	}

	// make the level strings all the same length
	var levelStr string
	switch r.Level {
	case slog.LevelDebug:
		levelStr = "DEBUG"
	case slog.LevelInfo:
		levelStr = "INFO"
	case slog.LevelWarn:
		levelStr = "WARN"
	case slog.LevelError:
		levelStr = "ERROR"
	}

	prefix := strings.Join(h.groups, ".")
	attrs := make([]string, 0, len(h.attrs)+r.NumAttrs())
	var stacktrace string

	for _, a := range h.attrs {
		appendAttrPairs(&attrs, prefix, a, &stacktrace)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttrPairs(&attrs, prefix, a, &stacktrace)
		return true
	})

	attrSuffix := ""
	if len(attrs) > 0 {
		attrSuffix = " " + strings.Join(attrs, " ")
	}

	msg := fmt.Sprintf("%s %s%s %s%s\n", r.Time.UTC().Format(timeu.RFC3339Milli), levelStr, logContext, r.Message, attrSuffix)
	_, err := fmt.Fprint(h.Writer, msg)
	if err != nil {
		return err
	}

	if stacktrace != "" {
		_, err = fmt.Fprint(h.Writer, stacktrace)
	}

	return err
}

func (h *PlainLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	newAttrs = append(newAttrs, attrs...)

	newGroups := make([]string, len(h.groups))
	copy(newGroups, h.groups)

	return &PlainLogHandler{
		Writer: h.Writer,
		Level:  h.Level,
		attrs:  newAttrs,
		groups: newGroups,
	}
}

func (h *PlainLogHandler) WithGroup(name string) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs))
	copy(newAttrs, h.attrs)

	newGroups := make([]string, 0, len(h.groups)+1)
	newGroups = append(newGroups, h.groups...)
	newGroups = append(newGroups, name)

	return &PlainLogHandler{
		Writer: h.Writer,
		Level:  h.Level,
		attrs:  newAttrs,
		groups: newGroups,
	}
}

func appendAttrPairs(pairs *[]string, prefix string, attr slog.Attr, stacktrace *string) {
	attr.Value = attr.Value.Resolve()

	if attr.Value.Kind() == slog.KindGroup {
		groupPrefix := joinAttrKey(prefix, attr.Key)
		for _, nested := range attr.Value.Group() {
			appendAttrPairs(pairs, groupPrefix, nested, stacktrace)
		}
		return
	}

	key := joinAttrKey(prefix, attr.Key)
	if key == "" {
		return
	}

	if key == "stacktrace" {
		*stacktrace = attr.Value.String()
		return
	}

	*pairs = append(*pairs, fmt.Sprintf("%s=%v", key, attr.Value.Any()))
}

func joinAttrKey(prefix string, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}
