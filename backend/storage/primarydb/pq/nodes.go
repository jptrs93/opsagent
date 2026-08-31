package pq

import (
	"context"
	"strings"
)

type NodeCurrentRow struct {
	NodeRow
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

const nodeCurrentColumns = `n.id, n.created_at, n.enrolled_at, n.name, n.identifier,
	v.version, v.status, v.roles, v.addresses, v.wg_public_key, v.allowed_spaces,
	COALESCE(ns.is_connected, 0), COALESCE(ns.opendeploy_version, ''), COALESCE(ns.remote_address, ''), COALESCE(ns.enrollment_pending, 0)`

const nodeCurrentFrom = `FROM nodes n
	JOIN node_versions v ON v.node_id = n.id
	AND v.version = (SELECT MAX(version) FROM node_versions WHERE node_id = n.id)
	LEFT JOIN node_statuses ns ON ns.node_id = n.id`

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
		WHERE n.id = ?
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
		INSERT INTO nodes (created_at, enrolled_at, name, identifier)
		VALUES (?, ?, ?, ?)
		RETURNING id`, p.CreatedAt, p.EnrolledAt, p.Name, p.Identifier).Scan(&nodeID)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO node_versions (node_id, version, created_at, author, status, roles, addresses, wg_public_key, allowed_spaces, global_seq)
		VALUES (?, 1, ?, 0, ?, ?, ?, ?, `+allSpaceIDsExpr+`, ?)`,
		nodeID, p.CreatedAt, p.Status, p.RolesJSON, p.AddressesJSON, p.WgPublicKey, p.GlobalSeq)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	if err := q.ensureNodeStatusRow(ctx, nodeID); err != nil {
		return NodeCurrentRow{}, err
	}
	return q.GetNodeRowByID(ctx, nodeID)
}

type InsertNodeVersionParams struct {
	NodeID            int64
	CreatedAt         int64
	Author            int64
	Status            int64
	RolesJSON         string
	AddressesJSON     string
	WgPublicKey       string
	AllowedSpacesJSON string
	GlobalSeq         int64
}

func (q *Queries) InsertNodeVersionRow(ctx context.Context, p InsertNodeVersionParams) (NodeCurrentRow, error) {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_versions (node_id, version, created_at, author, status, roles, addresses, wg_public_key, allowed_spaces, global_seq)
		VALUES (?, COALESCE((SELECT MAX(version) FROM node_versions WHERE node_id = ?), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.NodeID, p.NodeID, p.CreatedAt, p.Author, p.Status, p.RolesJSON, p.AddressesJSON, p.WgPublicKey, p.AllowedSpacesJSON, p.GlobalSeq)
	if err != nil {
		return NodeCurrentRow{}, err
	}
	return q.GetNodeRowByID(ctx, p.NodeID)
}

func (q *Queries) ListNodeVersionRows(ctx context.Context, nodeID int64) ([]NodeVersion, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, node_id, version, created_at, author, status, roles, addresses, wg_public_key, allowed_spaces, global_seq
		FROM node_versions
		WHERE node_id = ?
		ORDER BY version`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeVersion
	for rows.Next() {
		var r NodeVersion
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Version, &r.CreatedAt, &r.Author, &r.Status, &r.Roles, &r.Addresses, &r.WgPublicKey, &r.AllowedSpaces, &r.GlobalSeq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) ensureNodeStatusRow(ctx context.Context, nodeID int64) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id)
		VALUES (?)
		ON CONFLICT(node_id) DO NOTHING`, nodeID)
	return err
}

func (q *Queries) UpdateNodeName(ctx context.Context, name, identifier string) (NodeCurrentRow, error) {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE nodes SET name = ? WHERE identifier = ?`, name, identifier); err != nil {
		return NodeCurrentRow{}, err
	}
	return q.GetNodeRowByIdentifier(ctx, identifier)
}

func (q *Queries) UpdateNodeAccepted(ctx context.Context, name string, enrolledAt int64, id int64) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE nodes
		SET name = ?, enrolled_at = CASE WHEN enrolled_at = 0 THEN ? ELSE enrolled_at END
		WHERE id = ?`, name, enrolledAt, id)
	return err
}

func (q *Queries) ListNodeRows(ctx context.Context, statuses []int64) ([]NodeCurrentRow, error) {
	marks, args := statusPlaceholders(statuses)
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+nodeCurrentColumns+`
		`+nodeCurrentFrom+`
		WHERE v.status IN (`+marks+`)
		ORDER BY n.id`, args...)
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
		WHERE v.status IN (`+marks+`) OR COALESCE(ns.enrollment_pending, 0) = 1
		ORDER BY n.created_at DESC, n.id DESC`, args...)
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

func (q *Queries) GetNodeIDByIdentifier(ctx context.Context, identifier string) (int64, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM nodes WHERE identifier = ?`, identifier).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) GetNodeIdentifierByID(ctx context.Context, id int64) (string, error) {
	var identifier string
	err := q.db.QueryRowContext(ctx, `SELECT identifier FROM nodes WHERE id = ?`, id).Scan(&identifier)
	return identifier, err
}

func (q *Queries) GetNodeIDWithRole(ctx context.Context, role int64, statuses []int64) (int64, error) {
	marks, args := statusPlaceholders(statuses)
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `
		SELECT n.id
		`+nodeCurrentFrom+`
		WHERE v.status IN (`+marks+`)
		AND EXISTS (SELECT 1 FROM json_each(v.roles) WHERE value = ?)
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
		SELECT id FROM nodes WHERE identifier = ?`, identifier)
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
		WHERE node_id = (SELECT id FROM nodes WHERE identifier = ?)
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
