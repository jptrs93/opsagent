package nodepublisher

// Applied-stamp tracking. A published map is not in force the moment it is
// sent: each secondary has to accept it durably and program its kernel. Anything
// that must not happen until the cluster agrees on the new routing — retiring a
// draining placement, above all — waits for the write sequence that encodes it
// to be applied everywhere rather than guessing at a propagation delay.

// RecordApplied notes how far one node has applied, as the derived_from_seq
// stamp of the newest map it reports cleanly applied. Secondaries report this
// unprompted after accepting each map, and the primary's in-process applier
// records after reconciling its own targeted map, so the barrier advances on
// its own.
func (p *Publisher) RecordApplied(nodeID uint64, appliedSeq int64) {
	p.ackMu.Lock()
	if current, ok := p.applied[nodeID]; ok && current >= appliedSeq {
		p.ackMu.Unlock()
		return
	}
	p.applied[nodeID] = appliedSeq
	p.ackMu.Unlock()
	notifyAck(p.ackUpdates)
}

// ForgetNode drops a secondary's acknowledgement when its session ends. A
// disconnected secondary cannot hold the barrier: it is served a complete snapshot
// on reconnect, so it has no way to keep acting on a map it never applied.
func (p *Publisher) ForgetNode(nodeID uint64) {
	p.ackMu.Lock()
	_, tracked := p.applied[nodeID]
	delete(p.applied, nodeID)
	p.ackMu.Unlock()
	if tracked {
		notifyAck(p.ackUpdates)
	}
}

// DecisionInForce reports whether the routing implied by the sequenced write
// at seq has been rendered and applied by every node whose map it changed.
//
// Two conditions compose it. First, a render must have seen state@seq: until
// then the maps may predate the decision entirely. Second, every reporting
// node whose map is stamped at or after seq must have applied that stamp. A
// node whose map kept an older stamp was not changed by the decision, and a
// render at seq that changed no map satisfies the wait immediately — that is
// what makes a same-node rollover need no propagation wait, with no special
// case.
//
// Nodes that have never reported are not counted. A node only reports after
// accepting a map, so one that has never reported holds no routes at all and
// therefore cannot be routing to a placement the barrier is trying to retire.
// Counting them would wedge the barrier behind any node that connects without
// network state.
func (p *Publisher) DecisionInForce(seq int64) bool {
	p.mu.Lock()
	rendered := p.appliedSeq >= seq
	stamps := make(map[uint64]int64, len(p.maps))
	for nodeID, entry := range p.maps {
		if entry.current.DerivedFromSeq >= seq {
			stamps[nodeID] = entry.current.DerivedFromSeq
		}
	}
	p.mu.Unlock()
	if !rendered {
		return false
	}
	p.ackMu.Lock()
	defer p.ackMu.Unlock()
	for nodeID, stamp := range stamps {
		if applied, ok := p.applied[nodeID]; ok && applied < stamp {
			return false
		}
	}
	return true
}

// AckUpdates signals that applied sequences have advanced. It is a single
// coalescing slot for one consumer, not a broadcast: a missed wakeup is
// harmless because consumers re-read DecisionInForce on every tick.
func (p *Publisher) AckUpdates() <-chan struct{} { return p.ackUpdates }

func notifyAck(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
