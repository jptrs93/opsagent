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
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
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

func TestLegacyNodeObservationMigratesOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	s := state.Open(path)
	node := testNode(s, "primary")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := sqlitedb.MustOpen(path)
	_, err := db.Exec(`INSERT INTO node_statuses (node_id, observed_at, last_connected_at, is_connected, opendeploy_version, remote_address) VALUES (?, 1000, 900, 1, 'old', '192.0.2.1')`, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s = state.Open(path)
	history := erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{}))
	if len(history) != 1 || history[0].UpdatedAt.UnixNano() != 1000000000 || history[0].OpendeployVersion != "old" {
		t.Fatalf("migration = %+v", history)
	}
	UpdateNodeObservedMeta(s, node.Identifier, "192.0.2.2", "new")
	s.Close()
	s = state.Open(path)
	defer s.Close()
	if history = erru.Must(s.Queries().ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{})); len(history) != 2 || history[1].OpendeployVersion != "new" {
		t.Fatalf("history after restart = %+v", history)
	}
	db = sqlitedb.MustOpen(path)
	defer db.Close()
	var legacy string
	if err := db.QueryRow(`SELECT opendeploy_version FROM node_statuses WHERE node_id = ?`, node.ID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "old" {
		t.Fatalf("legacy row was written: %q", legacy)
	}
}
