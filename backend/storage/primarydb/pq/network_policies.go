package pq

import (
	"context"
)

type NetworkPolicyEvent struct {
	ID          int64
	GlobalSeq   int64
	EventTime   int64
	CreatedTime int64
	Author      int64
	PolicyID    int64
	Version     int64
	DataBlob    []byte
	EventType   int64
}

const networkPolicyEventColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.policy_id, e.version, e.data_blob, e.event_type`

func scanNetworkPolicyEvent(scan func(dest ...any) error) (NetworkPolicyEvent, error) {
	var e NetworkPolicyEvent
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.PolicyID, &e.Version, &e.DataBlob, &e.EventType)
	return e, err
}

func (q *Queries) NextNetworkPolicyID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(policy_id), 0) + 1 FROM network_policy_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestNetworkPolicyEvent(ctx context.Context, policyID int64) (NetworkPolicyEvent, error) {
	return scanNetworkPolicyEvent(q.db.QueryRowContext(ctx, `
		SELECT `+networkPolicyEventColumns+`
		FROM network_policy_event_log e
		WHERE e.policy_id = ?
		ORDER BY e.version DESC LIMIT 1`, policyID).Scan)
}

func (q *Queries) ListLatestNetworkPolicyEvents(ctx context.Context) ([]NetworkPolicyEvent, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+networkPolicyEventColumns+`
		FROM network_policy_event_log e
		JOIN (SELECT policy_id, MAX(version) AS version
		      FROM network_policy_event_log GROUP BY policy_id) latest
		  ON latest.policy_id = e.policy_id AND latest.version = e.version
		ORDER BY e.policy_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkPolicyEvent
	for rows.Next() {
		e, err := scanNetworkPolicyEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) InsertNetworkPolicyEvent(ctx context.Context, e NetworkPolicyEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO network_policy_event_log (
			global_seq, event_time, created_time, author, policy_id, version, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.PolicyID, e.Version, e.DataBlob, e.EventType)
	return err
}
