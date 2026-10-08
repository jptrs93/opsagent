package pq

import (
	"context"
)

type NodeNetmap struct {
	NodeID      uint64
	DerivedSeq  int64
	ContentHash string
}

const listNodeNetmaps = `SELECT node_id, derived_seq, content_hash FROM node_netmap ORDER BY node_id`

func (q *Queries) ListNodeNetmaps(ctx context.Context) ([]NodeNetmap, error) {
	rows, err := q.db.QueryContext(ctx, listNodeNetmaps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []NodeNetmap
	for rows.Next() {
		var i NodeNetmap
		if err := rows.Scan(&i.NodeID, &i.DerivedSeq, &i.ContentHash); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const upsertNodeNetmap = `INSERT INTO node_netmap (node_id, derived_seq, content_hash) VALUES (?, ?, ?)
ON CONFLICT (node_id) DO UPDATE SET derived_seq = excluded.derived_seq, content_hash = excluded.content_hash`

func (q *Queries) UpsertNodeNetmap(ctx context.Context, arg NodeNetmap) error {
	_, err := q.db.ExecContext(ctx, upsertNodeNetmap, arg.NodeID, arg.DerivedSeq, arg.ContentHash)
	return err
}

const deleteNodeNetmap = `DELETE FROM node_netmap WHERE node_id = ?`

func (q *Queries) DeleteNodeNetmap(ctx context.Context, nodeID uint64) error {
	_, err := q.db.ExecContext(ctx, deleteNodeNetmap, nodeID)
	return err
}
