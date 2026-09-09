package pq

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) InsertNodeStatus(ctx context.Context, seq int64, st *apigen.NodeStatus) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO node_status_log
 (node_id, updated_at, global_seq, last_connected_at, is_connected, opendeploy_version, remote_address)
 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		st.NodeID, clockToNanos(st.UpdatedAt), seq, timeToMillis(st.LastConnectedAt), boolToInt(st.IsConnected), st.OpendeployVersion, st.RemoteAddress)
	return err
}

func (q *Queries) latestNodeStatusOrEmpty(ctx context.Context, nodeID int32) (*apigen.NodeStatus, error) {
	st, err := q.GetLatestNodeStatus(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apigen.NodeStatus{NodeID: nodeID}, nil
	}
	return st, err
}

func (q *Queries) SetNodeConnectionStatus(ctx context.Context, seq int64, identifier string, connected bool, connectedAt time.Time) (*apigen.NodeStatus, error) {
	nodeID, err := q.GetNodeIDByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	st, err := q.latestNodeStatusOrEmpty(ctx, int32(nodeID))
	if err != nil {
		return nil, err
	}
	st.BumpUpdatedAt()
	st.IsConnected = connected
	if connected {
		st.LastConnectedAt = connectedAt
	}
	return st, q.InsertNodeStatus(ctx, seq, st)
}

func (q *Queries) UpsertNodeObservedMeta(ctx context.Context, seq int64, nodeID int32, connectedAt time.Time, opendeployVersion, remoteAddress string) (*apigen.NodeStatus, error) {
	previous, err := q.latestNodeStatusOrEmpty(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	st := &apigen.NodeStatus{NodeID: nodeID, UpdatedAt: previous.UpdatedAt, LastConnectedAt: connectedAt, IsConnected: true, OpendeployVersion: opendeployVersion, RemoteAddress: remoteAddress}
	st.BumpUpdatedAt()
	return st, q.InsertNodeStatus(ctx, seq, st)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
