package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) ListNodeEventsAtSeq(ctx context.Context, seq int64) ([]*apigen.NodeEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id,global_seq,event_time,created_time,author,node_id,version,name,identifier,enrolled_time,status,roles,addresses,wg_public_key,allowed_spaces,event_type,host_addresses,enrollment_requested_at FROM node_event_log WHERE global_seq = ? ORDER BY id`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.NodeEvent
	for rows.Next() {
		var e apigen.NodeEvent
		var roles, addresses, allowed, hosts string
		if err := rows.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.NodeID, &e.Version, &e.Value.Operator.Name, &e.Value.Reported.Identifier, &e.Value.Operator.EnrolledTime, &e.Value.Status, &roles, &addresses, &e.Value.Reported.WgPublicKey, &allowed, &e.EventType, &hosts, &e.Value.EnrollmentRequestedAt); err != nil {
			return nil, err
		}
		decodeNodeLists(&e, roles, addresses, allowed, hosts)
		out = append(out, &e)
	}
	return out, rows.Err()
}
