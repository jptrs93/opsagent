package pq

import (
	"context"
	"strings"
)

type NodeCurrentRow struct {
	ID                int64
	CreatedAt         int64
	EnrolledAt        int64
	Name              string
	Identifier        string
	Version           int64
	Status            int64
	Roles             string
	Addresses         string
	WgPublicKey       string
	AllowedSpaces     string
	IsConnected       int64
	OpendeployVersion string
	RemoteAddress     string
	EnrollmentPending int64
}

type NodeEvent struct {
	ID            int64
	GlobalSeq     int64
	EventTime     int64
	CreatedTime   int64
	Author        int64
	NodeID        int64
	Version       int64
	Name          string
	Identifier    string
	EnrolledTime  int64
	Status        int64
	Roles         string
	Addresses     string
	WgPublicKey   string
	AllowedSpaces string
	EventType     int64
}

const nodeCurrentColumns = `n.node_id, n.created_time, n.enrolled_time, n.name, n.identifier,
	n.version, n.status, n.roles, n.addresses, n.wg_public_key, n.allowed_spaces,
	COALESCE(ns.is_connected, 0), COALESCE(ns.opendeploy_version, ''), COALESCE(ns.remote_address, ''), COALESCE(ns.enrollment_pending, 0)`

const nodeCurrentFrom = `FROM node_event_log n
	JOIN (SELECT node_id, MAX(version) AS version
	      FROM node_event_log GROUP BY node_id) latest
	  ON latest.node_id = n.node_id AND latest.version = n.version
	LEFT JOIN node_statuses ns ON ns.node_id = n.node_id`

const allSpaceIDsExpr = `COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]')`

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNodeCurrentRow(scanner nodeScanner) (NodeCurrentRow, error) {
	var r NodeCurrentRow
	err := scanner.Scan(&r.ID, &r.CreatedAt, &r.EnrolledAt, &r.Name, &r.Identifier,
		&r.Version, &r.Status, &r.Roles, &r.Addresses, &r.WgPublicKey, &r.AllowedSpaces,
		&r.IsConnected, &r.OpendeployVersion, &r.RemoteAddress, &r.EnrollmentPending)
	return r, err
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

func (q *Queries) GetNodeRowByID(ctx context.Context, id int64) (NodeCurrentRow, error) {
	return scanNodeCurrentRow(q.db.QueryRowContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.node_id = ?
		LIMIT 1`, id))
}

func (q *Queries) GetNodeRowByIdentifier(ctx context.Context, identifier string) (NodeCurrentRow, error) {
	return scanNodeCurrentRow(q.db.QueryRowContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.identifier = ?
		LIMIT 1`, identifier))
}

type InsertNodeParams struct {
	CreatedAt     int64
	EnrolledAt    int64
	Name          string
	Identifier    string
	Status        int64
	RolesJSON     string
	AddressesJSON string
	WgPublicKey   string
	GlobalSeq     int64
}

func (q *Queries) InsertNodeRow(ctx context.Context, p InsertNodeParams) (NodeCurrentRow, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(node_id), 0) + 1 FROM node_event_log`).Scan(&nodeID)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO node_event_log (
			global_seq, event_time, created_time, author, node_id, version,
			name, identifier, enrolled_time, status, roles, addresses,
			wg_public_key, allowed_spaces, event_type)
		VALUES (?, ?, ?, 0, ?, 1, ?, ?, ?, ?, ?, ?, ?, `+allSpaceIDsExpr+`, ?)`,
		p.GlobalSeq, p.CreatedAt, p.CreatedAt, nodeID, p.Name, p.Identifier, p.EnrolledAt,
		p.Status, p.RolesJSON, p.AddressesJSON, p.WgPublicKey, EventCreate)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	if err := q.ensureNodeStatusRow(ctx, nodeID); err != nil {
		return NodeCurrentRow{}, err
	}
	return q.GetNodeRowByID(ctx, nodeID)
}

type AppendNodeEventParams struct {
	NodeID            int64
	EventTime         int64
	Author            int64
	Name              string
	Identifier        string
	EnrolledTime      int64
	Status            int64
	RolesJSON         string
	AddressesJSON     string
	WgPublicKey       string
	AllowedSpacesJSON string
	GlobalSeq         int64
}

