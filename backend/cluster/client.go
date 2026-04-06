package cluster

import (
	"crypto/tls"
	"fmt"
)

// Dial connects to a cluster peer at addr using mTLS and returns a framed Conn.
func Dial(addr string, tlsConfig *tls.Config) (*Conn, error) {
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("cluster dial %s: %w", addr, err)
	}
	return NewConn(conn), nil
}
