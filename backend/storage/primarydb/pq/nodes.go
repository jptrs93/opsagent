package pq

import (
	"context"
)

// Hand-written node queries. The nodes table stores roles, addresses, and
// allowed_spaces as JSON strings; rows come back as the raw NodeRow model and
// the storage layer owns parsing and invariants.

const nodeColumns = `id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`

// allSpaceIDsExpr yields every space that currently exists, as a JSON array, in
// the same statement that inserts the node. A new node starts out allowing
// everything and is narrowed deliberately; the storage layer keeps that true
// for spaces created later.
const allSpaceIDsExpr = `COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]')`

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNodeRow(scanner nodeScanner) (NodeRow, error) {
	var r NodeRow
	err := scanner.Scan(&r.ID, &r.EnrollmentID, &r.EnrolledAt, &r.Name, &r.Identifier, &r.Roles, &r.Addresses, &r.WgPublicKey, &r.AllowedSpaces)
	return r, err
}

func (q *Queries) GetNodeRowByIdentifier(ctx context.Context, identifier string) (NodeRow, error) {
	return scanNodeRow(q.db.QueryRowContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes
		WHERE identifier = ?
		LIMIT 1`, identifier))
}

type InsertNodeRowParams struct {
	EnrollmentID any
	EnrolledAt   int64
	Name         string
	Identifier   string
	RolesJSON    string
}

// InsertNodeRow inserts a node allowing every space that currently exists,
// together with its zeroed node_statuses row.
func (q *Queries) InsertNodeRow(ctx context.Context, p InsertNodeRowParams) (NodeRow, error) {
	node, err := scanNodeRow(q.db.QueryRowContext(ctx, `
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces)
		VALUES (?, ?, ?, ?, ?, '[]', '', `+allSpaceIDsExpr+`)
		RETURNING `+nodeColumns, p.EnrollmentID, p.EnrolledAt, p.Name, p.Identifier, p.RolesJSON))
	if err != nil {
		return node, err
	}
	return node, q.ensureNodeStatusRow(ctx, node.ID)
}

type UpsertEnrolledNodeRowParams struct {
	EnrollmentID  any
	EnrolledAt    int64
	Name          string
	Identifier    string
	RolesJSON     string
	AddressesJSON string
}

// UpsertEnrolledNodeRow inserts or refreshes an enrolled node (matching on
// identifier), together with its zeroed node_statuses row.
func (q *Queries) UpsertEnrolledNodeRow(ctx context.Context, p UpsertEnrolledNodeRowParams) (NodeRow, error) {
	node, err := scanNodeRow(q.db.QueryRowContext(ctx, `
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces)
		VALUES (?, ?, ?, ?, ?, ?, '', `+allSpaceIDsExpr+`)
		ON CONFLICT(identifier) DO UPDATE SET
			enrollment_id = COALESCE(nodes.enrollment_id, excluded.enrollment_id),
			name = excluded.name,
			roles = excluded.roles,
			addresses = excluded.addresses
		RETURNING `+nodeColumns, p.EnrollmentID, p.EnrolledAt, p.Name, p.Identifier, p.RolesJSON, p.AddressesJSON))
	if err != nil {
		return node, err
	}
	return node, q.ensureNodeStatusRow(ctx, node.ID)
}

func (q *Queries) ensureNodeStatusRow(ctx context.Context, nodeID int64) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id, last_connected_at, is_connected)
		VALUES (?, 0, 0)
		ON CONFLICT(node_id) DO NOTHING`, nodeID)
	return err
}

func (q *Queries) UpdateNodeAddresses(ctx context.Context, addressesJSON string, id int64) (NodeRow, error) {
	return scanNodeRow(q.db.QueryRowContext(ctx, `
		UPDATE nodes SET addresses = ? WHERE id = ?
		RETURNING `+nodeColumns, addressesJSON, id))
}

func (q *Queries) UpdateNodeName(ctx context.Context, name, identifier string) (NodeRow, error) {
	return scanNodeRow(q.db.QueryRowContext(ctx, `
		UPDATE nodes SET name = ? WHERE identifier = ?
		RETURNING `+nodeColumns, name, identifier))
}

func (q *Queries) UpdateNodeAllowedSpacesByIdentifier(ctx context.Context, spacesJSON, identifier string) (NodeRow, error) {
	return scanNodeRow(q.db.QueryRowContext(ctx, `
		UPDATE nodes SET allowed_spaces = ? WHERE identifier = ?
		RETURNING `+nodeColumns, spacesJSON, identifier))
}

func (q *Queries) UpdateNodeAllowedSpacesByID(ctx context.Context, spacesJSON string, id int64) (NodeRow, error) {
	return scanNodeRow(q.db.QueryRowContext(ctx, `
		UPDATE nodes SET allowed_spaces = ? WHERE id = ?
		RETURNING `+nodeColumns, spacesJSON, id))
}

func (q *Queries) ListNodeRows(ctx context.Context) ([]NodeRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+nodeColumns+`
		FROM nodes
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRow
	for rows.Next() {
		r, err := scanNodeRow(rows)
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

// GetNodeIDWithRole returns the id of a node carrying the given role.
func (q *Queries) GetNodeIDWithRole(ctx context.Context, role int64) (int64, error) {
	var nodeID int64
	err := q.db.QueryRowContext(ctx, `
		SELECT id
		FROM nodes
		WHERE EXISTS (SELECT 1 FROM json_each(nodes.roles) WHERE value = ?)
		LIMIT 1`, role).Scan(&nodeID)
	return nodeID, err
}

func (q *Queries) ListNodeStatusRows(ctx context.Context) ([]NodeStatus, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, node_id, last_connected_at, is_connected
		FROM node_statuses
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeStatus
	for rows.Next() {
		var r NodeStatus
		if err := rows.Scan(&r.ID, &r.NodeID, &r.LastConnectedAt, &r.IsConnected); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnsureNodeStatusRowByIdentifier creates the zeroed status row for a node
// resolved by identifier if it does not exist yet.
func (q *Queries) EnsureNodeStatusRowByIdentifier(ctx context.Context, identifier string) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO node_statuses (node_id, last_connected_at, is_connected)
		SELECT id, 0, 0 FROM nodes WHERE identifier = ?`, identifier)
	return err
}

type SetNodeConnectionStatusParams struct {
	Connected       int64
	LastConnectedAt int64
	Identifier      string
}

// SetNodeConnectionStatus flips a node's connection flag, refreshing
// last_connected_at only on connect.
func (q *Queries) SetNodeConnectionStatus(ctx context.Context, p SetNodeConnectionStatusParams) (NodeStatus, error) {
	var r NodeStatus
	err := q.db.QueryRowContext(ctx, `
		UPDATE node_statuses
		SET last_connected_at = CASE WHEN ? = 1 THEN ? ELSE last_connected_at END,
			is_connected = ?
		WHERE node_id = (SELECT id FROM nodes WHERE identifier = ?)
		RETURNING id, node_id, last_connected_at, is_connected`,
		p.Connected, p.LastConnectedAt, p.Connected, p.Identifier).
		Scan(&r.ID, &r.NodeID, &r.LastConnectedAt, &r.IsConnected)
	return r, err
}
