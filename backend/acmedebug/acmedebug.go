package acmedebug

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const (
	EnvVar           = "OPENDEPLOY_DEBUG_ACME"
	acmeTLSALPNProto = "acme-tls/1"
)

type Config struct {
	CacheDir string
	Hosts    []string
	Email    string
}

func EnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func EnableFromEnv(manager *autocert.Manager, tlsConfig *tls.Config, cfg Config) bool {
	if !EnabledFromEnv() {
		return false
	}
	Enable(manager, tlsConfig, cfg)
	return true
}

func Enable(manager *autocert.Manager, tlsConfig *tls.Config, cfg Config) {
	if manager == nil || tlsConfig == nil {
		return
	}

	manager.HostPolicy = loggingAutocertHostPolicy(manager.HostPolicy)
	manager.Client = loggingACMEClient()
	tlsConfig.GetCertificate = loggingAutocertGetCertificate(manager.GetCertificate)
	slog.Info("configured acme certificate manager",
		"cache_dir", cfg.CacheDir,
		"hosts", cfg.Hosts,
		"email", cfg.Email,
		"next_protos", tlsConfig.NextProtos,
		"tls_alpn_01_enabled", hasString(tlsConfig.NextProtos, acmeTLSALPNProto),
	)
}

func ServerErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn)
}

func loggingAutocertHostPolicy(next autocert.HostPolicy) autocert.HostPolicy {
	return func(ctx context.Context, host string) error {
		if next == nil {
			slog.InfoContext(ctx, "acme host accepted", "host", host)
			return nil
		}

		err := next(ctx, host)
		if err != nil {
			slog.WarnContext(ctx, "acme host rejected", "host", host, "err", err)
			return err
		}

		slog.InfoContext(ctx, "acme host accepted", "host", host)
		return nil
	}
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

func loggingAutocertGetCertificate(next func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		serverName := ""
		remoteAddr := ""
		supportedProtos := []string(nil)
		isACMEChallenge := false
		if hello != nil {
			serverName = hello.ServerName
			supportedProtos = hello.SupportedProtos
			isACMEChallenge = hasString(hello.SupportedProtos, acmeTLSALPNProto)
			if hello.Conn != nil {
				remoteAddr = hello.Conn.RemoteAddr().String()
			}
		}

		slog.Info("acme certificate lookup started",
			"server_name", serverName,
			"remote_addr", remoteAddr,
			"supported_protos", supportedProtos,
			"tls_alpn_01", isACMEChallenge,
		)

		cert, err := next(hello)
		if err != nil {
			slog.Warn("acme certificate lookup failed",
				"server_name", serverName,
				"remote_addr", remoteAddr,
				"supported_protos", supportedProtos,
				"tls_alpn_01", isACMEChallenge,
				"err", err,
			)
			return nil, err
		}

		slog.Info("acme certificate lookup succeeded",
			"server_name", serverName,
			"remote_addr", remoteAddr,
			"tls_alpn_01", isACMEChallenge,
		)
		return cert, nil
	}
}

func hasString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
