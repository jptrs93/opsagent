package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	bundle, err := base64.StdEncoding.DecodeString(os.Getenv("OPENDEPLOY_TLS_BUNDLE_B64"))
	if err != nil {
		log.Fatalf("decode TLS bundle: %v", err)
	}
	cert, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		log.Fatalf("load TLS bundle: %v", err)
	}
	backend := os.Getenv("OPENDEPLOY_TLS_BACKEND")
	if backend == "" {
		log.Fatal("OPENDEPLOY_TLS_BACKEND is required")
	}

	listener, err := tls.Listen("tcp", "[::]:8443", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	log.Printf("tlspassthrough listening backend=%s address=%s", backend, listener.Addr())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fmt.Fprintf(w, "backend=%s\nsni=%s\n", backend, r.TLS.ServerName); err != nil {
			log.Printf("write response: %v", err)
		}
	})
	if err := (&http.Server{Handler: handler}).Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
