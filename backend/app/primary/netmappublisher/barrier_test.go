package netmappublisher

import (
	"testing"
	"time"
)

func newTestBarrier() *Publisher {
	return &Publisher{applied: make(map[int32]int64), ackUpdates: make(chan struct{}, 1)}
}

func TestBarrierWaitsForEveryReportingNode(t *testing.T) {
	p := newTestBarrier()
	p.RecordApplied(1, 5)
	p.RecordApplied(2, 5)

	if !p.AppliedEverywhere(5) {
		t.Fatal("sequence 5 should be applied everywhere")
	}
	if p.AppliedEverywhere(6) {
		t.Fatal("sequence 6 is not applied anywhere yet")
	}

	p.RecordApplied(1, 6)
	if p.AppliedEverywhere(6) {
		t.Fatal("one node at sequence 6 is not everywhere")
	}
	p.RecordApplied(2, 6)
	if !p.AppliedEverywhere(6) {
		t.Fatal("both nodes reached sequence 6")
	}
}

// TestBarrierIgnoresSilentNodes covers the wedge this would otherwise create. A
// node that has never reported holds no accepted map at all, so it cannot be
// routing to the placement the barrier is trying to retire; counting it would
// stall every rollover behind any node without network state.
func TestBarrierIgnoresSilentNodes(t *testing.T) {
	p := newTestBarrier()
	if !p.AppliedEverywhere(7) {
		t.Fatal("a cluster with no reported maps must not hold the barrier")
	}
	p.RecordApplied(1, 7)
	if !p.AppliedEverywhere(7) {
		t.Fatal("the only reporting node is caught up")
	}
}

// TestBarrierReleasesOnDisconnect covers a node that goes away mid-drain. It is
// served a complete snapshot on reconnect, so it cannot still be acting on a
// map it never applied, and must not pin the placement in the meantime.
func TestBarrierReleasesOnDisconnect(t *testing.T) {
	p := newTestBarrier()
	p.RecordApplied(1, 9)
	p.RecordApplied(2, 3)
	if p.AppliedEverywhere(9) {
		t.Fatal("node 2 is behind")
	}
	p.ForgetNode(2)
	if !p.AppliedEverywhere(9) {
		t.Fatal("a disconnected node must not hold the barrier")
	}
}

func TestBarrierDoesNotRegress(t *testing.T) {
	p := newTestBarrier()
	p.RecordApplied(1, 9)
	// Reconnect replays an older status; it must not undo progress.
	p.RecordApplied(1, 2)
	if !p.AppliedEverywhere(9) {
		t.Fatal("an older report rolled the applied sequence backwards")
	}
}

func TestBarrierNotifiesWithoutBlocking(t *testing.T) {
	p := newTestBarrier()
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
