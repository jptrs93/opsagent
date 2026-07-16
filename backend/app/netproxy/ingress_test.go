package netproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestReadTLSClientHelloFragmented(t *testing.T) {
	want := clientHelloRecord("DB.Example.COM")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		for _, chunk := range [][]byte{want[:3], want[3:11], want[11:]} {
			_, _ = client.Write(chunk)
		}
	}()

	got, hostname, err := readTLSClientHello(server)
	if err != nil {
		t.Fatalf("readTLSClientHello failed: %v", err)
	}
	if hostname != "db.example.com" {
		t.Fatalf("hostname = %q, want normalized SNI", hostname)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ClientHello bytes changed\ngot:  %x\nwant: %x", got, want)
	}
}

func TestReadTLSClientHelloRejectsMissingSNI(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() { _, _ = client.Write(clientHelloRecord("")) }()

	if _, _, err := readTLSClientHello(server); err == nil {
		t.Fatal("readTLSClientHello succeeded without SNI")
	}
}

func TestReadTLSClientHelloAcrossRecords(t *testing.T) {
	handshake := clientHelloRecord("db.example.com")[5:]
	want := append(tlsHandshakeRecord(handshake[:12]), tlsHandshakeRecord(handshake[12:])...)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() { _, _ = client.Write(want) }()

	got, hostname, err := readTLSClientHello(server)
	if err != nil {
		t.Fatalf("readTLSClientHello failed: %v", err)
	}
	if hostname != "db.example.com" || !bytes.Equal(got, want) {
		t.Fatalf("got hostname %q and bytes %x, want db.example.com and %x", hostname, got, want)
	}
}

func TestReadTLSClientHelloFromGoTLSClient(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		_ = tls.Client(client, &tls.Config{ServerName: "db.example.com", InsecureSkipVerify: true}).Handshake()
	}()

	preface, hostname, err := readTLSClientHello(server)
	if err != nil {
		t.Fatalf("readTLSClientHello failed: %v", err)
	}
	if hostname != "db.example.com" || len(preface) == 0 {
		t.Fatalf("got hostname %q and %d bytes, want db.example.com ClientHello", hostname, len(preface))
	}
	_ = server.Close()
	<-clientDone
}

func TestIngressStateFromNetState(t *testing.T) {
	state := ingressStateFromSnapshot(&apigen.NetState{
		Seq: 7,
		Ingress: []*apigen.NetIngress{{
			Kind:     apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
			Hostname: "DB.Example.COM.",
			TlsPassthrough: &apigen.TlsPassthroughNetIngress{
				HostPort: 8443,
				Backends: []*apigen.IngressBackend{
					{Address: "fd00::42", Port: 5432},
					{Address: "not-an-address", Port: 5432},
				},
			},
		}},
	})
	route := state.routes[8443]["db.example.com"]
	if state.seq != 7 || route == nil {
		t.Fatalf("state = %+v, want normalized route", state)
	}
	if got := route.backends; len(got) != 1 || got[0].address != "fd00::42" || got[0].port != 5432 {
		t.Fatalf("backends = %+v, want valid rendered backend", got)
	}
}

func TestIngressForwardsInspectedClientHello(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting backend: %v", err)
	}
	defer backendListener.Close()
	backendPort := uint16(backendListener.Addr().(*net.TCPAddr).Port)
	want := clientHelloRecord("db.example.com")
	backendDone := make(chan error, 1)
	go func() {
		conn, err := backendListener.Accept()
		if err != nil {
			backendDone <- err
			return
		}
		defer conn.Close()
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			backendDone <- err
			return
		}
		if !bytes.Equal(got, want) {
			backendDone <- io.ErrUnexpectedEOF
			return
		}
		_, err = conn.Write([]byte("reply"))
		backendDone <- err
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &ingressServer{ctx: ctx}
	s.state.Store(&ingressState{routes: map[uint16]map[string]*ingressRoute{
		8443: {
			"db.example.com": {backends: []ingressBackend{{address: "127.0.0.1", port: backendPort}}},
		},
	}})
	server, client := net.Pipe()
	defer client.Close()
	go s.handle(8443, server)
	if _, err := client.Write(want); err != nil {
		t.Fatalf("writing ClientHello: %v", err)
	}
	reply := make([]byte, len("reply"))
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("reading backend reply: %v", err)
	}
	if string(reply) != "reply" {
		t.Fatalf("reply = %q, want reply", reply)
	}
	if err := <-backendDone; err != nil {
		t.Fatalf("backend failed: %v", err)
	}
}

func clientHelloRecord(hostname string) []byte {
	var extensions []byte
	if hostname != "" {
		serverName := make([]byte, 3+len(hostname))
		serverName[0] = 0
		binary.BigEndian.PutUint16(serverName[1:], uint16(len(hostname)))
		copy(serverName[3:], hostname)
		sni := make([]byte, 2+len(serverName))
		binary.BigEndian.PutUint16(sni, uint16(len(serverName)))
		copy(sni[2:], serverName)
		extensions = make([]byte, 4+len(sni))
		binary.BigEndian.PutUint16(extensions[2:], uint16(len(sni)))
		copy(extensions[4:], sni)
	}

	body := make([]byte, 0, 43+len(extensions))
	body = append(body, 3, 3)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0) // session ID length
	body = append(body, 0, 2, 0, 47)
	body = append(body, 1, 0) // compression methods
	extensionLength := make([]byte, 2)
	binary.BigEndian.PutUint16(extensionLength, uint16(len(extensions)))
	body = append(body, extensionLength...)
	body = append(body, extensions...)

	handshake := make([]byte, 4+len(body))
	handshake[0] = 1
	handshake[1] = byte(len(body) >> 16)
	handshake[2] = byte(len(body) >> 8)
	handshake[3] = byte(len(body))
	copy(handshake[4:], body)
	return tlsHandshakeRecord(handshake)
}

func tlsHandshakeRecord(handshake []byte) []byte {
	record := make([]byte, 5+len(handshake))
	record[0], record[1], record[2] = 22, 3, 1
	binary.BigEndian.PutUint16(record[3:], uint16(len(handshake)))
	copy(record[5:], handshake)
	return record
}

func TestIngressHostnameForProxy(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
		ok    bool
	}{
		{value: "DB.Example.COM.", want: "db.example.com", ok: true},
		{value: "-db.example.com", ok: false},
		{value: "db_example.com", ok: false},
	} {
		got, ok := ingressHostnameForProxy(test.value)
		if got != test.want || ok != test.ok {
			t.Errorf("ingressHostnameForProxy(%q) = %q, %t; want %q, %t", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestIngressRouteDialsNextBackend(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting backend: %v", err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	route := &ingressRoute{backends: []ingressBackend{
		{address: "127.0.0.1", port: uint16(port + 1)},
		{address: "127.0.0.1", port: port},
	}}
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()
	conn, err := route.dial(context.Background())
	if err != nil {
		t.Fatalf("route.dial failed: %v", err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatalf("backend %s was not dialed", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	}
}
