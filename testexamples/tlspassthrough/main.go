package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/jptrs93/goutil/logu"
)

func main() {
	slog.SetDefault(logu.NewJSONLogger(os.Stdout, slog.LevelInfo))
	bundle, err := base64.StdEncoding.DecodeString(os.Getenv("OPENDEPLOY_TLS_BUNDLE_B64"))
	if err != nil {
		fatalf("decode TLS bundle: %v", err)
	}
	cert, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		fatalf("load TLS bundle: %v", err)
	}
	backend := os.Getenv("OPENDEPLOY_TLS_BACKEND")
	if backend == "" {
		fatalf("OPENDEPLOY_TLS_BACKEND is required")
	}

	listener, err := tls.Listen("tcp", "[::]:8443", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		fatalf("listen: %v", err)
	}
	defer listener.Close()
	logf("tlspassthrough listening backend=%s address=%s", backend, listener.Addr())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fmt.Fprintf(w, "backend=%s\nsni=%s\n", backend, r.TLS.ServerName); err != nil {
			logf("write response: %v", err)
		}
	})
	if err := (&http.Server{Handler: handler}).Serve(listener); err != nil && err != http.ErrServerClosed {
		fatalf("serve: %v", err)
	}
}

func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
