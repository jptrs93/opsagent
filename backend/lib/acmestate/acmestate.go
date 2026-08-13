// Package acmestate holds the current ACME cert bindings and pending HTTP-01
// challenges on a node, shared between the cluster session and the netstate
// writer.
package acmestate

import (
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type Holder struct {
	mu          sync.Mutex
	state       *apigen.AcmeState
	subscribers map[chan *apigen.AcmeState]struct{}
}

func NewHolder() *Holder {
	return &Holder{subscribers: make(map[chan *apigen.AcmeState]struct{})}
}

func (h *Holder) Set(state *apigen.AcmeState) {
	if state == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != nil && state.Seq <= h.state.Seq {
		return
	}
	h.state = state
	for updates := range h.subscribers {
		select {
		case updates <- state:
		default:
			select {
			case <-updates:
			default:
			}
			updates <- state
		}
	}
}

func (h *Holder) Get() *apigen.AcmeState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *Holder) SnapshotAndSubscribe() (*apigen.AcmeState, <-chan *apigen.AcmeState, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	updates := make(chan *apigen.AcmeState, 1)
	h.subscribers[updates] = struct{}{}
	var once sync.Once
	return h.state, updates, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.subscribers, updates)
		})
	}
}

func Bindings(state *apigen.AcmeState) map[string]int32 {
	if state == nil {
		return nil
	}
	out := make(map[string]int32, len(state.CertBindings))
	for _, binding := range state.CertBindings {
		if binding == nil || binding.Hostname == "" || binding.SecretVersionID <= 0 {
			continue
		}
		out[binding.Hostname] = binding.SecretVersionID
	}
	return out
}
