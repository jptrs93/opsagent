// Package netmapstate holds the node's current accepted cluster network map,
// shared between the cluster session and the netstate writer.
package netmapstate

import (
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type Holder struct {
	mu          sync.Mutex
	state       *apigen.ClusterNetMap
	subscribers map[chan *apigen.ClusterNetMap]struct{}
}

func NewHolder() *Holder {
	return &Holder{subscribers: make(map[chan *apigen.ClusterNetMap]struct{})}
}

func (h *Holder) Set(state *apigen.ClusterNetMap) {
	if state == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
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

func (h *Holder) Get() *apigen.ClusterNetMap {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *Holder) SnapshotAndSubscribe() (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	updates := make(chan *apigen.ClusterNetMap, 1)
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
