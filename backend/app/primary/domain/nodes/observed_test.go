package nodes

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestNodeObservationHistorySurvivesRestartAndClockRegression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	s := state.Open(path)
	node := testNode(s, "primary")
	future := time.Now().Add(24 * time.Hour).UnixNano()
	if err := s.Queries().InsertNodeStatus(context.Background(), 0, &apigen.NodeStatus{NodeID: node.ID, UpdatedAt: time.Unix(0, future), IsConnected: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = state.Open(path)
	defer s.Close()
	seq := s.BuildSnapshot(context.Background()).Seq
	SetNodeStatusByIdentifier(s, node.Identifier, false, time.Time{})
	SetNodeStatusByIdentifier(s, node.Identifier, true, time.Now())
	history := erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{}))
	if len(history) != 3 || !history[0].IsConnected || history[1].IsConnected || !history[2].IsConnected {
		t.Fatalf("connection history = %+v", history)
	}
	for i, status := range history {
		if status.UpdatedAt.UnixNano() != future+int64(i) {
			t.Fatalf("clock %d = %v", i, status.UpdatedAt)
		}
	}
	snapshot := s.BuildSnapshot(context.Background())
	if snapshot.Seq != seq+2 || len(snapshot.NodeStatuses) != 1 || !bytes.Equal(snapshot.NodeStatuses[0].Encode(), history[2].Encode()) {
		t.Fatalf("latest observation = %+v", snapshot.NodeStatuses)
	}
	if got := erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, history[0].UpdatedAt)); len(got) != 2 {
		t.Fatalf("history since = %+v", got)
	}
}
