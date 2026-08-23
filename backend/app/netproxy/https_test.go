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

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestHTTPSTerminationRoutesAndStripsPrefix(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Path", r.URL.Path)
		w.Header().Set("X-Backend-Prefix", r.Header.Get("X-Forwarded-Prefix"))
		w.Header().Set("X-Backend-Host", r.Host)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	backendHost, backendPortStr, _ := net.SplitHostPort(backendURL.Host)
	backendPort, _ := strconv.Atoi(backendPortStr)

	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"app.test"})
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), CertBundleFileName)
	if err := WriteCertBundle(bundlePath, &apigen.CertBundle{Seq: 1, Certs: []*apigen.CertBundleEntry{
		{CertID: "secret:7", Pem: append(append([]byte{}, certPEM...), keyPEM...)},
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
		Hostname: "app.test",
		Https: &apigen.HttpsNetIngress{
			PathPrefix:  "/api",
			StripPrefix: true,
			CertID:      "secret:7",
			Backends:    []*apigen.IngressBackend{{Address: backendHost, Port: int32(backendPort)}},
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
	tlsConn := tls.Client(clientConn, &tls.Config{ServerName: "app.test", RootCAs: roots})
	transport := &http.Transport{
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) { return tlsConn, nil },
	}
	client := &http.Client{Transport: transport}

	resp, err := client.Get("https://app.test/api/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status = %d body = %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Backend-Path"); got != "/hello" {
		t.Fatalf("backend path = %q, want /hello", got)
	}
	if got := resp.Header.Get("X-Backend-Prefix"); got != "/api" {
		t.Fatalf("X-Forwarded-Prefix = %q, want /api", got)
	}
	if got := resp.Header.Get("X-Backend-Host"); got != "app.test" {
		t.Fatalf("backend host = %q, want app.test", got)
	}

	notFound, err := client.Get("https://app.test/other")
	if err != nil {
		t.Fatal(err)
	}

	misdirected, err := http.NewRequest("GET", "https://app.test/api/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	misdirected.Host = "other.test"
	resp, err = client.Do(misdirected)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("misdirected status = %d, want %d", resp.StatusCode, http.StatusMisdirectedRequest)
	}
	_ = notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("unmatched prefix status = %d, want 404", notFound.StatusCode)
	}
}
