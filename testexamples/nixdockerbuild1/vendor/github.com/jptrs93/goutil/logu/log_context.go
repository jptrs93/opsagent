package logu

import (
	"context"
	"log/slog"
	"slices"
)

// LogContextKey is the context key used to store a *LogContext in context.Context.
//
// This pattern provides a simple way to inject contextual logging params that are
// scoped to the current context and its descendants. When a child context is
// created with AddKV or AddTag, it gets a copied KV/tag slice plus the new item,
// so parent and sibling contexts keep their own values.
//
// Example (user ID propagation):
//
//	base := context.Background()
//	requestCtx := AddKV(base, "request_id", "req-123")
//	userCtx := AddKV(requestCtx, "user_id", 42)
//
// This lets you add request/user/job metadata once and automatically include it
// in all logs that flow through that derived context tree. Exact formatting in
// output depends on the configured logging handler.
const LogContextKey = "_logContext"

const tagsKey = "_tags"

type KV struct {
	K string
	V any
}

type LogContext struct {
	KVs  []KV
	Tags []string
	// jsonFragment is the pre-rendered JSON encoding of KVs and Tags, one
	// leading comma per field, ready to splice into a log line after the
	// built-in fields.
	jsonFragment []byte
}

func AddTag(ctx context.Context, tag string) context.Context {
	lc := &LogContext{}
	if existing, ok := ctx.Value(LogContextKey).(*LogContext); ok {
		lc.KVs = existing.KVs
		lc.Tags = append(slices.Clone(existing.Tags), tag) // copy on write
	} else {
		lc.Tags = []string{tag}
	}
	lc.renderJSONFragment()
	return context.WithValue(ctx, LogContextKey, lc)
}

func AddKV(ctx context.Context, key string, value any) context.Context {
	lc := &LogContext{}
	if existing, ok := ctx.Value(LogContextKey).(*LogContext); ok {
		lc.Tags = existing.Tags
		if i := slices.IndexFunc(existing.KVs, func(kv KV) bool { return kv.K == key }); i >= 0 {
			lc.KVs = slices.Clone(existing.KVs) // copy on write
			lc.KVs[i].V = value
		} else {
			n := len(existing.KVs)
			// capacity=n forces the append to allocate a new underlying array
			lc.KVs = append(existing.KVs[:n:n], KV{K: key, V: value})
		}
	} else {
		lc.KVs = []KV{{K: key, V: value}}
	}
	lc.renderJSONFragment()
	return context.WithValue(ctx, LogContextKey, lc)
}

func (lc *LogContext) renderJSONFragment() {
	b := jsonBuf{needComma: true}
	for _, kv := range lc.KVs {
		b.key(kv.K)
		b.value(slog.AnyValue(kv.V).Resolve())
	}
	if len(lc.Tags) > 0 {
		b.key(tagsKey)
		b.buf = append(b.buf, '[')
		for i, tag := range lc.Tags {
			if i > 0 {
				b.buf = append(b.buf, ',')
			}
			b.buf = appendJSONString(b.buf, tag)
		}
		b.buf = append(b.buf, ']')
	}
	lc.jsonFragment = b.buf
}

func GetContext(ctx context.Context) *LogContext {
	if m, ok := ctx.Value(LogContextKey).(*LogContext); ok {
		return m
	}
	return nil
}
