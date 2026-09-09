package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) InsertNetworkPolicyEvent(ctx context.Context, e *apigen.NetworkPolicyEvent) error {
	blob := e.Value.Encode()
	if blob == nil {
		blob = []byte{}
	}
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO network_policy_event_log (
			global_seq, event_time, created_time, author, policy_id, version, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id,global_seq,event_time,created_time,author,policy_id,version,data_blob,event_type`,
		e.Seq, e.EventTime, e.CreatedTime, e.Author, e.NetworkPolicyID, e.Version, blob, e.EventType)
	written, err := scanNetworkPolicyEvent(row)
	if err != nil {
		return err
	}
	*e = *written
	return nil
}
