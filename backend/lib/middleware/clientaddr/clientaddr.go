// Package clientaddr derives the address a request came from and carries it in
// the request context. Generated handlers receive an apigen.Context and never
// the *http.Request, so a handler that wants to record where a request
// originated has no other way to reach it.
package clientaddr

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type ctxKey struct{}

type uaCtxKey struct{}

// ClientIP is the connecting address. It reads RemoteAddr only: forwarded
// headers are attacker-controlled unless a trusted proxy overwrites them, and
// nothing in front of this server does.
func ClientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(req.RemoteAddr) != "" {
		return req.RemoteAddr
	}
	return "unknown"
}

// Middleware stashes the client address for From to read further down.
func Middleware() apigen.MiddlewareFunc {
	return func(next apigen.HandlerFunc) apigen.HandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
			ctx = context.WithValue(ctx, ctxKey{}, ClientIP(req))
			ctx = context.WithValue(ctx, uaCtxKey{}, req.Header.Get("User-Agent"))
			next(ctx, w, req)
		}
	}
}

// From returns the recorded client address, or "" when the middleware is not
// installed — as in handler unit tests, which construct a context directly.
// Callers must treat it as advisory: it is for display and audit, never for
// access decisions.
func From(ctx context.Context) string {
	addr, _ := ctx.Value(ctxKey{}).(string)
	return addr
}

func UserAgentFrom(ctx context.Context) string {
	ua, _ := ctx.Value(uaCtxKey{}).(string)
	return ua
}
