package netproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/util/certu"
	"golang.org/x/net/http2"
)

// TestH2CBidiServerHalfClose drives a full-duplex exchange through the
// terminating listener with an h2c backend: the backend echoes one message
// and ends its response while the client's request stream stays open. The
// response end must reach the client promptly; without the request-body pump
// in h2BackendTransport, closing the backend response body deadlocks against
// the still-open inbound request body until the client gives up.
func TestH2CBidiServerHalfClose(t *testing.T) {
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32)
		n, _ := r.Body.Read(buf)
		w.Header().Set("Content-Type", "application/protobuf-stream")
		_, _ = w.Write(buf[:n])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Return without draining the request body: the client stream is
		// still open, mimicking a bidi handler that finished responding.
	}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	backend.Config.Protocols = protocols
	backend.Start()
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	backendHost, backendPortStr, _ := net.SplitHostPort(backendURL.Host)
	backendPort, _ := strconv.Atoi(backendPortStr)

	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"bidi.test"})
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), CertBundleFileName)
	if err := WriteCertBundle(bundlePath, &apigen.CertBundle{Seq: 1, Certs: []*apigen.CertBundleEntry{
		{CertID: "secret:9", Pem: append(append([]byte{}, certPEM...), keyPEM...)},
	}}); err != nil {
		t.Fatal(err)
	}
	certs := newCertStore(bundlePath)
	if err := certs.reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stateHolder atomic.Pointer[httpsState]
	server := newHTTPSServer(ctx, certs, func() *httpsState { return stateHolder.Load() }, nil)
	go server.run()

	snapshot := &apigen.NetState{Seq: 1, Ingress: []*apigen.NetIngress{{
		Kind:     apigen.IngressKind_INGRESS_KIND_HTTPS,
		Hostname: "bidi.test",
		Https: &apigen.HttpsNetIngress{
			CertID:          "secret:9",
			BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_H2C,
			Backends:        []*apigen.IngressBackend{{Address: backendHost, Port: int32(backendPort)}},
		},
	}}}
	stateHolder.Store(server.buildState(snapshot))

	clientConn, serverConn := net.Pipe()
	go func() {
		preface, _, err := readTLSClientHello(serverConn)
		if err != nil {
			_ = serverConn.Close()
			return
		}
		server.terminate(serverConn, preface)
	}()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	tlsConn := tls.Client(clientConn, &tls.Config{ServerName: "bidi.test", RootCAs: roots, NextProtos: []string{"h2"}})
	transport := &http2.Transport{
		DialTLSContext: func(context.Context, string, string, *tls.Config) (net.Conn, error) { return tlsConn, nil },
	}

	bodyReader, bodyWriter := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, "https://bidi.test/bidi", bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/protobuf-stream")
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	if _, err := bodyWriter.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	var resp *http.Response
	select {
	case resp = <-respCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no response headers")
	}
	defer resp.Body.Close()

	echo := make([]byte, 32)
	n, err := resp.Body.Read(echo)
	if err != nil || string(echo[:n]) != "hello" {
		t.Fatalf("echo read n=%d err=%v", n, err)
	}

	// The request stream is deliberately still open; the response must end anyway.
	eofCh := make(chan error, 1)
	go func() {
		_, err := resp.Body.Read(echo)
		eofCh <- err
	}()
	select {
	case err := <-eofCh:
		if err != io.EOF {
			t.Fatalf("expected EOF after backend half-close, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("response did not end while request stream stayed open")
	}
	_ = bodyWriter.Close()
}
