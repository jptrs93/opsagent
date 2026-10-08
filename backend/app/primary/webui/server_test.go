package webui

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
)

func reservePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func webSettings(httpEnabled bool, httpListen string, httpsEnabled bool, httpsListen string) apigen.SystemConfig {
	return apigen.SystemConfig{Settings: apigen.ClusterSettings{
		HttpWeb:  apigen.HttpWebSettings{Enabled: systemconfig.BoolLiteral(httpEnabled), Listen: systemconfig.StringLiteral(httpListen)},
		HttpsWeb: apigen.HttpsWebSettings{Enabled: systemconfig.BoolLiteral(httpsEnabled), Listen: systemconfig.StringLiteral(httpsListen)},
	}}
}

func newTestManager(initial apigen.SystemConfig) (*Manager, *systemconfig.Service) {
	cs := &systemconfig.Service{Subs: pubsubu.NewPubSub(initial, 16)}
	return NewManager(cs, nil), cs
}

func get(addr string) (string, error) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func waitServing(t *testing.T, addr, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := get(addr); err == nil && body == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not serve %q in time", addr, want)
}

func waitRefused(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s still accepts connections", addr)
}

func TestHTTPServerFollowsSettings(t *testing.T) {
	addrA, addrB, addrC := reservePort(t), reservePort(t), reservePort(t)
	m, cs := newTestManager(webSettings(true, addrA, false, ""))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.RunHTTP(ctx, handler) }()
	waitServing(t, addrA, "ok")

	moved := webSettings(true, addrB, false, "")
	prepared, err := m.Prepare(&moved.Settings)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(prepared.entries) != 1 || prepared.entries[0].listen != addrB {
		t.Fatalf("Prepare bound %+v, want one listener on %s", prepared.entries, addrB)
	}
	if _, err := net.Listen("tcp", addrB); err == nil {
		t.Fatalf("the prepared address is not held")
	}
	cs.Subs.Notify(moved)
	waitServing(t, addrB, "ok")
	waitRefused(t, addrA)
	m.mu.Lock()
	pendingLeft := len(m.pending)
	m.mu.Unlock()
	if pendingLeft != 0 {
		t.Fatalf("the adopted listener is still pending")
	}

	cs.Subs.Notify(webSettings(false, addrB, true, addrC))
	waitRefused(t, addrB)

	cs.Subs.Notify(webSettings(true, addrC, false, ""))
	waitServing(t, addrC, "ok")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunHTTP: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("RunHTTP did not return after cancel")
	}
	waitRefused(t, addrC)
}

func TestPrepareFailsOnUnbindableAddressAndLeavesServersAlone(t *testing.T) {
	addrA, taken := reservePort(t), reservePort(t)
	hold, err := net.Listen("tcp", taken)
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer hold.Close()
	m, _ := newTestManager(webSettings(true, addrA, false, ""))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.RunHTTP(ctx, handler) }()
	waitServing(t, addrA, "ok")

	both := webSettings(true, addrA, true, taken)
	prepared, err := m.Prepare(&both.Settings)
	if err == nil {
		prepared.Release()
		t.Fatalf("Prepare bound a port that is in use")
	}
	if !strings.Contains(err.Error(), "https_web.listen") || !strings.Contains(err.Error(), taken) {
		t.Fatalf("error should name the field and address, got %v", err)
	}
	if body, err := get(addrA); err != nil || body != "ok" {
		t.Fatalf("running server disturbed: %q %v", body, err)
	}

	unchanged := webSettings(true, addrA, false, "")
	prepared, err = m.Prepare(&unchanged.Settings)
	if err != nil || len(prepared.entries) != 0 {
		t.Fatalf("unchanged settings should bind nothing, got %+v %v", prepared, err)
	}
}

func TestReleaseFreesPreparedListeners(t *testing.T) {
	addrA, addrB := reservePort(t), reservePort(t)
	m, _ := newTestManager(webSettings(true, addrA, false, ""))
	moved := webSettings(true, addrB, false, "")
	prepared, err := m.Prepare(&moved.Settings)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	prepared.Release()
	ln, err := net.Listen("tcp", addrB)
	if err != nil {
		t.Fatalf("released address still held: %v", err)
	}
	_ = ln.Close()
	if ln := m.take(serverHTTP, addrB); ln != nil {
		t.Fatalf("released listener still pending")
	}
}

func TestInitialBindFailureEndsTheServer(t *testing.T) {
	taken := reservePort(t)
	hold, err := net.Listen("tcp", taken)
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer hold.Close()
	m, _ := newTestManager(webSettings(true, taken, false, ""))
	err = m.RunHTTP(context.Background(), http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), taken) {
		t.Fatalf("expected a bind error naming %s, got %v", taken, err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected the listen error to be wrapped, got %v", err)
	}
}
