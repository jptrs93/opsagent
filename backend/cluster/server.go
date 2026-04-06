package cluster

import (
	"crypto/tls"
	"fmt"
	"net"
)

// Server is a TLS listener that accepts mTLS connections from cluster peers.
type Server struct {
	listener net.Listener
}

// NewServer creates a TLS listener on addr using the provided mTLS config.
func NewServer(addr string, tlsConfig *tls.Config) (*Server, error) {
	ln, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("cluster listen on %s: %w", addr, err)
	}
	return &Server{listener: ln}, nil
}

// Accept waits for and returns the next mTLS connection as a framed Conn.
// The caller owns the returned Conn and must close it when done.
func (s *Server) Accept() (*Conn, error) {
	raw, err := s.listener.Accept()
	if err != nil {
		return nil, err
	}
	tlsConn, ok := raw.(*tls.Conn)
	if !ok {
		raw.Close()
		return nil, fmt.Errorf("accepted non-TLS connection")
	}
	if err := tlsConn.Handshake(); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}
	return NewConn(tlsConn), nil
}

// Addr returns the listener's network address.
func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

// Close stops the listener.
func (s *Server) Close() error {
	return s.listener.Close()
}
