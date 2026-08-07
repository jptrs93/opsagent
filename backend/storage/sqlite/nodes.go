package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
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
	Identifier   string
	Roles        []int32
	Addresses    []string
	WGPublicKey  string
	EnrolledAt   time.Time
	// Spaces whose deployments may be placed here. Always contains
	// OpendeploySpaceID; see normalizeAllowedSpaces.
	AllowedSpaces []int32
}

func nodeRolesJSON(roles []int32) string {
	b, err := json.Marshal(roles)
	if err != nil {
		panic(fmt.Sprintf("marshal node roles: %v", err))
	}
	return string(b)
}

func nodeAddressesJSON(addresses []string) string {
	b, err := json.Marshal(addresses)
	if err != nil {
		panic(fmt.Sprintf("marshal node addresses: %v", err))
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

func allowedSpacesJSON(spaces []int32) string {
	b, err := json.Marshal(normalizeAllowedSpaces(spaces))
	if err != nil {
		panic(fmt.Sprintf("marshal node allowed spaces: %v", err))
	}
	return string(b)
}

func parseAllowedSpaces(s string) []int32 {
	var spaces []int32
	if err := json.Unmarshal([]byte(s), &spaces); err != nil {
		// Includes the pre-backfill sentinel. Normalizing an empty list still
		// yields the opendeploy space, so a node is never left allowing nothing.
		return normalizeAllowedSpaces(nil)
	}
	return normalizeAllowedSpaces(spaces)
}

// normalizeAllowedSpaces is the single point where the invariant holds: the
// opendeploy space is always allowed, whatever the caller passed and whatever
// is on disk. Applied on every read and every write, so a bad migration, a
// hand-edited database, or a future writer cannot violate it, and no caller
// has to remember to.
func normalizeAllowedSpaces(spaces []int32) []int32 {
	seen := map[int32]struct{}{OpendeploySpaceID: {}}
	out := []int32{OpendeploySpaceID}
	for _, id := range spaces {
		if id < 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// allSpaceIDsExpr yields every space that currently exists, as a JSON array, in
// the same statement that inserts the node. A new node starts out allowing
// everything and is narrowed deliberately; see PostV1SpacesCreate, which keeps
// that true for spaces created later.
const allSpaceIDsExpr = `COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]')`

// EnsurePrimaryNode resolves the primary by its server certificate CN, then
// creates the initial registry entry if that certificate has no row yet. The
// identifier is the mTLS and deployment identity; name is UI metadata only.
func (s *PrimaryStorage) EnsurePrimaryNode(name, identifier string) *Node {
	s.mu.Lock()
	ctx := context.Background()
	node, err := scanNodeRows(s.db.QueryRowContext(ctx, `
		SELECT id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces
		FROM nodes
		WHERE identifier = ?
		LIMIT 1`, identifier))
	if err == sql.ErrNoRows {
		node, err = insertNode(ctx, s.db, nil, name, identifier, []int32{NodeRolePrimary})
	}
	s.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("ensure primary node: %v", err))
	}
	s.nodeSubs.Notify(*nodeToAPI(node))
	return node
}

type nodeExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func insertNode(ctx context.Context, execer nodeExecer, enrollmentID any, name, identifier string, roles []int32) (*Node, error) {
	node, err := scanNodeRows(execer.QueryRowContext(ctx, `
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces)
		VALUES (?, ?, ?, ?, ?, '[]', '', COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]'))
		RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`, enrollmentID, time.Now().UnixMilli(), name, identifier, nodeRolesJSON(roles)))
	if err != nil {
		return nil, err
	}
	if _, err := execer.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id, last_connected_at, is_connected)
		VALUES (?, 0, 0)
		ON CONFLICT(node_id) DO NOTHING`, int64(node.ID)); err != nil {
		return nil, err
	}
	return node, nil
}

func upsertEnrolledNode(ctx context.Context, execer nodeExecer, enrollmentID any, name, identifier string, roles []int32, addresses []string) (*Node, error) {
	node, err := scanNodeRows(execer.QueryRowContext(ctx, `
		INSERT INTO nodes (enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces)
		VALUES (?, ?, ?, ?, ?, ?, '', COALESCE((SELECT '[' || group_concat(id) || ']' FROM spaces), '[0]'))
		ON CONFLICT(identifier) DO UPDATE SET
			enrollment_id = COALESCE(nodes.enrollment_id, excluded.enrollment_id),
			name = excluded.name,
			roles = excluded.roles,
			addresses = excluded.addresses
		RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`, enrollmentID, time.Now().UnixMilli(), name, identifier, nodeRolesJSON(roles), nodeAddressesJSON(addresses)))
	if err != nil {
		return nil, err
	}
	if _, err := execer.ExecContext(ctx, `
		INSERT INTO node_statuses (node_id, last_connected_at, is_connected)
		VALUES (?, 0, 0)
		ON CONFLICT(node_id) DO NOTHING`, int64(node.ID)); err != nil {
		return nil, err
	}
	return node, nil
}

func (s *PrimaryStorage) MustSetNodeAddresses(id int32, addresses []string) *Node {
	s.mu.Lock()
	node, err := scanNodeRows(s.db.QueryRowContext(context.Background(), `
		UPDATE nodes SET addresses = ? WHERE id = ?
		RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`, nodeAddressesJSON(addresses), int64(id)))
	s.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("set node addresses: %v", err))
	}
	s.nodeSubs.Notify(*nodeToAPI(node))
	return node
}

// NormalizeNodeUnderlay canonicalizes an optional underlay address and ensures
// it uses the same address family as the other nodes in the cluster.
func (s *PrimaryStorage) NormalizeNodeUnderlay(identifier, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return "", fmt.Errorf("invalid underlay address %q", value)
	}
	addr = addr.Unmap()
	for _, node := range s.ListNodes() {
		if node == nil || node.Identifier == identifier || len(node.Addresses) == 0 || strings.TrimSpace(node.Addresses[0]) == "" {
			continue
		}
		existing, err := netip.ParseAddr(strings.TrimSpace(node.Addresses[0]))
		if err != nil || existing.Zone() != "" {
			return "", fmt.Errorf("node %d has invalid stored underlay address", node.ID)
		}
		if existing.Unmap().BitLen() != addr.BitLen() {
			return "", fmt.Errorf("underlay address family differs from cluster")
		}
	}
	return addr.String(), nil
}

func (s *PrimaryStorage) ListNodes() []*Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listNodesLocked()
}

func (s *PrimaryStorage) listNodesLocked() []*Node {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces
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

// FetchNetworkMapInputs returns node and scheduled-instance state from one
// storage critical section so the publisher never renders a mixed-time snapshot.
func (s *PrimaryStorage) FetchNetworkMapInputs() ([]*Node, []apigen.ScheduledInstanceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listNodesLocked(), s.instanceSnapshotLocked(nil)
}

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNodeRows(scanner nodeScanner) (*Node, error) {
	var id int64
	var enrollmentID sql.NullInt64
	var enrolledAt int64
	var name, identifier, roles, addresses, wgPublicKey, allowedSpaces string
	if err := scanner.Scan(&id, &enrollmentID, &enrolledAt, &name, &identifier, &roles, &addresses, &wgPublicKey, &allowedSpaces); err != nil {
		return nil, err
	}
	var eid *int32
	if enrollmentID.Valid {
		v := int32(enrollmentID.Int64)
		eid = &v
	}
	return &Node{
		ID:            int32(id),
		EnrollmentID:  eid,
		Name:          name,
		Identifier:    identifier,
		Roles:         parseNodeRoles(roles),
		Addresses:     parseNodeAddresses(addresses),
		WGPublicKey:   wgPublicKey,
		EnrolledAt:    time.UnixMilli(enrolledAt),
		AllowedSpaces: parseAllowedSpaces(allowedSpaces),
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
		ID:            node.ID,
		EnrollmentID:  enrollmentID,
		Name:          node.Name,
		Identifier:    node.Identifier,
		Roles:         node.Roles,
		WgPublicKey:   node.WGPublicKey,
		Addresses:     node.Addresses,
		EnrolledAt:    node.EnrolledAt,
		AllowedSpaces: normalizeAllowedSpaces(node.AllowedSpaces),
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

func (s *PrimaryStorage) SetNodeStatusByIdentifier(identifier string, connected bool, connectedAt time.Time) {
	s.mu.Lock()
	ctx := context.Background()
	lastConnectedAt := int64(0)
	if connected {
		lastConnectedAt = connectedAt.UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO node_statuses (node_id, last_connected_at, is_connected)
		SELECT id, 0, 0 FROM nodes WHERE identifier = ?`, identifier)
	if err != nil {
		s.mu.Unlock()
		panic(fmt.Sprintf("ensure node status %q: %v", identifier, err))
	}
	status, err := scanNodeStatusRows(s.db.QueryRowContext(ctx, `
		UPDATE node_statuses
		SET last_connected_at = CASE WHEN ? = 1 THEN ? ELSE last_connected_at END,
			is_connected = ?
		WHERE node_id = (SELECT id FROM nodes WHERE identifier = ?)
		RETURNING id, node_id, last_connected_at, is_connected`, boolToInt(connected), lastConnectedAt, boolToInt(connected), identifier))
	s.mu.Unlock()
	if err == nil {
		s.nodeStatusSubs.Notify(*status)
		return
	}
	if err != sql.ErrNoRows {
		panic(fmt.Sprintf("set node status %q: %v", identifier, err))
	}
}