func (q *Queries) AppendNodeEvent(ctx context.Context, p AppendNodeEventParams) (NodeCurrentRow, error) {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_event_log (
			global_seq, event_time, created_time, author, node_id, version,
			name, identifier, enrolled_time, status, roles, addresses,
			wg_public_key, allowed_spaces, event_type)
		SELECT ?1, ?2, COALESCE(MIN(created_time), ?2), ?3,
		       ?4, COALESCE(MAX(version), 0) + 1,
		       ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13
		FROM node_event_log
		WHERE node_id = ?4`,
		p.GlobalSeq, p.EventTime, p.Author, p.NodeID,
		p.Name, p.Identifier, p.EnrolledTime, p.Status, p.RolesJSON, p.AddressesJSON,
		p.WgPublicKey, p.AllowedSpacesJSON, EventUpdate)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	return q.GetNodeRowByID(ctx, p.NodeID)
}

func (q *Queries) ListNodeEvents(ctx context.Context, nodeID int64) ([]NodeEvent, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, global_seq, event_time, created_time, author, node_id, version,
		       name, identifier, enrolled_time, status, roles, addresses,
		       wg_public_key, allowed_spaces, event_type
		FROM node_event_log
		WHERE node_id = ?
		ORDER BY version`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeEvent
	for rows.Next() {
		var r NodeEvent
		if err := rows.Scan(&r.ID, &r.GlobalSeq, &r.EventTime, &r.CreatedTime, &r.Author,
			&r.NodeID, &r.Version, &r.Name, &r.Identifier, &r.EnrolledTime, &r.Status,
			&r.Roles, &r.Addresses, &r.WgPublicKey, &r.AllowedSpaces, &r.EventType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

func (q *Queries) ensureNodeStatusRow(ctx context.Context, nodeID int64) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id)
		VALUES (?)
		ON CONFLICT(node_id) DO NOTHING`, nodeID)
	return err
}

func (q *Queries) ListNodeRows(ctx context.Context, statuses []int64) ([]NodeCurrentRow, error) {
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
	var out []NodeCurrentRow
	for rows.Next() {
		r, err := scanNodeCurrentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) ListEnrollmentNodeRows(ctx context.Context, enrollmentStatuses []int64) ([]NodeCurrentRow, error) {
	marks, args := statusPlaceholders(enrollmentStatuses)
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE n.status IN (`+marks+`) OR COALESCE(ns.enrollment_pending, 0) = 1
		ORDER BY n.created_time DESC, n.node_id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeCurrentRow
	for rows.Next() {
		r, err := scanNodeCurrentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const nodeIDByIdentifierExpr = `(SELECT node_id FROM node_event_log WHERE identifier = ? LIMIT 1)`

func (q *Queries) GetNodeIDByIdentifier(ctx context.Context, identifier string) (int64, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `SELECT node_id FROM node_event_log WHERE identifier = ? LIMIT 1`, identifier).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) GetNodeIdentifierByID(ctx context.Context, id int64) (string, error) {
	var identifier string
	err := q.db.QueryRowContext(ctx, `SELECT identifier FROM node_event_log WHERE node_id = ? LIMIT 1`, id).Scan(&identifier)
	return identifier, err
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

func (q *Queries) ListNodeStatusRows(ctx context.Context) ([]NodeStatus, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, node_id, last_connected_at, is_connected, opendeploy_version, remote_address, enrollment_pending
		FROM node_statuses
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeStatus
	for rows.Next() {
		var r NodeStatus
		if err := rows.Scan(&r.ID, &r.NodeID, &r.LastConnectedAt, &r.IsConnected, &r.OpendeployVersion, &r.RemoteAddress, &r.EnrollmentPending); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) EnsureNodeStatusRowByIdentifier(ctx context.Context, identifier string) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO node_statuses (node_id)
		SELECT node_id FROM node_event_log WHERE identifier = ? LIMIT 1`, identifier)
	return err
}

type SetNodeConnectionStatusParams struct {
	Connected       int64
	LastConnectedAt int64
	Identifier      string
}

func (q *Queries) SetNodeConnectionStatus(ctx context.Context, p SetNodeConnectionStatusParams) (NodeStatus, error) {
	var r NodeStatus
	err := q.db.QueryRowContext(ctx, `
		UPDATE node_statuses
		SET last_connected_at = CASE WHEN ? = 1 THEN ? ELSE last_connected_at END,
			is_connected = ?
		WHERE node_id = `+nodeIDByIdentifierExpr+`
		RETURNING id, node_id, last_connected_at, is_connected, opendeploy_version, remote_address, enrollment_pending`,
		p.Connected, p.LastConnectedAt, p.Connected, p.Identifier).
		Scan(&r.ID, &r.NodeID, &r.LastConnectedAt, &r.IsConnected, &r.OpendeployVersion, &r.RemoteAddress, &r.EnrollmentPending)
	return r, err
}

type UpsertNodeObservedMetaParams struct {
	NodeID            int64
	LastConnectedAt   int64
	IsConnected       int64
	OpendeployVersion string
	RemoteAddress     string
	EnrollmentPending int64
}

func (q *Queries) UpsertNodeObservedMeta(ctx context.Context, p UpsertNodeObservedMetaParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id, last_connected_at, is_connected, opendeploy_version, remote_address, enrollment_pending)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			last_connected_at = excluded.last_connected_at,
			is_connected = excluded.is_connected,
			opendeploy_version = excluded.opendeploy_version,
			remote_address = excluded.remote_address,
			enrollment_pending = excluded.enrollment_pending`,
		p.NodeID, p.LastConnectedAt, p.IsConnected, p.OpendeployVersion, p.RemoteAddress, p.EnrollmentPending)
	return err
}

func (q *Queries) ClearNodeEnrollmentPending(ctx context.Context, nodeID int64) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE node_statuses SET enrollment_pending = 0 WHERE node_id = ?`, nodeID)
	return err
}
