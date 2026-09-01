package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const (
	NodeRolePrimary   int32 = 0
	NodeRoleSecondary int32 = 1
)

type Node struct {
	ID          int32
	Name        string
	Identifier  string
	Status      apigen.NodeLifecycleStatus
	Roles       []int32
	Addresses   []string
	WGPublicKey string
	CreatedAt   time.Time
	EnrolledAt  time.Time
	// Spaces whose deployments may be placed here. Always contains
	// OpendeploySpaceID; see normalizeAllowedSpaces.
	AllowedSpaces []int32
}

var memberNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_UNHEALTHY),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_DRAINING),
	int64(apigen.NodeLifecycleStatus_NODE_MEMBER_MISSING),
}

var enrollmentNodeStatuses = []int64{
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_CANCELLED),
	int64(apigen.NodeLifecycleStatus_NODE_ENROLLMENT_REQUEST_EXPIRED),
}

var allNodeStatuses = []int64{
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

func isMemberStatus(status apigen.NodeLifecycleStatus) bool {
	for _, s := range memberNodeStatuses {
		if int64(status) == s {
			return true
		}
	}
	return false
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

// EnsurePrimaryNode resolves the primary by its server certificate CN, then
// creates the initial registry entry if that certificate has no row yet. The
// identifier is the mTLS and deployment identity; name is UI metadata only.
// Node registry changes are network-map inputs, so the create advances the
// global write sequence; a restart that finds the row consumes nothing.
func (s *Service) EnsurePrimaryNode(name, identifier string) *Node {
	s.Mu.Lock()
	ctx := context.Background()
	row, err := s.q.GetNodeRowByIdentifier(ctx, identifier)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.q.Tx(ctx, func(q *pq.Queries) error {
			seq, err := q.NextGlobalSeq(ctx)
			if err != nil {
				return err
			}
			now := time.Now().UnixMilli()
			var txErr error
			row, txErr = q.InsertNodeRow(ctx, pq.InsertNodeParams{
				CreatedAt:     now,
				EnrolledAt:    now,
				Name:          name,
				Identifier:    identifier,
				Status:        int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL),
				RolesJSON:     nodeRolesJSON([]int32{NodeRolePrimary}),
				AddressesJSON: nodeAddressesJSON([]string{}),
				GlobalSeq:     seq,
			})
			return txErr
		})
	}
	s.Mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("ensure primary node: %v", err))
	}
	node := nodeRowToNode(row)
	s.nodeSubs.Notify(*nodeToAPI(node))
	return node
}

// nodeVersionSpec is the versioned slice of node state, in the JSON-string
// shapes the node_versions row stores, so change detection is plain string
// comparison against the current row.
type nodeVersionSpec struct {
	Status            apigen.NodeLifecycleStatus
	RolesJSON         string
	AddressesJSON     string
	WGPublicKey       string
	AllowedSpacesJSON string
}

func nodeVersionSpecOf(row pq.NodeCurrentRow) nodeVersionSpec {
	return nodeVersionSpec{
		Status:            apigen.NodeLifecycleStatus(row.Status),
		RolesJSON:         row.Roles,
		AddressesJSON:     row.Addresses,
		WGPublicKey:       row.WgPublicKey,
		AllowedSpacesJSON: row.AllowedSpaces,
	}
}

// appendNodeVersion applies mutate to current's versioned state and, if it
// changed, allocates the global write sequence and appends the stamped
// version row in the same transaction. An unchanged spec is a pure no-op, so
// callers get diff-gating and the map-input seq invariant without holding
// either themselves.
func appendNodeVersion(ctx context.Context, q *pq.Queries, current pq.NodeCurrentRow, author int32, mutate func(*nodeVersionSpec)) (pq.NodeCurrentRow, bool, error) {
	spec := nodeVersionSpecOf(current)
	mutate(&spec)
	if spec == nodeVersionSpecOf(current) {
		return current, false, nil
	}
	seq, err := q.NextGlobalSeq(ctx)
	if err != nil {
		return current, false, err
	}
	row, err := q.InsertNodeVersionRow(ctx, pq.InsertNodeVersionParams{
		NodeID:            current.ID,
		CreatedAt:         time.Now().UnixMilli(),
		Author:            int64(author),
		Status:            int64(spec.Status),
		RolesJSON:         spec.RolesJSON,
		AddressesJSON:     spec.AddressesJSON,
		WgPublicKey:       spec.WGPublicKey,
		AllowedSpacesJSON: spec.AllowedSpacesJSON,
		GlobalSeq:         seq,
	})
	return row, err == nil, err
}

