package pq

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

const nodeEventColumns = `id, global_seq, event_time, created_time, author, node_id, version, name, identifier, enrolled_time, status, roles, addresses, wg_public_key, allowed_spaces, event_type, host_addresses, enrollment_requested_at`

func scanNodeEvent(row scanner) (*apigen.NodeEvent, error) {
	var e apigen.NodeEvent
	var roles, addresses, allowed, hosts string
	if err := row.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.NodeID, &e.Version, &e.Value.Operator.Name, &e.Value.Reported.Identifier, &e.Value.Operator.EnrolledTime, &e.Value.Status, &roles, &addresses, &e.Value.Reported.WgPublicKey, &allowed, &e.EventType, &hosts, &e.Value.EnrollmentRequestedAt); err != nil {
		return nil, err
	}
	decodeNodeLists(&e, roles, addresses, allowed, hosts)
	e.Value.CreatedTime = e.CreatedTime
	return &e, nil
}
