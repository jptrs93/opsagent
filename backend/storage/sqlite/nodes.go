package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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
	WGPublicKey  string
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

func (s *PrimaryStorage) EnsurePrimaryNode(name string) {
	s.ensureNode(nil, name, name, []int32{NodeRolePrimary})
}

func (s *PrimaryStorage) EnsureNodesForSystemDeployments(primaryName string) {
	s.mu.Lock()
	machines := make([]string, 0)
	seen := map[string]bool{}
	for _, cfg := range s.configCache {
		if IsSystemDeploymentConfig(cfg) && !cfg.Deleted && cfg.ConfigID.Machine != "" && !seen[cfg.ConfigID.Machine] {
			seen[cfg.ConfigID.Machine] = true
			machines = append(machines, cfg.ConfigID.Machine)
		}
	}
	s.mu.Unlock()
	sort.Strings(machines)

	for _, machine := range machines {
		roles := []int32{NodeRoleSecondary}
		if machine == primaryName {
			roles = []int32{NodeRolePrimary}
		}
		s.ensureNode(nil, machine, machine, roles)
	}
}

func (s *PrimaryStorage) ensureNode(enrollmentID any, name string, sni string, roles []int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := upsertNode(ctx, s.db, enrollmentID, name, sni, roles); err != nil {
		panic(fmt.Sprintf("ensure node %q: %v", name, err))
	}
}

type nodeExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func upsertNode(ctx context.Context, execer nodeExecer, enrollmentID any, name string, sni string, roles []int32) error {
	_, err := execer.ExecContext(ctx, `
		INSERT INTO nodes (enrollment_id, name, sni, roles, wg_public_key)
		VALUES (?, ?, ?, ?, '')
		ON CONFLICT(name) DO UPDATE SET
			enrollment_id = COALESCE(nodes.enrollment_id, excluded.enrollment_id),
			sni = excluded.sni,
			roles = excluded.roles`, enrollmentID, name, sni, nodeRolesJSON(roles))
	return err
}

func (s *PrimaryStorage) ListNodes() []*Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, enrollment_id, name, sni, roles, wg_public_key
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
	var name, sni, roles, wgPublicKey string
	if err := scanner.Scan(&id, &enrollmentID, &name, &sni, &roles, &wgPublicKey); err != nil {
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
		WGPublicKey:  wgPublicKey,
	}, nil
}