// mustAppendNodeVersion runs one versioned-state mutation for a node in its
// own transaction, notifying subscribers only when a version was appended.
func (s *Service) mustAppendNodeVersion(id int32, what string, mutate func(*nodeVersionSpec)) *Node {
	s.Mu.Lock()
	ctx := context.Background()
	var row pq.NodeCurrentRow
	changed := false
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		current, err := q.GetNodeRowByID(ctx, int64(id))
		if err != nil {
			return err
		}
		row, changed, err = appendNodeVersion(ctx, q, current, 0, mutate)
		return err
	})
	s.Mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("%s: %v", what, err))
	}
	node := nodeRowToNode(row)
	if changed {
		s.nodeSubs.Notify(*nodeToAPI(node))
	}
	return node
}

func (s *Service) MustSetNodeAddresses(id int32, addresses []string) *Node {
	return s.mustAppendNodeVersion(id, "set node addresses", func(spec *nodeVersionSpec) {
		spec.AddressesJSON = nodeAddressesJSON(addresses)
	})
}

// MustSetNodeWGPublicKey records a node's current WireGuard public key. An
// unchanged key is a pure no-op (secondaries re-report on every cluster-stream
// connect); a change appends a version row, which doubles as the key audit
// history an unexpected change would be investigated through.
func (s *Service) MustSetNodeWGPublicKey(id int32, wgPublicKey string) *Node {
	return s.mustAppendNodeVersion(id, "set node wireguard public key", func(spec *nodeVersionSpec) {
		spec.WGPublicKey = wgPublicKey
	})
}

// NormalizeNodeUnderlay canonicalizes a secondary's underlay address and ensures
// it uses the same address family as the other nodes in the cluster. Secondaries
// resolve an address before enrolling or connecting, so an empty address is a
// caller bug, never a legitimate state.
func (s *Service) NormalizeNodeUnderlay(identifier, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("underlay address is required")
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

func (s *Service) ListNodes() []*Node {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.listNodesLocked()
}

func (s *Service) listNodesLocked() []*Node {
	rows, err := s.q.ListNodeRows(context.Background(), memberNodeStatuses)
	if err != nil {
		panic(fmt.Sprintf("list nodes: %v", err))
	}
	out := make([]*Node, 0, len(rows))
	for _, row := range rows {
		out = append(out, nodeRowToNode(row))
	}
	return out
}

// NetworkMapInputs is one consistent render input snapshot: nodes, scheduled
// instances, override policies, the deployment-to-space resolution for policy
// peers, and the global write sequence the snapshot reflects.
type NetworkMapInputs struct {
	Nodes            []*Node
	Instances        []apigen.ScheduledInstanceState
	Policies         []*apigen.NetworkPolicy
	DeploymentSpaces map[int32]int32
	Seq              int64
}

// FetchNetworkMapInputs returns all network-map render inputs from one
// storage critical section so the publisher never renders a mixed-time
// snapshot. Every map-input write advances the global sequence in its own
// transaction, so a render at seq N is a pure function of state@N.
func (s *Service) FetchNetworkMapInputs() NetworkMapInputs {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	seq, err := s.q.GetGlobalSeq(context.Background())
	if err != nil {
		panic(fmt.Sprintf("get global seq: %v", err))
	}
	spaces := make(map[int32]int32, len(s.deploymentCache))
	for id, cfg := range s.deploymentCache {
		if cfg != nil && !cfg.Deleted {
			spaces[id] = cfg.SpaceID
		}
	}
	return NetworkMapInputs{
		Nodes:            s.listNodesLocked(),
		Instances:        s.SnapshotLocked(nil),
		Policies:         s.listNetworkPoliciesLocked(false),
		DeploymentSpaces: spaces,
		Seq:              seq,
	}
}

func (s *Service) ListClusterNodes() []*apigen.ClusterNode {
	nodes := s.ListNodes()
	out := make([]*apigen.ClusterNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeToAPI(node))
	}
	return out
}

func (s *Service) SubscribeNodeUpdates() (*pubsubu.Sub[apigen.ClusterNode], func()) {
	sub := s.nodeSubs.Subscribe(nil)
	return sub, sub.Unsubscribe
}

func (s *Service) ListNodeStatuses() []*apigen.ClusterNodeStatus {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	rows, err := s.q.ListNodeStatusRows(context.Background())
	if err != nil {
		panic(fmt.Sprintf("list node statuses: %v", err))
	}
	out := make([]*apigen.ClusterNodeStatus, 0, len(rows))
	for _, row := range rows {
		out = append(out, nodeStatusRowToProto(row))
	}
	return out
}

