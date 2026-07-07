package acmedebug

import (
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func Enable(manager *autocert.Manager) {
	if manager == nil {
		return
	}
	manager.Client = loggingACMEClient()
}

func loggingACMEClient() *acme.Client {
	return &acme.Client{
		DirectoryURL: autocert.DefaultACMEDirectory,
		HTTPClient: &http.Client{
			Transport: loggingACMETransport{next: http.DefaultTransport},
		},
	}
}

type loggingACMETransport struct {
	next http.RoundTripper
}

func (t loggingACMETransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}

	method := ""
	url := ""
	if req != nil {
		method = req.Method
		url = sanitizedHTTPURL(req)
	}
	slog.Info("acme http request started", "method", method, "url", url)

	resp, err := next.RoundTrip(req)
	if err != nil {
		slog.Warn("acme http request failed",
			"method", method,
			"url", url,
			"duration", time.Since(start),
			"err", err,
		)
		return nil, err
	}

	attrs := []any{
		"method", method,
		"url", url,
		"status", resp.Status,
		"status_code", resp.StatusCode,
		"duration", time.Since(start),
	}
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		attrs = append(attrs, "retry_after", retryAfter)
	}
	if resp.Header.Get("Replay-Nonce") != "" {
		attrs = append(attrs, "replay_nonce", "present")
	}

	if resp.StatusCode >= 400 {
		slog.Warn("acme http request completed", attrs...)
	} else {
		slog.Info("acme http request completed", attrs...)
	}
	return resp, nil
}

func sanitizedHTTPURL(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	u := *req.URL
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
