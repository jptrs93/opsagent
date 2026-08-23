package acmedebug

import (
	"context"
	"fmt"
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

	ctx := context.Background()
	method := ""
	url := ""
	if req != nil {
		ctx = req.Context()
		method = req.Method
		url = sanitizedHTTPURL(req)
	}
	slog.InfoContext(ctx, fmt.Sprintf("acme http request started %s %s", method, url))

	resp, err := next.RoundTrip(req)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("acme http request failed %s %s duration=%s", method, url, time.Since(start)), "err", err)
		return nil, err
	}

	msg := fmt.Sprintf("acme http request completed %s %s status=%q duration=%s", method, url, resp.Status, time.Since(start))
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		msg += fmt.Sprintf(" retry_after=%s", retryAfter)
	}
	if resp.Header.Get("Replay-Nonce") != "" {
		msg += " replay_nonce=present"
	}

	if resp.StatusCode >= 400 {
		slog.WarnContext(ctx, msg)
	} else {
		slog.InfoContext(ctx, msg)
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
