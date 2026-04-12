package cluster

import (
	"crypto/tls"
	"fmt"
)

// Dial connects to a cluster peer at addr using mTLS and returns a framed Conn.
// serverName is the expected CN/SAN of the server's certificate — needed when
// dialing by IP address so TLS verification matches the cert's DNS SAN rather
// than requiring an IP SAN.
func Dial(addr string, tlsConfig *tls.Config, serverName string) (*Conn, error) {
	cfg := tlsConfig.Clone()
	if serverName != "" {
		cfg.ServerName = serverName
	}
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("cluster dial %s: %w", addr, err)
	}
	return NewConn(conn), nil
}
