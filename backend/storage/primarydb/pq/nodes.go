package pq

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// CurrentNode keeps authored and observed data as separate API objects.
var MemberNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_UNHEALTHY),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_MISSING),
}

var EnrollmentNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED),
}

var AllNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_STATUS_UNKNOWN),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_UNHEALTHY),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_MISSING),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED),
}

type CurrentNode struct {
	Event  apigen.NodeEvent
	Status apigen.NodeStatus
}

const nodeCurrentColumns = `n.node_id, n.created_time, n.enrolled_time, n.name, n.identifier,
	n.version, n.status, n.roles, n.addresses, n.wg_public_key, n.allowed_spaces,
	COALESCE(ns.is_connected, 0), COALESCE(ns.opendeploy_version, ''), COALESCE(ns.remote_address, ''), n.enrollment_requested_at, n.host_addresses, n.id, n.global_seq, n.author, n.event_type, n.event_time, COALESCE(ns.updated_at, 0), COALESCE(ns.last_connected_at, 0)`

const nodeCurrentFrom = `FROM node_event_log n
	JOIN (SELECT node_id, MAX(version) AS version
	      FROM node_event_log GROUP BY node_id) latest
	  ON latest.node_id = n.node_id AND latest.version = n.version
	LEFT JOIN node_status_log ns ON ns.node_id = n.node_id
 AND ns.updated_at = (SELECT MAX(updated_at) FROM node_status_log WHERE node_id = n.node_id)`

const allSpaceIDsExpr = `COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]')`

func scanCurrentNode(row scanner) (CurrentNode, error) {
	var r CurrentNode
	e, st := &r.Event, &r.Status
	var roles, addresses, allowed, hosts string
	var connected, updatedAt, connectedAt int64
	if err := row.Scan(&e.NodeID, &e.CreatedTime, &e.Value.Operator.EnrolledTime, &e.Value.Operator.Name, &e.Value.Reported.Identifier,
		&e.Version, &e.Value.Status, &roles, &addresses, &e.Value.Reported.WgPublicKey, &allowed,
		&connected, &st.OpendeployVersion, &st.RemoteAddress, &e.Value.EnrollmentRequestedAt, &hosts,
		&e.EventID, &e.Seq, &e.Author, &e.EventType, &e.EventTime, &updatedAt, &connectedAt); err != nil {
		return r, err
	}
	decodeNodeLists(e, roles, addresses, allowed, hosts)
	st.NodeID = e.NodeID
	st.IsConnected = connected != 0
	st.UpdatedAt = nanosToClock(updatedAt)
	st.LastConnectedAt = millisToTime(connectedAt)
	return r, nil
}

func decodeNodeLists(e *apigen.NodeEvent, roles, addresses, allowed, hosts string) {
	_ = json.Unmarshal([]byte(roles), &e.Value.Operator.Roles)
	_ = json.Unmarshal([]byte(hosts), &e.Value.Reported.HostAddresses)
	var underlays []string
	_ = json.Unmarshal([]byte(addresses), &underlays)
	if len(underlays) > 0 {
		e.Value.Reported.UnderlayAddress = underlays[0]
	}
	var spaces []int32
	_ = json.Unmarshal([]byte(allowed), &spaces)
	e.Value.Operator.AllowedSpaces = []int32{0}
	seen := map[int32]bool{0: true}
	for _, id := range spaces {
		if id >= 0 && !seen[id] {
			e.Value.Operator.AllowedSpaces = append(e.Value.Operator.AllowedSpaces, id)
			seen[id] = true
		}
	}
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

func (q *Queries) GetNodeRowByID(ctx context.Context, id int64) (CurrentNode, error) {
	return scanCurrentNode(q.db.QueryRowContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.node_id = ?
		LIMIT 1`, id))
}

func (q *Queries) GetNodeRowByIdentifier(ctx context.Context, identifier string) (CurrentNode, error) {
	return scanCurrentNode(q.db.QueryRowContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.identifier = ?
		LIMIT 1`, identifier))
}

// NextEnrollmentRequestedAt preserves request identity even when two requests
// start in one millisecond or the primary's wall clock moves backwards.
func (q *Queries) NextEnrollmentRequestedAt(ctx context.Context, nodeID, now int64) (int64, error) {
	var at int64
	err := q.db.QueryRowContext(ctx, `SELECT MAX(?, COALESCE(MAX(enrollment_requested_at), 0) + 1)
		FROM node_event_log WHERE node_id = ?`, now, nodeID).Scan(&at)
	return at, err
}

