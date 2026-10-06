package nodes

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestNodeObservationHistorySurvivesRestartAndClockRegression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	s := state.Open(path)
	node := testNode(s, "primary")
	future := time.Now().Add(24 * time.Hour).UnixNano()
	if err := s.Commit(context.Background(), nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		return pq.NewUpdate(pq.NodeStatusMutation(seq, time.Now().UnixMilli(), &apigen.NodeStatus{NodeID: node.ID, UpdatedAt: apigen.TimeOf(time.Unix(0, future)), IsConnected: true})), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = state.Open(path)
	defer s.Close()
	seq := globalSeq(t, s)
	SetNodeStatusByIdentifier(s, node.Identifier, false, time.Time{})
	SetNodeStatusByIdentifier(s, node.Identifier, true, time.Now())
	history := erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{}))
	if len(history) != 3 || !history[0].IsConnected || history[1].IsConnected || !history[2].IsConnected {
		t.Fatalf("connection history = %+v", history)
	}
	for i, status := range history {
		if status.UpdatedAt.Value.UnixNano() != future+int64(i) {
			t.Fatalf("clock %d = %v", i, status.UpdatedAt)
		}
	}
	latest := erru.Must(s.Queries().ListLatestNodeStatuses(context.Background()))
	if globalSeq(t, s) != seq+2 || len(latest) != 1 || !bytes.Equal(latest[0].Encode(), history[2].Encode()) {
		t.Fatalf("latest observation = %+v", latest)
	}
	if got := erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, history[0].UpdatedAt.Value)); len(got) != 2 {
		t.Fatalf("history since = %+v", got)
	}
}
