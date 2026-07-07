package ratelimit

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/time/rate"
)

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func Global(r rate.Limit, burst int) apigen.MiddlewareFunc {
	limiter := rate.NewLimiter(r, burst)
	return func(next apigen.HandlerFunc) apigen.HandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
			if !limiter.Allow() {
				apigen.HandleReqErr(ctx, apigen.NewApiErr("Too many requests", "global rate limit exceeded", http.StatusTooManyRequests), req, w)
				return
			}
			next(ctx, w, req)
		}
	}
}

func PerIP(r rate.Limit, burst int, ttl time.Duration) apigen.MiddlewareFunc {
	return perIPWithMatcher(r, burst, ttl, func(*http.Request) bool { return true }, "per-ip rate limit exceeded")
}

func PerIPAndPrefix(prefix string, r rate.Limit, burst int, ttl time.Duration) apigen.MiddlewareFunc {
	return perIPWithMatcher(r, burst, ttl, func(req *http.Request) bool {
		return strings.HasPrefix(req.URL.Path, prefix)
	}, "per-ip prefix rate limit exceeded")
}

func perIPWithMatcher(r rate.Limit, burst int, ttl time.Duration, match func(*http.Request) bool, internalErr string) apigen.MiddlewareFunc {
	var mu sync.Mutex
	clients := map[string]*clientLimiter{}

	return func(next apigen.HandlerFunc) apigen.HandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
			if !match(req) {
				next(ctx, w, req)
				return
			}
			now := time.Now()
			ip := clientIP(req)

			mu.Lock()
			for key, client := range clients {
				if now.Sub(client.lastSeen) > ttl {
					delete(clients, key)
				}
			}
			client, ok := clients[ip]
			if !ok {
				client = &clientLimiter{limiter: rate.NewLimiter(r, burst)}
				clients[ip] = client
			}
			client.lastSeen = now
			allow := client.limiter.Allow()
			mu.Unlock()

			if !allow {
				apigen.HandleReqErr(ctx, apigen.NewApiErr("Too many requests", internalErr, http.StatusTooManyRequests), req, w)
				return
			}
			next(ctx, w, req)
		}
	}
}

func clientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(req.RemoteAddr) != "" {
		return req.RemoteAddr
	}
	return "unknown"
}