type InsertNodeParams struct {
	HostAddressesJSON     string
	EnrollmentRequestedAt int64
	CreatedAt             int64
	EnrolledAt            int64
	Name                  string
	Identifier            string
	Status                int64
	RolesJSON             string
	AddressesJSON         string
	WgPublicKey           string
	GlobalSeq             int64
}

func (q *Queries) InsertNodeRow(ctx context.Context, p InsertNodeParams) (CurrentNode, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(node_id), 0) + 1 FROM node_event_log`).Scan(&nodeID)
	if err != nil {
		return CurrentNode{}, err
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO node_event_log (
			global_seq, event_time, created_time, author, node_id, version,
			name, identifier, enrolled_time, status, roles, addresses,
			wg_public_key, allowed_spaces, event_type, host_addresses, enrollment_requested_at)
		VALUES (?, ?, ?, 0, ?, 1, ?, ?, ?, ?, ?, ?, ?, `+allSpaceIDsExpr+`, ?, ?, ?)`,
		p.GlobalSeq, p.CreatedAt, p.CreatedAt, nodeID, p.Name, p.Identifier, p.EnrolledAt,
		p.Status, p.RolesJSON, p.AddressesJSON, p.WgPublicKey, EventCreate, normalizeHostAddressesJSON(p.HostAddressesJSON), p.EnrollmentRequestedAt)
	if err != nil {
		return CurrentNode{}, err
	}
	return q.GetNodeRowByID(ctx, nodeID)
}

type AppendNodeEventParams struct {
	HostAddressesJSON     string
	EnrollmentRequestedAt int64
	NodeID                int64
	EventTime             int64
	Author                int64
	Name                  string
	Identifier            string
	EnrolledTime          int64
	Status                int64
	RolesJSON             string
	AddressesJSON         string
	WgPublicKey           string
	AllowedSpacesJSON     string
	GlobalSeq             int64
}

func (q *Queries) AppendNodeEvent(ctx context.Context, p AppendNodeEventParams) (CurrentNode, error) {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_event_log (
			global_seq, event_time, created_time, author, node_id, version,
			name, identifier, enrolled_time, status, roles, addresses,
			wg_public_key, allowed_spaces, event_type, host_addresses, enrollment_requested_at)
		SELECT ?1, ?2, COALESCE(MIN(created_time), ?2), ?3,
		       ?4, COALESCE(MAX(version), 0) + 1,
		       ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15
		FROM node_event_log
		WHERE node_id = ?4`,
		p.GlobalSeq, p.EventTime, p.Author, p.NodeID,
		p.Name, p.Identifier, p.EnrolledTime, p.Status, p.RolesJSON, p.AddressesJSON,
		p.WgPublicKey, p.AllowedSpacesJSON, EventUpdate, normalizeHostAddressesJSON(p.HostAddressesJSON), p.EnrollmentRequestedAt)
	if err != nil {
		return CurrentNode{}, err
	}
	return q.GetNodeRowByID(ctx, p.NodeID)
}

func (q *Queries) CountNodesWithName(ctx context.Context, name string, excludeNodeID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM node_event_log n
		JOIN (SELECT node_id, MAX(version) AS version
		      FROM node_event_log GROUP BY node_id) latest
		  ON latest.node_id = n.node_id AND latest.version = n.version
		WHERE n.event_type != 3 AND n.name = ? AND n.node_id != ?`, name, excludeNodeID).Scan(&n)
	return n, err
}

func (q *Queries) ListNodeRows(ctx context.Context, statuses []int64) ([]CurrentNode, error) {
	marks, args := statusPlaceholders(statuses)
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.status IN (`+marks+`)
		ORDER BY n.node_id`, args...)
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

func (q *Queries) ListEnrollmentNodeRows(ctx context.Context, enrollmentStatuses []int64) ([]CurrentNode, error) {
	marks, args := statusPlaceholders(enrollmentStatuses)
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.status IN (`+marks+`) OR n.enrollment_requested_at != 0
		ORDER BY n.created_time DESC, n.node_id DESC`, args...)
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

func (q *Queries) GetNodeIDByIdentifier(ctx context.Context, identifier string) (int64, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `SELECT node_id FROM node_event_log WHERE identifier = ? LIMIT 1`, identifier).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) GetNodeIDWithRole(ctx context.Context, role int64, statuses []int64) (int64, error) {
	marks, args := statusPlaceholders(statuses)
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `
		SELECT n.node_id
		`+nodeCurrentFrom+`
		WHERE n.status IN (`+marks+`)
		AND EXISTS (SELECT 1 FROM json_each(n.roles) WHERE value = ?)
		LIMIT 1`, append(args, role)...).Scan(&nodeID)
	return nodeID, err
}

func normalizeHostAddressesJSON(value string) string {
	if value == "" {
		return "[]"
	}
	return value
}
