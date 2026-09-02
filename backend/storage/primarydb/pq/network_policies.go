package pq

import (
	"context"
)

func (q *Queries) InsertNetworkPolicyEvent(ctx context.Context, e NetworkPolicyEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO network_policy_event_log (
			global_seq, event_time, created_time, author, policy_id, version, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.PolicyID, e.Version, e.DataBlob, e.EventType)
	return err
}
