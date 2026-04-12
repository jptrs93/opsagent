// Package primary implements the primary-side cluster listener. It accepts
// mTLS connections from worker nodes, sends them the current per-machine
// deployment snapshot, forwards ongoing deployment config updates, and
// handles incoming status writes and log proxy requests from workers.
package primary

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/storage"
)

// Primary manages worker connections and forwards state between the local
// store and connected workers.
type Primary struct {
	store  storage.PrimaryLocalStore
	server *cluster.Server

	mu          sync.RWMutex
	sessions    map[string]*Session   // machine name → session
	connectedAt map[string]time.Time  // machine name → when session was accepted

	// OnSlaveConnect is invoked (if set) after a slave session is accepted
	// and registered.
	OnSlaveConnect func(machine string)
}

// New creates a Primary and starts the mTLS listener.
func New(store storage.PrimaryLocalStore, tlsCfg *tls.Config, listenAddr string) (*Primary, error) {
	srv, err := cluster.NewServer(listenAddr, tlsCfg)
	if err != nil {
		return nil, err
	}
	return &Primary{
		store:       store,
		server:      srv,
		sessions:    make(map[string]*Session),
		connectedAt: make(map[string]time.Time),
	}, nil
}

// Start begins the accept loop. Each accepted connection runs its own
// session which both sends the initial snapshot + forwards per-machine
// updates, and reads incoming status writes / log chunks.
func (p *Primary) Start(ctx context.Context) {
	go p.acceptLoop(ctx)
}

func (p *Primary) acceptLoop(ctx context.Context) {
	slog.Info("cluster primary accepting connections", "addr", p.server.Addr())
	for {
		conn, err := p.server.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			slog.Error("cluster accept error", "err", err)
			continue
		}

		machine := conn.PeerName()
		slog.Info("worker connected", "machine", machine)

		sess := newSession(conn, machine, p.store, p)
		p.registerSession(machine, sess)
		if p.OnSlaveConnect != nil {
			p.OnSlaveConnect(machine)
		}

		go func(s *Session) {
			if err := s.run(ctx); err != nil {
				slog.Info("worker session ended", "machine", machine, "err", err)
			}
			p.unregisterSession(machine, s)
		}(sess)
	}
}

func (p *Primary) registerSession(machine string, sess *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sessions[machine]; ok {
		old.conn.Close()
	}
	p.sessions[machine] = sess
	p.connectedAt[machine] = time.Now()
}

func (p *Primary) unregisterSession(machine string, expected *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.sessions[machine]; ok && current == expected {
		current.conn.Close()
		delete(p.sessions, machine)
	}
}

// RequestLogs sends a log request to the named worker and returns a reader
// that yields the streamed log data. The caller must read until EOF (or
// close the reader to abort).
func (p *Primary) RequestLogs(machineName string, req *apigen.MsgToWorker) (io.ReadCloser, error) {
	p.mu.RLock()
	sess, ok := p.sessions[machineName]
	p.mu.RUnlock()
	if !ok {
		return nil, &MachineNotConnectedError{Machine: machineName}
	}
	return sess.requestLogs(req)
}

// ConnectedMachines returns the set of currently connected slave machines
// and when each connected.
func (p *Primary) ConnectedMachines() map[string]time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]time.Time, len(p.sessions))
	for name, _ := range p.sessions {
		out[name] = p.connectedAt[name]
	}
	return out
}

// MachineNotConnectedError is returned when a log proxy request targets a
// machine that has no active cluster session.
type MachineNotConnectedError struct {
	Machine string
}

func (e *MachineNotConnectedError) Error() string {
	return "machine not connected: " + e.Machine
}
