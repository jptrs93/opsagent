package pq

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// CurrentNode keeps authored and observed data as separate API objects.
var MemberNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_UNHEALTHY),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_DRAINING),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_MISSING),
}

var EnrollmentNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUESTED),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_CANCELLED),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUEST_EXPIRED),
}

var AllNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUESTED),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_CANCELLED),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUEST_EXPIRED),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_UNHEALTHY),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_DRAINING),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_MISSING),
	int64(apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_EVICTED),
}

type CurrentNode struct {
	Event  NodeEvent
	Status apigen.NodeStatus
}

const nodeCurrentColumns = `n.id, n.name, n.identifier, n.status, n.roles, n.allowed_spaces, n.enrolled_time, n.enrollment_requested_at,
	n.underlay_address, n.wg_public_key, n.host_addresses, n.host_addresses_unknown, n.created_time, n.seq, n.event_time, n.author,
	COALESCE(ns.updated_at, 0), COALESCE(ns.is_connected, 0), COALESCE(ns.last_connected_at, 0), COALESCE(ns.opendeploy_version, ''), COALESCE(ns.remote_address, ''), COALESCE(ns.runtime_versions, '')`

const nodeCurrentFrom = `FROM nodes n LEFT JOIN node_status ns ON ns.node_id = n.id`

func scanCurrentNode(row scanner) (CurrentNode, error) {
	var r CurrentNode
	e, st := &r.Event, &r.Status
	var roles, allowed, hosts, underlay string
	var status, enrolledTime, requestedAt, hostsUnknown, updatedAt, connected, connectedAt int64
	if err := row.Scan(&e.NodeID, &e.Value.Operator.Name, &e.Value.Reported.Identifier, &status, &roles, &allowed, &enrolledTime, &requestedAt,
		&underlay, &e.Value.Reported.WgPublicKey, &hosts, &hostsUnknown, &e.CreatedTime, &e.Seq, &e.EventTime, &e.Author,
		&updatedAt, &connected, &connectedAt, &st.OpendeployVersion, &st.RemoteAddress, &st.RuntimeVersions); err != nil {
		return r, err
	}
	e.Value.ID = e.NodeID
	e.Value.Status = apigen.NodeLifecycleStatus(status)
	e.Value.Operator.EnrolledTime = millisToTime(enrolledTime)
	e.Value.EnrollmentRequestedAt = millisToTime(requestedAt)
	e.Value.Reported.UnderlayAddress = addressColumn(underlay)
	decodeNodeLists(e, roles, allowed, hosts)
	e.Value.Reported.HostAddressesUnknown = hostsUnknown != 0
	st.NodeID = e.NodeID
	st.IsConnected = connected != 0
	st.UpdatedAt = nanosToClock(updatedAt)
	st.LastConnectedAt = millisToTime(connectedAt)
	return r, nil
}

func addressColumn(s string) apigen.IpAddress {
	addr, err := apigen.ParseAddr(s)
	if err != nil {
		return apigen.IpAddress{}
	}
	return addr
}

func addressText(a apigen.IpAddress) string {
	if !a.Addr().IsValid() {
		return ""
	}
	return a.String()
}

func decodeNodeLists(e *NodeEvent, roles, allowed, hosts string) {
	_ = json.Unmarshal([]byte(roles), &e.Value.Operator.Roles)
	var hostList []string
	_ = json.Unmarshal([]byte(hosts), &hostList)
	e.Value.Reported.HostAddresses = nil
	for _, h := range hostList {
		if addr, err := apigen.ParseAddr(h); err == nil {
			e.Value.Reported.HostAddresses = append(e.Value.Reported.HostAddresses, addr)
		}
	}
	var spaces []uint64
	_ = json.Unmarshal([]byte(allowed), &spaces)
	e.Value.Operator.AllowedSpaces = normaliseAllowedSpaces(spaces)
}

func normaliseAllowedSpaces(spaces []uint64) []uint64 {
	out := []uint64{0}
	seen := map[uint64]bool{0: true}
	for _, id := range spaces {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

func jsonList[T any](items []T) string {
	if items == nil {
		items = []T{}
	}
	b, _ := json.Marshal(items)
	return string(b)
}

func hostAddressesJSON(addrs []apigen.IpAddress) string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if s := addressText(a); s != "" {
			out = append(out, s)
		}
	}
	return jsonList(out)
}

func statusPlaceholders(statuses []int64) (string, []any) {
	marks := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, status := range statuses {
		marks[i] = "?"
		args[i] = status
	}
	return strings.Join(marks, ", "), args
}

func (q *Queries) GetNodeRowByID(ctx context.Context, id uint64) (CurrentNode, error) {
	return scanCurrentNode(q.db.QueryRowContext(ctx, `SELECT `+nodeCurrentColumns+` `+nodeCurrentFrom+` WHERE n.id = ?`, id))
}

func (q *Queries) GetNodeRowByIdentifier(ctx context.Context, identifier string) (CurrentNode, error) {
	return scanCurrentNode(q.db.QueryRowContext(ctx, `SELECT `+nodeCurrentColumns+` `+nodeCurrentFrom+` WHERE n.identifier = ?`, identifier))
}

