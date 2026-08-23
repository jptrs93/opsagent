package netstatewatch

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestPublishLogsAcceptedNetstateSummary(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	w := New("netstate.pb")
	state := &apigen.NetState{
		Seq:               7,
		NodeIdentifier:    "primary",
		DnsServices:       []*apigen.DnsService{{Endpoints: []*apigen.Endpoint{{}, {}}}},
		UpstreamResolvers: []string{"192.0.2.53"},
		Ingress:           []*apigen.NetIngress{{}},
	}
	w.publish(state)
	w.publish(state)

	got := output.String()
	for _, want := range []string{
		"netstate loaded",
		"seq=7",
		"node=primary",
		"dnsServices=1",
		"dnsEndpoints=2",
		"upstreamResolvers=1",
		"ingressRoutes=1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output %q does not contain %q", got, want)
		}
	}
	if count := strings.Count(got, "netstate loaded"); count != 1 {
		t.Errorf("netstate loaded log count = %d, want 1", count)
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
