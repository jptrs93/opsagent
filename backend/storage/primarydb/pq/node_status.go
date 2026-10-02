package pq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const nodeStatusColumns = `node_id, updated_at, is_connected, last_connected_at, opendeploy_version, remote_address, runtime_versions`

type nodeStatusEnvelope struct {
	rowEnvelope
	Status *apigen.NodeStatus
}

func scanNodeStatusInto(st *apigen.NodeStatus, extra []any, row scanner) error {
	var nodeID, updatedAt, connected, connectedAt int64
	dest := []any{&nodeID, &updatedAt, &connected, &connectedAt, &st.OpendeployVersion, &st.RemoteAddress, &st.RuntimeVersions}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	st.NodeID = int32(nodeID)
	st.UpdatedAt = nanosToClock(updatedAt)
	st.IsConnected = connected != 0
	st.LastConnectedAt = millisToTime(connectedAt)
	return nil
}

func (q *Queries) GetLatestNodeStatus(ctx context.Context, nodeID int32) (*apigen.NodeStatus, error) {
	var st apigen.NodeStatus
	if err := scanNodeStatusInto(&st, nil, q.db.QueryRowContext(ctx, `SELECT `+nodeStatusColumns+` FROM node_status WHERE node_id = ?`, nodeID)); err != nil {
		return nil, err
	}
	return &st, nil
}

func (q *Queries) listNodeStatusRows(ctx context.Context) ([]nodeStatusEnvelope, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+nodeStatusColumns+`, seq, event_time, author, created_time FROM node_status ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nodeStatusEnvelope
	for rows.Next() {
		var st apigen.NodeStatus
		var env rowEnvelope
		if err := scanNodeStatusInto(&st, []any{&env.Seq, &env.EventTime, &env.Author, &env.CreatedTime}, rows); err != nil {
			return nil, err
		}
		out = append(out, nodeStatusEnvelope{rowEnvelope: env, Status: &st})
	}
	return out, rows.Err()
}

func (q *Queries) ListLatestNodeStatuses(ctx context.Context) ([]*apigen.NodeStatus, error) {
	rows, err := q.listNodeStatusRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.NodeStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Status)
	}
	return out, nil
}

// ListNodeStatusHistorySince is a node's observed history after since,
// oldest first, read from the write log.
func (q *Queries) ListNodeStatusHistorySince(ctx context.Context, nodeID int32, since time.Time) ([]*apigen.NodeStatus, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? AND op != ? ORDER BY seq, idx`,
		int64(apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS), int64(nodeID), int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NodeStatus
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil || entity.NodeStatus == nil {
			return nil, fmt.Errorf("node %d status: %v", nodeID, err)
		}
		if st := entity.NodeStatus; st.UpdatedAt.After(since) {
			out = append(out, st)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out, nil
}

func (q *Queries) latestNodeStatusOrEmpty(ctx context.Context, nodeID int32) (*apigen.NodeStatus, error) {
	st, err := q.GetLatestNodeStatus(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return &apigen.NodeStatus{NodeID: nodeID}, nil
	}
	return st, err
}

// NodeConnectionStatus is the node's status after a connect or disconnect,
// its clock advanced past the stored one. Nothing is written.
func (q *Queries) NodeConnectionStatus(ctx context.Context, identifier string, connected bool, connectedAt time.Time) (*apigen.NodeStatus, error) {
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
	return st, nil
}

// NodeObservedMetaStatus is the node's status after a hello: connected now,
// with the version and address it reported. Nothing is written.
func (q *Queries) NodeObservedMetaStatus(ctx context.Context, nodeID int32, connectedAt time.Time, opendeployVersion, remoteAddress string) (*apigen.NodeStatus, error) {
	previous, err := q.latestNodeStatusOrEmpty(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	st := &apigen.NodeStatus{NodeID: nodeID, UpdatedAt: previous.UpdatedAt, LastConnectedAt: connectedAt, IsConnected: true, OpendeployVersion: opendeployVersion, RemoteAddress: remoteAddress, RuntimeVersions: previous.RuntimeVersions}
	st.BumpUpdatedAt()
	return st, nil
}

// reduceNodeStatus keeps the report with the greater clock. A stale report
// changes nothing and its meta is the row's.
func (q *Queries) reduceNodeStatus(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, st *apigen.NodeStatus) error {
	if st == nil {
		return fmt.Errorf("payload has no status")
	}
	if err := q.upsert(ctx, meta, `INSERT INTO node_status (node_id, updated_at, is_connected, last_connected_at, opendeploy_version, remote_address, runtime_versions, seq, event_time, author, created_time)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id) DO UPDATE SET updated_at = excluded.updated_at, is_connected = excluded.is_connected, last_connected_at = excluded.last_connected_at,
  opendeploy_version = excluded.opendeploy_version, remote_address = excluded.remote_address, runtime_versions = excluded.runtime_versions,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
WHERE excluded.updated_at >= node_status.updated_at
RETURNING created_time`,
		id, clockToNanos(st.UpdatedAt), boolToInt(st.IsConnected), timeToMillis(st.LastConnectedAt), st.OpendeployVersion, st.RemoteAddress, st.RuntimeVersions,
		env.Seq, env.EventTime, env.Author, env.EventTime); err != nil {
		return err
	}
	return q.rowMetaIfStale(ctx, meta, apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS, id)
}

func (q *Queries) deleteNodeStatusRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM node_status WHERE node_id = ?`, id)
	return err
}
