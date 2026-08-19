// wsecho is a dependency-free RFC 6455 WebSocket server used by the e2e suite
// to verify WebSocket proxying through the opendeploy-net HTTPS terminating
// proxy. Endpoints:
//
//	/ws/echo?stream_id=X   echo text/binary messages; replies pong to pings;
//	                       a text message "close:<code>:<reason>" makes the
//	                       server initiate the close handshake.
//	/ws/push?stream_id=X&count=N&interval_ms=M
//	                       push N text messages after the handshake.
//	/state?stream_id=X     report what the server observed for a connection.
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opcodeContinuation = 0x0
	opcodeText         = 0x1
	opcodeBinary       = 0x2
	opcodeClose        = 0x8
	opcodePing         = 0x9
	opcodePong         = 0xa
)

type connState struct {
	mu          sync.Mutex
	opened      bool
	messagesIn  int
	messagesOut int
	pingsIn     int
	closeCode   int
	closeReason string
	result      string
}

func (s *connState) setResult(result string) {
	s.mu.Lock()
	if s.result == "" {
		s.result = result
	}
	s.mu.Unlock()
}

var states sync.Map

// Each connection starts a fresh state so re-used stream ids (e.g. a retried
// test) report only the most recent connection.
func stateFor(streamID string) *connState {
	state := &connState{}
	states.Store(streamID, state)
	return state
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/echo", serveEcho)
	mux.HandleFunc("/ws/push", servePush)
	mux.HandleFunc("/state", serveState)
	addr := "[::]:8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = "[::]:" + port
	}
	server := &http.Server{Addr: addr, Handler: mux}
	log.Printf("wsecho listening address=%s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func serveState(w http.ResponseWriter, r *http.Request) {
	streamID := r.URL.Query().Get("stream_id")
	value, ok := states.Load(streamID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !ok {
		fmt.Fprintf(w, "opened=false\n")
		return
	}
	state := value.(*connState)
	state.mu.Lock()
	defer state.mu.Unlock()
	fmt.Fprintf(w, "opened=%t\n", state.opened)
	fmt.Fprintf(w, "messages-in=%d\n", state.messagesIn)
	fmt.Fprintf(w, "messages-out=%d\n", state.messagesOut)
	fmt.Fprintf(w, "pings-in=%d\n", state.pingsIn)
	fmt.Fprintf(w, "close-code=%d\n", state.closeCode)
	fmt.Fprintf(w, "close-reason=%s\n", state.closeReason)
	fmt.Fprintf(w, "result=%s\n", state.result)
}

func upgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, *connState, error) {
	streamID := r.URL.Query().Get("stream_id")
	if streamID == "" {
		http.Error(w, "stream_id required", http.StatusBadRequest)
		return nil, nil, nil, errors.New("missing stream_id")
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusUpgradeRequired)
		return nil, nil, nil, errors.New("not websocket")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "Sec-WebSocket-Key required", http.StatusBadRequest)
		return nil, nil, nil, errors.New("missing key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return nil, nil, nil, errors.New("no hijack")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, nil, err
	}
	sum := sha1.Sum([]byte(key + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	state := stateFor(streamID)
	state.mu.Lock()
	state.opened = true
	state.mu.Unlock()
	return conn, rw, state, nil
}

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func readFrame(r *bufio.Reader) (frame, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	f := frame{fin: header[0]&0x80 != 0, opcode: header[0] & 0x0f}
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > 16<<20 {
		return frame{}, errors.New("frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return frame{}, err
		}
	}
	f.payload = make([]byte, length)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return frame{}, err
	}
	if masked {
		for i := range f.payload {
			f.payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

func writeFrame(w *bufio.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xffff:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return w.Flush()
}

func closePayload(code int, reason string) []byte {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	return payload
}

func recordClose(state *connState, payload []byte) {
	state.mu.Lock()
	if len(payload) >= 2 {
		state.closeCode = int(binary.BigEndian.Uint16(payload))
		state.closeReason = string(payload[2:])
	}
	state.mu.Unlock()
	state.setResult("closed")
}

func serveEcho(w http.ResponseWriter, r *http.Request) {
	conn, rw, state, err := upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		f, err := readFrame(rw.Reader)
		if err != nil {
			state.setResult("client-gone")
			return
		}
		switch f.opcode {
		case opcodePing:
			state.mu.Lock()
			state.pingsIn++
			state.mu.Unlock()
			if err := writeFrame(rw.Writer, opcodePong, f.payload); err != nil {
				state.setResult("client-gone")
				return
			}
		case opcodeClose:
			recordClose(state, f.payload)
			_ = writeFrame(rw.Writer, opcodeClose, f.payload)
			return
		case opcodeText, opcodeBinary:
			if !f.fin {
				state.setResult("unsupported-fragmentation")
				return
			}
			state.mu.Lock()
			state.messagesIn++
			state.mu.Unlock()
			if f.opcode == opcodeText && strings.HasPrefix(string(f.payload), "close:") {
				parts := strings.SplitN(string(f.payload), ":", 3)
				code, _ := strconv.Atoi(parts[1])
				reason := ""
				if len(parts) == 3 {
					reason = parts[2]
				}
				_ = writeFrame(rw.Writer, opcodeClose, closePayload(code, reason))
				// Wait briefly for the client's close reply so the handshake
				// completes before the TCP connection drops.
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if reply, err := readFrame(rw.Reader); err == nil && reply.opcode == opcodeClose {
					recordClose(state, reply.payload)
				}
				state.setResult("server-closed")
				return
			}
			if err := writeFrame(rw.Writer, f.opcode, f.payload); err != nil {
				state.setResult("client-gone")
				return
			}
			state.mu.Lock()
			state.messagesOut++
			state.mu.Unlock()
		}
	}
}

func servePush(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	intervalMs, _ := strconv.Atoi(r.URL.Query().Get("interval_ms"))
	conn, rw, state, err := upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	// Read in the background so client close frames and disconnects are
	// noticed while the push loop is sleeping between messages.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, err := readFrame(rw.Reader)
			if err != nil {
				state.setResult("client-gone")
				return
			}
			if f.opcode == opcodeClose {
				recordClose(state, f.payload)
				return
			}
		}
	}()
	for i := 1; i <= count; i++ {
		select {
		case <-done:
			return
		case <-time.After(time.Duration(intervalMs) * time.Millisecond):
		}
		if err := writeFrame(rw.Writer, opcodeText, fmt.Appendf(nil, "push %d/%d", i, count)); err != nil {
			state.setResult("client-gone")
			return
		}
		state.mu.Lock()
		state.messagesOut++
		state.mu.Unlock()
	}
	state.setResult("completed")
	_ = writeFrame(rw.Writer, opcodeClose, closePayload(1000, "push complete"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	<-done
}