// NextEnrollmentRequestedAt preserves request identity even when two requests
// start in one millisecond or the primary's wall clock moves backwards: it is
// above every request time the node's history carries. Times are epoch ms.
func (q *Queries) NextEnrollmentRequestedAt(ctx context.Context, nodeID uint64, now int64) (int64, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT payload FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? AND op != ?`,
		int64(apigen.CoreEntityType_CORE_ENTITY_NODE), nodeID, int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	at := now
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return 0, err
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil {
			return 0, err
		}
		if n := entity.Value.Node; n != nil && n.EnrollmentRequestedAt.Present {
			at = max(at, n.EnrollmentRequestedAt.Value.UnixMilli()+1)
		}
	}
	return at, rows.Err()
}

// NewNode allocates a node id and returns the node as the create write will
// materialise it: created now, author system, allowed every space. Nothing is
// written.
func (q *Queries) NewNode(ctx context.Context, seq, now int64, node apigen.Node) (CurrentNode, error) {
	id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NODE)
	if err != nil {
		return CurrentNode{}, err
	}
	spaces, err := q.ListSpaces(ctx)
	if err != nil {
		return CurrentNode{}, err
	}
	allowed := make([]uint64, 0, len(spaces))
	for _, sp := range spaces {
		allowed = append(allowed, sp.ID)
	}
	node.ID = id
	node.Operator.AllowedSpaces = normaliseAllowedSpaces(allowed)
	return CurrentNode{
		Event:  NodeEvent{NodeID: id, Seq: seq, CreatedTime: now, EventTime: now, Value: node},
		Status: apigen.NodeStatus{NodeID: id},
	}, nil
}

func (q *Queries) CountNodesWithName(ctx context.Context, name string, excludeNodeID uint64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE name = ? AND id != ?`, name, excludeNodeID).Scan(&n)
	return n, err
}

func (q *Queries) queryNodeRows(ctx context.Context, where, order string, args ...any) ([]CurrentNode, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+nodeCurrentColumns+` `+nodeCurrentFrom+` `+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CurrentNode
	for rows.Next() {
		r, err := scanCurrentNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) ListNodeRows(ctx context.Context, statuses []int64) ([]CurrentNode, error) {
	marks, args := statusPlaceholders(statuses)
	return q.queryNodeRows(ctx, `WHERE n.status IN (`+marks+`)`, `n.id`, args...)
}

func (q *Queries) ListEnrollmentNodeRows(ctx context.Context, enrollmentStatuses []int64) ([]CurrentNode, error) {
	marks, args := statusPlaceholders(enrollmentStatuses)
	return q.queryNodeRows(ctx, `WHERE n.status IN (`+marks+`) OR n.enrollment_requested_at != 0`, `n.created_time DESC, n.id DESC`, args...)
}

func (q *Queries) GetNodeIDByIdentifier(ctx context.Context, identifier string) (uint64, error) {
	var nodeID uint64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM nodes WHERE identifier = ?`, identifier).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) GetNodeIDByIdentifierWithStatus(ctx context.Context, identifier string, statuses []int64) (uint64, error) {
	marks, args := statusPlaceholders(statuses)
	var nodeID uint64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM nodes WHERE identifier = ? AND status IN (`+marks+`)`, append([]any{identifier}, args...)...).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) GetNodeIDWithRole(ctx context.Context, role apigen.NodeRole, statuses []int64) (uint64, error) {
	marks, args := statusPlaceholders(statuses)
	var nodeID uint64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM nodes WHERE status IN (`+marks+`) AND EXISTS (SELECT 1 FROM json_each(roles) WHERE value = ?) ORDER BY id LIMIT 1`,
		append(args, int64(role))...).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) reduceNode(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, n *apigen.Node) error {
	if n == nil {
		return fmt.Errorf("payload has no node")
	}
	return q.upsert(ctx, meta, `INSERT INTO nodes (id, name, identifier, status, roles, allowed_spaces, enrolled_time, enrollment_requested_at,
  underlay_address, wg_public_key, host_addresses, host_addresses_unknown, created_time, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, identifier = excluded.identifier, status = excluded.status, roles = excluded.roles,
  allowed_spaces = excluded.allowed_spaces, enrolled_time = excluded.enrolled_time, enrollment_requested_at = excluded.enrollment_requested_at,
  underlay_address = excluded.underlay_address, wg_public_key = excluded.wg_public_key, host_addresses = excluded.host_addresses,
  host_addresses_unknown = excluded.host_addresses_unknown, seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, n.Operator.Name, n.Reported.Identifier, int64(n.Status), jsonList(n.Operator.Roles), jsonList(n.Operator.AllowedSpaces),
		timeToMillis(n.Operator.EnrolledTime), timeToMillis(n.EnrollmentRequestedAt),
		addressText(n.Reported.UnderlayAddress), n.Reported.WgPublicKey, hostAddressesJSON(n.Reported.HostAddresses), boolToInt(n.Reported.HostAddressesUnknown), env.EventTime,
		env.Seq, env.EventTime, env.Author)
}

func (q *Queries) deleteNodeRows(ctx context.Context, id uint64) error {
	if err := q.deleteNodeStatusRow(ctx, id); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	return err
}