func (s *Service) SetNodeStatusByIdentifier(identifier string, connected bool, connectedAt time.Time) {
	s.Mu.Lock()
	ctx := context.Background()
	lastConnectedAt := int64(0)
	if connected {
		lastConnectedAt = connectedAt.UnixMilli()
	}
	if err := s.q.EnsureNodeStatusRowByIdentifier(ctx, identifier); err != nil {
		s.Mu.Unlock()
		panic(fmt.Sprintf("ensure node status %q: %v", identifier, err))
	}
	row, err := s.q.SetNodeConnectionStatus(ctx, pq.SetNodeConnectionStatusParams{
		Connected:       boolToInt(connected),
		LastConnectedAt: lastConnectedAt,
		Identifier:      identifier,
	})
	s.Mu.Unlock()
	if err == nil {
		s.nodeStatusSubs.Notify(*nodeStatusRowToProto(row))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("set node status %q: %v", identifier, err))
	}
}

func (s *Service) NodeIDByIdentifier(identifier string) (int32, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	nodeID, err := s.q.GetNodeIDByIdentifier(context.Background(), identifier)
	return int32(nodeID), err
}

func (s *Service) NodeIdentifierByID(nodeID int32) (string, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.q.GetNodeIdentifierByID(context.Background(), int64(nodeID))
}

func (s *Service) PrimaryNodeID() (int32, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	nodeID, err := s.q.GetNodeIDWithRole(context.Background(), int64(NodeRolePrimary), memberNodeStatuses)
	return int32(nodeID), err
}

func (s *Service) RenameNode(identifier, name string) (*apigen.ClusterNode, error) {
	s.Mu.Lock()
	row, err := s.q.UpdateNodeName(context.Background(), name, identifier)
	s.Mu.Unlock()
	if err != nil {
		return nil, err
	}
	result := nodeToAPI(nodeRowToNode(row))
	s.nodeSubs.Notify(*result)
	return result, nil
}

// SetNodeAllowedSpaces replaces a node's allow list. The value is normalized on
// the way in, so the opendeploy space survives a caller that omitted it.
func (s *Service) SetNodeAllowedSpaces(identifier string, spaces []int32) (*apigen.ClusterNode, error) {
	s.Mu.Lock()
	ctx := context.Background()
	var row pq.NodeCurrentRow
	changed := false
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		current, err := q.GetNodeRowByIdentifier(ctx, identifier)
		if err != nil {
			return err
		}
		row, changed, err = appendNodeVersion(ctx, q, current, 0, func(spec *nodeVersionSpec) {
			spec.AllowedSpacesJSON = allowedSpacesJSON(spaces)
		})
		return err
	})
	s.Mu.Unlock()
	if err != nil {
		return nil, err
	}
	result := nodeToAPI(nodeRowToNode(row))
	if changed {
		s.nodeSubs.Notify(*result)
	}
	return result, nil
}

// AllowSpaceOnAllNodes opens a space on every node. Called when a space is
// created so that "a new node allows everything that exists" stays true in the
// other direction too: without it, a space added after a node was enrolled
// would be silently unavailable there, and the first deployment into it would
// fail on every existing node.
func (s *Service) AllowSpaceOnAllNodes(spaceID int32) {
	s.updateAllNodeAllowedSpaces(func(spaces []int32) []int32 {
		return append(spaces, spaceID)
	})
}

// RemoveSpaceFromAllNodes drops a deleted space from every allow list, so ids
// of spaces that no longer exist do not accumulate.
func (s *Service) RemoveSpaceFromAllNodes(spaceID int32) {
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

func (s *Service) updateAllNodeAllowedSpaces(fn func([]int32) []int32) {
	s.Mu.Lock()
	ctx := context.Background()
	var updated []*Node
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		rows, err := q.ListNodeRows(ctx, allNodeStatuses)
		if err != nil {
			return err
		}
		for _, current := range rows {
			row, changed, err := appendNodeVersion(ctx, q, current, 0, func(spec *nodeVersionSpec) {
				spec.AllowedSpacesJSON = allowedSpacesJSON(fn(parseAllowedSpaces(current.AllowedSpaces)))
			})
			if err != nil {
				return err
			}
			if changed && isMemberStatus(apigen.NodeLifecycleStatus(row.Status)) {
				updated = append(updated, nodeRowToNode(row))
			}
		}
		return nil
	})
	s.Mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("update node allowed spaces: %v", err))
	}
	// Notified outside the lock, matching the other node mutators.
	for _, node := range updated {
		s.nodeSubs.Notify(*nodeToAPI(node))
	}
}

func (s *Service) SubscribeNodeStatusUpdates() (*pubsubu.Sub[apigen.ClusterNodeStatus], func()) {
	sub := s.nodeStatusSubs.Subscribe(nil)
	return sub, sub.Unsubscribe
}
