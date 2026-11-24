package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const nodeAccessPolicyColumns = `id, data_blob, created_time, seq, event_time, author`

func scanNodeAccessPolicy(row scanner) (*NodeAccessPolicyEvent, error) {
	var e NodeAccessPolicyEvent
	var blob []byte
	if err := row.Scan(&e.NodeAccessPolicyID, &blob, &e.CreatedTime, &e.Seq, &e.EventTime, &e.Author); err != nil {
		return nil, err
	}
	value, err := apigen.DecodeNodeAccessPolicy(blob)
	if err != nil {
		return nil, err
	}
	e.Value = *value
	e.Value.ID = e.NodeAccessPolicyID
	return &e, nil
}

func (q *Queries) GetNodeAccessPolicy(ctx context.Context, id uint64) (*NodeAccessPolicyEvent, error) {
	return scanNodeAccessPolicy(q.db.QueryRowContext(ctx, `SELECT `+nodeAccessPolicyColumns+` FROM node_access_policies WHERE id = ?`, id))
}

func (q *Queries) ListNodeAccessPolicies(ctx context.Context) ([]*NodeAccessPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+nodeAccessPolicyColumns+` FROM node_access_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NodeAccessPolicyEvent
	for rows.Next() {
		e, err := scanNodeAccessPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) NodeAccessPolicyValues(ctx context.Context) ([]apigen.NodeAccessPolicy, error) {
	rows, err := q.ListNodeAccessPolicies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.NodeAccessPolicy, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Value)
	}
	return out, nil
}

func (q *Queries) reduceNodeAccessPolicy(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, p *apigen.NodeAccessPolicy) error {
	if p == nil {
		return fmt.Errorf("payload has no policy")
	}
	value := *p
	value.ID = 0
	return q.upsert(ctx, meta, `INSERT INTO node_access_policies (id, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, notNullBlob(value.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteNodeAccessPolicyRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM node_access_policies WHERE id = ?`, id)
	return err
}