func (s *PrimaryStorage) HasNodeIdentifier(identifier string) bool {
	_, err := s.NodeIDByIdentifier(identifier)
	return err == nil
}

func (s *PrimaryStorage) NodeIDByIdentifier(identifier string) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var nodeID int64
	err := s.db.QueryRowContext(context.Background(), `SELECT id FROM nodes WHERE identifier = ?`, identifier).Scan(&nodeID)
	return int32(nodeID), err
}

func (s *PrimaryStorage) NodeIdentifierByID(nodeID int32) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var identifier string
	err := s.db.QueryRowContext(context.Background(), `SELECT identifier FROM nodes WHERE id = ?`, nodeID).Scan(&identifier)
	return identifier, err
}

func (s *PrimaryStorage) PrimaryNodeID() (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var nodeID int32
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id
		FROM nodes
		WHERE EXISTS (SELECT 1 FROM json_each(nodes.roles) WHERE value = ?)
		LIMIT 1`, NodeRolePrimary).Scan(&nodeID)
	return nodeID, err
}

func (s *PrimaryStorage) RenameNode(identifier, name string) (*apigen.ClusterNode, error) {
	s.mu.Lock()
	node, err := scanNodeRows(s.db.QueryRowContext(context.Background(), `
		UPDATE nodes SET name = ? WHERE identifier = ?
		RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`, name, identifier))
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	result := nodeToAPI(node)
	s.nodeSubs.Notify(*result)
	return result, nil
}

// SetNodeAllowedSpaces replaces a node's allow list. The value is normalized on
// the way in, so the opendeploy space survives a caller that omitted it.
func (s *PrimaryStorage) SetNodeAllowedSpaces(identifier string, spaces []int32) (*apigen.ClusterNode, error) {
	s.mu.Lock()
	node, err := scanNodeRows(s.db.QueryRowContext(context.Background(), `
		UPDATE nodes SET allowed_spaces = ? WHERE identifier = ?
		RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`,
		allowedSpacesJSON(spaces), identifier))
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	result := nodeToAPI(node)
	s.nodeSubs.Notify(*result)
	return result, nil
}

// AllowSpaceOnAllNodes opens a space on every node. Called when a space is
// created so that "a new node allows everything that exists" stays true in the
// other direction too: without it, a space added after a node was enrolled
// would be silently unavailable there, and the first deployment into it would
// fail on every existing node.
func (s *PrimaryStorage) AllowSpaceOnAllNodes(spaceID int32) {
	s.updateAllNodeAllowedSpaces(func(spaces []int32) []int32 {
		return append(spaces, spaceID)
	})
}

// RemoveSpaceFromAllNodes drops a deleted space from every allow list, so ids
// of spaces that no longer exist do not accumulate.
func (s *PrimaryStorage) RemoveSpaceFromAllNodes(spaceID int32) {
	s.updateAllNodeAllowedSpaces(func(spaces []int32) []int32 {
		out := spaces[:0:0]
		for _, id := range spaces {
			if id != spaceID {
				out = append(out, id)
			}
		}
		return out
	})
}

func (s *PrimaryStorage) updateAllNodeAllowedSpaces(fn func([]int32) []int32) {
	s.mu.Lock()
	nodes := s.listNodesLocked()
	updated := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		next := allowedSpacesJSON(fn(node.AllowedSpaces))
		if next == allowedSpacesJSON(node.AllowedSpaces) {
			continue
		}
		row, err := scanNodeRows(s.db.QueryRowContext(context.Background(), `
			UPDATE nodes SET allowed_spaces = ? WHERE id = ?
			RETURNING id, enrollment_id, enrolled_at, name, identifier, roles, addresses, wg_public_key, allowed_spaces`,
			next, int64(node.ID)))
		if err != nil {
			panic(fmt.Sprintf("update node allowed spaces: %v", err))
		}
		updated = append(updated, row)
	}
	s.mu.Unlock()
	// Notified outside the lock, matching the other node mutators.
	for _, node := range updated {
		s.nodeSubs.Notify(*nodeToAPI(node))
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
