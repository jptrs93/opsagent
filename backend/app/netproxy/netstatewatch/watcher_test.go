package netstatewatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSnapshotAndSubscribeCoalescesUpdates(t *testing.T) {
	w := New("netstate.pb")
	snapshot, updates, unsubscribe := w.SnapshotAndSubscribe()
	defer unsubscribe()
	if snapshot != nil {
		t.Fatalf("initial snapshot = %+v, want nil", snapshot)
	}

	w.publish(&apigen.NetState{Seq: 1})
	if got := receiveSnapshot(t, updates); got.Seq != 1 {
		t.Fatalf("first update sequence = %d, want 1", got.Seq)
	}
	w.publish(&apigen.NetState{Seq: 2})
	w.publish(&apigen.NetState{Seq: 3})
	if got := receiveSnapshot(t, updates); got.Seq != 3 {
		t.Fatalf("coalesced update sequence = %d, want 3", got.Seq)
	}

	snapshot, _, unsubscribeSnapshot := w.SnapshotAndSubscribe()
	defer unsubscribeSnapshot()
	if snapshot == nil || snapshot.Seq != 3 {
		t.Fatalf("snapshot = %+v, want sequence 3", snapshot)
	}
}

func TestRunPublishesAtomicRenames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netstate.pb")
	writeState(t, path, 1)
	w := New(path)
	_, updates, unsubscribe := w.SnapshotAndSubscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	if got := receiveSnapshot(t, updates); got.Seq != 1 {
		t.Fatalf("initial update sequence = %d, want 1", got.Seq)
	}

	writeState(t, path, 2)
	if got := receiveSnapshot(t, updates); got.Seq != 2 {
		t.Fatalf("rename update sequence = %d, want 2", got.Seq)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run failed: %v", err)
	}
}

func receiveSnapshot(t *testing.T, updates <-chan *apigen.NetState) *apigen.NetState {
	t.Helper()
	select {
	case snapshot := <-updates:
		return snapshot
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for netstate update")
		return nil
	}
}

func writeState(t *testing.T, path string, seq int64) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, (&apigen.NetState{Seq: seq}).Encode(), 0o600); err != nil {
		t.Fatalf("writing temporary state: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("replacing state: %v", err)
	}
}
