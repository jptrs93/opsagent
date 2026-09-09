package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const nodeStatusColumns = `node_id, updated_at, last_connected_at, is_connected, opendeploy_version, remote_address`

func nodeStatusFrom(nodeID, updatedAt, lastConnectedAt, isConnected int64, opendeployVersion, remoteAddress string) *apigen.NodeStatus {
	return &apigen.NodeStatus{
		NodeID:            int32(nodeID),
		UpdatedAt:         nanosToClock(updatedAt),
		LastConnectedAt:   millisToTime(lastConnectedAt),
		IsConnected:       isConnected != 0,
		OpendeployVersion: opendeployVersion,
		RemoteAddress:     remoteAddress,
	}
}

func scanNodeStatus(row scanner) (*apigen.NodeStatus, error) {
	var nodeID, updatedAt, lastConnectedAt, isConnected int64
	var opendeployVersion, remoteAddress string
	if err := row.Scan(&nodeID, &updatedAt, &lastConnectedAt, &isConnected, &opendeployVersion, &remoteAddress); err != nil {
		return nil, err
	}
	return nodeStatusFrom(nodeID, updatedAt, lastConnectedAt, isConnected, opendeployVersion, remoteAddress), nil
}

func (q *Queries) queryNodeStatuses(ctx context.Context, query string, args ...any) ([]*apigen.NodeStatus, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NodeStatus
	for rows.Next() {
		st, err := scanNodeStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (q *Queries) GetLatestNodeStatus(ctx context.Context, nodeID int32) (*apigen.NodeStatus, error) {
	return scanNodeStatus(q.db.QueryRowContext(ctx, `SELECT `+nodeStatusColumns+` FROM node_status_log WHERE node_id = ? ORDER BY updated_at DESC LIMIT 1`, nodeID))
}

func (q *Queries) ListLatestNodeStatuses(ctx context.Context) ([]*apigen.NodeStatus, error) {
	return q.queryNodeStatuses(ctx, `SELECT status.node_id, status.updated_at, status.last_connected_at, status.is_connected, status.opendeploy_version, status.remote_address
 FROM node_status_log status
 JOIN (SELECT node_id, MAX(updated_at) AS updated_at FROM node_status_log GROUP BY node_id) latest
 ON latest.node_id = status.node_id AND latest.updated_at = status.updated_at
 ORDER BY status.node_id`)
}

func (q *Queries) ListNodeStatusHistorySince(ctx context.Context, nodeID int32, since time.Time) ([]*apigen.NodeStatus, error) {
	return q.queryNodeStatuses(ctx, `SELECT `+nodeStatusColumns+` FROM node_status_log WHERE node_id = ? AND updated_at > ? ORDER BY updated_at`, nodeID, clockToNanos(since))
}

func (q *Queries) ListNodeStatusesAtSeq(ctx context.Context, seq int64) ([]*apigen.NodeStatus, error) {
	return q.queryNodeStatuses(ctx, `SELECT `+nodeStatusColumns+` FROM node_status_log WHERE global_seq = ? ORDER BY node_id, updated_at`, seq)
}
