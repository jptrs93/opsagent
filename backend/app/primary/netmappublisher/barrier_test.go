package netmappublisher

import (
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func newTestBarrier(currentStamp, lastRenderedSeq int64) *Publisher {
	p := &Publisher{applied: make(map[int32]int64), ackUpdates: make(chan struct{}, 1)}
	p.lastRenderedSeq = lastRenderedSeq
	if currentStamp > 0 {
		p.current = &apigen.ClusterNetMap{DerivedFromSeq: currentStamp}
	}
	return p
}

func TestBarrierWaitsForEveryReportingNode(t *testing.T) {
	p := newTestBarrier(6, 6)
	p.RecordApplied(1, 5)
	p.RecordApplied(2, 5)

	if p.DecisionInForce(6) {
		t.Fatal("no node has applied the current map yet")
	}
	p.RecordApplied(1, 6)
	if p.DecisionInForce(6) {
		t.Fatal("one node at the current stamp is not everywhere")
	}
	p.RecordApplied(2, 6)
	if !p.DecisionInForce(6) {
		t.Fatal("both nodes applied the current map")
	}
}

// TestBarrierWaitsForRender covers the decision-to-map handoff. Until a render
// has seen the decision's write sequence, the current map may predate it
// entirely; once a render has seen it without changing the map, the map already
// in force encodes the decision and the wait collapses. That collapse is the
// same-node rollover shortcut.
func TestBarrierWaitsForRender(t *testing.T) {
	p := newTestBarrier(5, 5)
	p.RecordApplied(1, 5)
	if !p.DecisionInForce(5) {
		t.Fatal("rendered and applied decision should be in force")
	}
	if p.DecisionInForce(6) {
		t.Fatal("a decision newer than every render cannot be in force")
	}
	p.mu.Lock()
	p.lastRenderedSeq = 6
	p.mu.Unlock()
	if !p.DecisionInForce(6) {
		t.Fatal("a render that changed nothing should satisfy the decision immediately")
	}
}

// TestBarrierIgnoresSilentNodes covers the wedge this would otherwise create. A
// node that has never reported holds no accepted map at all, so it cannot be
// routing to the placement the barrier is trying to retire; counting it would
// stall every rollover behind any node without network state.
func TestBarrierIgnoresSilentNodes(t *testing.T) {
	p := newTestBarrier(7, 7)
	if !p.DecisionInForce(7) {
		t.Fatal("a cluster with no reported maps must not hold the barrier")
	}
	p.RecordApplied(1, 7)
	if !p.DecisionInForce(7) {
		t.Fatal("the only reporting node is caught up")
	}
}

// TestBarrierReleasesOnDisconnect covers a node that goes away mid-drain. It is
// served a complete snapshot on reconnect, so it cannot still be acting on a
// map it never applied, and must not pin the placement in the meantime.
func TestBarrierReleasesOnDisconnect(t *testing.T) {
	p := newTestBarrier(9, 9)
	p.RecordApplied(1, 9)
	p.RecordApplied(2, 3)
	if p.DecisionInForce(9) {
		t.Fatal("node 2 is behind")
	}
	p.ForgetNode(2)
	if !p.DecisionInForce(9) {
		t.Fatal("a disconnected node must not hold the barrier")
	}
}

func TestBarrierDoesNotRegress(t *testing.T) {
	p := newTestBarrier(9, 9)
	p.RecordApplied(1, 9)
	// Reconnect replays an older status; it must not undo progress.
	p.RecordApplied(1, 2)
	if !p.DecisionInForce(9) {
		t.Fatal("an older report rolled the applied stamp backwards")
	}
}

func TestBarrierNotifiesWithoutBlocking(t *testing.T) {
	p := newTestBarrier(0, 0)
	// More notifications than the single slot holds: coalescing must not block.
	done := make(chan struct{})
	go func() {
		for i := int64(1); i <= 50; i++ {
			p.RecordApplied(1, i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordApplied blocked on the notification slot")
	}
	select {
	case <-p.AckUpdates():
	default:
		t.Fatal("no wakeup queued after recording")
	}
}
