package cluster

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"
)

// MaxFrameSize is the hard upper limit on a single frame payload. This
// prevents a misbehaving peer from causing unbounded memory allocation.
const MaxFrameSize = 16 * 1024 * 1024 // 16 MB

// Conn wraps a TLS connection with length-prefixed framing for bidirectional
// protobuf message exchange.
//
// Wire format per frame:
//
//	[4 bytes: payload length, big-endian uint32]
//	[N bytes: protobuf-encoded payload]
//
// Each side knows the message type from its role:
//   - The primary decodes incoming frames as MsgToMaster.
//   - The worker decodes incoming frames as MsgToWorker.
//
// Writes are mutex-protected so multiple goroutines can send concurrently.
// Reads are NOT mutex-protected — a single goroutine should own the read side.
type Conn struct {
	conn *tls.Conn
	mu   sync.Mutex // protects writes
}

// NewConn wraps an established TLS connection.
func NewConn(conn *tls.Conn) *Conn {
	return &Conn{conn: conn}
}

// PeerName returns the CN (Common Name) from the peer's TLS certificate.
// This is the machine name set during cert generation.
func (c *Conn) PeerName() string {
	state := c.conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	return state.PeerCertificates[0].Subject.CommonName
}

// WriteFrame sends a length-prefixed payload. Safe for concurrent use.
func (c *Conn) WriteFrame(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))

	if _, err := c.conn.Write(header[:]); err != nil {
		return fmt.Errorf("writing frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return fmt.Errorf("writing frame payload: %w", err)
		}
	}
	return nil
}

// ReadFrame reads the next length-prefixed payload. NOT safe for concurrent
// use — a single goroutine should own the read loop.
func (c *Conn) ReadFrame() ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(header[:])

	if length > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d bytes (max %d)", length, MaxFrameSize)
	}

	if length == 0 {
		return nil, nil
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return nil, fmt.Errorf("reading frame payload: %w", err)
	}

	return payload, nil
}

// SetReadDeadline sets a deadline on the next read. Pass a zero time to clear.
func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

// Close closes the underlying TLS connection.
func (c *Conn) Close() error {
	return c.conn.Close()
}
