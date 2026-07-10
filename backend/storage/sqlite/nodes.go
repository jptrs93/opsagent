package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	NodeRolePrimary   int32 = 0
	NodeRoleSecondary int32 = 1
)

type Node struct {
	ID           int32
	EnrollmentID *int32
	Name         string
	SNI          string
	Roles        []int32
	Addresses    []string
	WGPublicKey  string
	EnrolledAt   time.Time
}

func nodeRolesJSON(roles []int32) string {
	b, err := json.Marshal(roles)
	if err != nil {
		panic(fmt.Sprintf("marshal node roles: %v", err))
	}
	return string(b)
}

func parseNodeRoles(s string) []int32 {
	var roles []int32
	if err := json.Unmarshal([]byte(s), &roles); err != nil {
		return nil
	}
	return roles
}

func parseNodeAddresses(s string) []string {
	var addresses []string
	if err := json.Unmarshal([]byte(s), &addresses); err != nil {
		return nil
	}
	return addresses
}

func (s *PrimaryStorage) EnsurePrimaryNode(name string) {
	s.ensureNode(nil, name, name, []int32{NodeRolePrimary})
}

func (s *PrimaryStorage) ensureNode(enrollmentID any, name string, sni string, roles []int32) {
	s.mu.Lock()
	ctx := context.Background()
	node, err := upsertNode(ctx, s.db, enrollmentID, name, sni, roles)
	s.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("ensure node %q: %v", name, err))
	}
	s.nodeSubs.Notify(*node)
}

type nodeExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func upsertNode(ctx context.Context, execer nodeExecer, enrollmentID any, name string, sni string, roles []int32) (*apigen.ClusterNode, error) {
	node, err := scanNodeRows(execer.QueryRowContext(ctx, `
		INSERT INTO nodes (enrollment_id, enrolled_at, name, sni, roles, addresses, wg_public_key)
		VALUES (?, ?, ?, ?, ?, '[]', '')
		ON CONFLICT(name) DO UPDATE SET
			enrollment_id = COALESCE(nodes.enrollment_id, excluded.enrollment_id),
			sni = excluded.sni,
			roles = excluded.roles
		RETURNING id, enrollment_id, enrolled_at, name, sni, roles, addresses, wg_public_key`, enrollmentID, time.Now().UnixMilli(), name, sni, nodeRolesJSON(roles)))
	if err != nil {
		return nil, err
	}
	if _, err := execer.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id, last_connected_at, is_connected)
		VALUES (?, 0, 0)
		ON CONFLICT(node_id) DO NOTHING`, int64(node.ID)); err != nil {
		return nil, err
	}
	return nodeToAPI(node), nil
}

func (s *PrimaryStorage) ListNodes() []*Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, enrollment_id, enrolled_at, name, sni, roles, addresses, wg_public_key
		FROM nodes
		ORDER BY id`)
	if err != nil {
		panic(fmt.Sprintf("list nodes: %v", err))
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		node, err := scanNodeRows(rows)
		if err != nil {
			panic(fmt.Sprintf("scan node: %v", err))
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("iterate nodes: %v", err))
	}
	return out
}

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNodeRows(scanner nodeScanner) (*Node, error) {
	var id int64
	var enrollmentID sql.NullInt64
	var enrolledAt int64
	var name, sni, roles, addresses, wgPublicKey string
	if err := scanner.Scan(&id, &enrollmentID, &enrolledAt, &name, &sni, &roles, &addresses, &wgPublicKey); err != nil {
		return nil, err
	}
	var eid *int32
	if enrollmentID.Valid {
		v := int32(enrollmentID.Int64)
		eid = &v
	}
	return &Node{
		ID:           int32(id),
		EnrollmentID: eid,
		Name:         name,
		SNI:          sni,
		Roles:        parseNodeRoles(roles),
		Addresses:    parseNodeAddresses(addresses),
		WGPublicKey:  wgPublicKey,
		EnrolledAt:   time.UnixMilli(enrolledAt),
	}, nil
}

func nodeToAPI(node *Node) *apigen.ClusterNode {
	if node == nil {
		return nil
	}
	enrollmentID := int32(0)
	if node.EnrollmentID != nil {
		enrollmentID = *node.EnrollmentID
	}
	return &apigen.ClusterNode{
		ID:           node.ID,
		EnrollmentID: enrollmentID,
		Name:         node.Name,
		Sni:          node.SNI,
		Roles:        node.Roles,
		WgPublicKey:  node.WGPublicKey,
		Addresses:    node.Addresses,
		EnrolledAt:   node.EnrolledAt,
	}
}

func (s *PrimaryStorage) ListClusterNodes() []*apigen.ClusterNode {
	nodes := s.ListNodes()
	out := make([]*apigen.ClusterNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeToAPI(node))
	}
	return out
}

func (s *PrimaryStorage) SubscribeNodeUpdates() (*pubsubu.Sub[apigen.ClusterNode], func()) {
	sub := s.nodeSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) ListNodeStatuses() []*apigen.ClusterNodeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, node_id, last_connected_at, is_connected
		FROM node_statuses
		ORDER BY id`)
	if err != nil {
		panic(fmt.Sprintf("list node statuses: %v", err))
	}
	defer rows.Close()
	var out []*apigen.ClusterNodeStatus
	for rows.Next() {
		status, err := scanNodeStatusRows(rows)
		if err != nil {
			panic(fmt.Sprintf("scan node status: %v", err))
		}
		out = append(out, status)
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("iterate node statuses: %v", err))
	}
	return out
}

func (s *PrimaryStorage) SetNodeStatusByName(name string, connected bool, connectedAt time.Time) {
	s.mu.Lock()
	ctx := context.Background()
	lastConnectedAt := int64(0)
	if connected {
		lastConnectedAt = connectedAt.UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO node_statuses (node_id, last_connected_at, is_connected)
		SELECT id, 0, 0 FROM nodes WHERE name = ?`, name)
	if err != nil {
		s.mu.Unlock()
		panic(fmt.Sprintf("ensure node status %q: %v", name, err))
	}
	status, err := scanNodeStatusRows(s.db.QueryRowContext(ctx, `
		UPDATE node_statuses
		SET last_connected_at = CASE WHEN ? = 1 THEN ? ELSE last_connected_at END,
			is_connected = ?
		WHERE node_id = (SELECT id FROM nodes WHERE name = ?)
		RETURNING id, node_id, last_connected_at, is_connected`, boolToInt(connected), lastConnectedAt, boolToInt(connected), name))
	s.mu.Unlock()
	if err == nil {
		s.nodeStatusSubs.Notify(*status)
		return
	}
	if err != sql.ErrNoRows {
		panic(fmt.Sprintf("set node status %q: %v", name, err))
	}
}

func (s *PrimaryStorage) SubscribeNodeStatusUpdates() (*pubsubu.Sub[apigen.ClusterNodeStatus], func()) {
	sub := s.nodeStatusSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func scanNodeStatusRows(scanner nodeScanner) (*apigen.ClusterNodeStatus, error) {
	var id, nodeID, lastConnectedAt int64
	var isConnected int64
	if err := scanner.Scan(&id, &nodeID, &lastConnectedAt, &isConnected); err != nil {
		return nil, err
	}
	connectedAt := time.Time{}
	if lastConnectedAt > 0 {
		connectedAt = time.UnixMilli(lastConnectedAt)
	}
	return &apigen.ClusterNodeStatus{
		ID:              int32(id),
		NodeID:          int32(nodeID),
		LastConnectedAt: connectedAt,
		IsConnected:     isConnected != 0,
	}, nil
}
