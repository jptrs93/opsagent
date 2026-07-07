package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/time/rate"
)

func TestPerIPAllowsBurstThenRejects(t *testing.T) {
	middleware := PerIP(rate.Limit(0), 1, time.Minute)

	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/enrollment/request"); got != http.StatusNoContent {
		t.Fatalf("first request status = %d, want %d", got, http.StatusNoContent)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/enrollment/request"); got != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestPerIPTracksClientsSeparately(t *testing.T) {
	middleware := PerIP(rate.Limit(0), 1, time.Minute)

	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/enrollment/request"); got != http.StatusNoContent {
		t.Fatalf("first client status = %d, want %d", got, http.StatusNoContent)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/enrollment/request"); got != http.StatusTooManyRequests {
		t.Fatalf("first client retry status = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.11:1234", "/v1/enrollment/request"); got != http.StatusNoContent {
		t.Fatalf("second client status = %d, want %d", got, http.StatusNoContent)
	}
}

func TestPerIPAndPrefixOnlyLimitsMatchingPrefix(t *testing.T) {
	middleware := PerIPAndPrefix("/v1/auth", rate.Limit(0), 1, time.Minute)

	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/settings"); got != http.StatusNoContent {
		t.Fatalf("non-matching request status = %d, want %d", got, http.StatusNoContent)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/settings"); got != http.StatusNoContent {
		t.Fatalf("non-matching retry status = %d, want %d", got, http.StatusNoContent)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/auth/passkey/login/start"); got != http.StatusNoContent {
		t.Fatalf("matching request status = %d, want %d", got, http.StatusNoContent)
	}
	if got := runRateLimitedRequest(middleware, "192.0.2.10:1234", "/v1/auth/passkey/login/start"); got != http.StatusTooManyRequests {
		t.Fatalf("matching retry status = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func runRateLimitedRequest(middleware apigen.MiddlewareFunc, remoteAddr, path string) int {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	handler := middleware(func(_ context.Context, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler(context.Background(), w, req)
	return w.Result().StatusCode
}
