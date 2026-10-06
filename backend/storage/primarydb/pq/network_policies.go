package pq

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const networkPolicyColumns = `id, data_blob, created_time, seq, event_time, author`

// scanNetworkPolicy reads one live policy as the view the API returns: the
// envelope of its last write, its creation time, and the policy, which is
// also its event stream payload.
func scanNetworkPolicy(row scanner) (*NetworkPolicyEvent, error) {
	var e NetworkPolicyEvent
	var blob []byte
	if err := row.Scan(&e.NetworkPolicyID, &blob, &e.CreatedTime, &e.Seq, &e.EventTime, &e.Author); err != nil {
		return nil, err
	}
	value, err := apigen.DecodeNetworkPolicy(blob)
	if err != nil {
		return nil, err
	}
	e.Value = *value
	e.Value.ID = e.NetworkPolicyID
	return &e, nil
}

// GetNetworkPolicy returns the live policy, or sql.ErrNoRows.
func (q *Queries) GetNetworkPolicy(ctx context.Context, id uint64) (*NetworkPolicyEvent, error) {
	return scanNetworkPolicy(q.db.QueryRowContext(ctx, `SELECT `+networkPolicyColumns+` FROM network_policies WHERE id = ?`, id))
}

func (q *Queries) ListNetworkPolicies(ctx context.Context) ([]*NetworkPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+networkPolicyColumns+` FROM network_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NetworkPolicyEvent
	for rows.Next() {
		e, err := scanNetworkPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) reduceNetworkPolicy(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, p *apigen.NetworkPolicy) error {
	if p == nil {
		return fmt.Errorf("payload has no policy")
	}
	value := *p
	value.ID = 0
	return q.upsert(ctx, meta, `INSERT INTO network_policies (id, data_blob, created_time, seq, event_time, author) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET data_blob = excluded.data_blob, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, notNullBlob(value.Encode()), env.EventTime, env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteNetworkPolicyRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM network_policies WHERE id = ?`, id)
	return err
}
