package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanNetworkPolicyEvent(row scanner) (*apigen.NetworkPolicyEvent, error) {
	var e apigen.NetworkPolicyEvent
	var blob []byte
	if err := row.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.NetworkPolicyID, &e.Version, &blob, &e.EventType); err != nil {
		return nil, err
	}
	value, err := apigen.DecodeNetworkPolicy(blob)
	if err != nil {
		return nil, err
	}
	e.Value = *value
	return &e, nil
}

func (q *Queries) GetLatestNetworkPolicyEvent(ctx context.Context, id int64) (*apigen.NetworkPolicyEvent, error) {
	return scanNetworkPolicyEvent(q.db.QueryRowContext(ctx, `SELECT id, global_seq, event_time, created_time, author, policy_id, version,
       data_blob, event_type
FROM network_policy_event_log
WHERE policy_id = ?
ORDER BY version DESC LIMIT 1`, id))
}

func (q *Queries) ListLatestNetworkPolicyEvents(ctx context.Context) ([]*apigen.NetworkPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.policy_id,
       e.version, e.data_blob, e.event_type
FROM network_policy_event_log e
JOIN (SELECT policy_id, MAX(version) AS version
      FROM network_policy_event_log GROUP BY policy_id) latest
  ON latest.policy_id = e.policy_id AND latest.version = e.version
ORDER BY e.policy_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NetworkPolicyEvent
	for rows.Next() {
		event, err := scanNetworkPolicyEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (q *Queries) ListNetworkPolicyEventsAtSeq(ctx context.Context, id int64) ([]*apigen.NetworkPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT * FROM network_policy_event_log WHERE global_seq = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NetworkPolicyEvent
	for rows.Next() {
		event, err := scanNetworkPolicyEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (q *Queries) ListLatestLiveNetworkPolicyEvents(ctx context.Context) ([]*apigen.NetworkPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.policy_id,
       e.version, e.data_blob, e.event_type
FROM network_policy_event_log e
JOIN (SELECT policy_id, MAX(version) AS version
      FROM network_policy_event_log GROUP BY policy_id) latest
  ON latest.policy_id = e.policy_id AND latest.version = e.version
WHERE e.event_type != 3
ORDER BY e.policy_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NetworkPolicyEvent
	for rows.Next() {
		event, err := scanNetworkPolicyEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
