package netmappublisher

// Applied-sequence tracking. A published map is not in force the moment it is
// sent: each worker has to accept it durably and program its kernel. Anything
// that must not happen until the cluster agrees on the new routing — retiring a
// draining placement, above all — waits for the sequence that encodes it to be
// applied everywhere rather than guessing at a propagation delay.

// RecordApplied notes how far one connected worker has applied. Workers report
// this unprompted after accepting each map, so the barrier advances on its own.
func (p *Publisher) RecordApplied(nodeID int32, appliedSequence int64) {
	if nodeID <= 0 {
		return
	}
	p.ackMu.Lock()
	if current, ok := p.applied[nodeID]; ok && current >= appliedSequence {
		p.ackMu.Unlock()
		return
	}
	p.applied[nodeID] = appliedSequence
	p.ackMu.Unlock()
	notifyAck(p.ackUpdates)
}

// ForgetNode drops a worker's acknowledgement when its session ends. A
// disconnected worker cannot hold the barrier: it is served a complete snapshot
// on reconnect, so it has no way to keep acting on a map it never applied.
func (p *Publisher) ForgetNode(nodeID int32) {
	p.ackMu.Lock()
	_, tracked := p.applied[nodeID]
	delete(p.applied, nodeID)
	p.ackMu.Unlock()
	if tracked {
		notifyAck(p.ackUpdates)
	}
}

// AppliedEverywhere reports whether every worker known to hold a map has
// applied at least the given sequence.
//
// Workers that have never reported are not counted. A worker only reports after
// accepting a map, so one that has never reported holds no routes at all and
// therefore cannot be routing to a placement the barrier is trying to retire.
// Counting them would wedge the barrier behind any node that connects without
// network state.
func (p *Publisher) AppliedEverywhere(sequence int64) bool {
	p.ackMu.Lock()
	defer p.ackMu.Unlock()
	for _, applied := range p.applied {
		if applied < sequence {
			return false
		}
	}
	return true
}

// CurrentSequence returns the sequence of the map that encodes the current
// derived state. Callers pair it with AppliedEverywhere to wait for their own
// decision to take effect; when a decision changes no routes, Refresh mints no
// new sequence and the wait is already satisfied. That is what makes a
// same-node rollover need no propagation wait, with no special case for it.
func (p *Publisher) CurrentSequence() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return 0
	}
	return p.current.Sequence
}

// AckUpdates signals that applied sequences have advanced. It is a single
// coalescing slot for one consumer, not a broadcast: a missed wakeup is
// harmless because consumers re-read AppliedEverywhere on every tick.
func (p *Publisher) AckUpdates() <-chan struct{} { return p.ackUpdates }

func notifyAck(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
