package state

import (
	"encoding/json"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// nodeRowToNode parses a joined identity-plus-latest-version row — roles,
// addresses, and allowed_spaces are JSON strings in the DB — into the domain
// Node.
func nodeRowToNode(r pq.NodeCurrentRow) *Node {
	return &Node{
		ID:            int32(r.ID),
		Name:          r.Name,
		Identifier:    r.Identifier,
		Status:        apigen.NodeLifecycleStatus(r.Status),
		Roles:         parseNodeRoles(r.Roles),
		Addresses:     parseNodeAddresses(r.Addresses),
		WGPublicKey:   r.WgPublicKey,
		CreatedAt:     time.UnixMilli(r.CreatedAt),
		EnrolledAt:    time.UnixMilli(r.EnrolledAt),
		AllowedSpaces: parseAllowedSpaces(r.AllowedSpaces),
	}
}

func nodeStatusRowToProto(r pq.NodeStatus) *apigen.ClusterNodeStatus {
	connectedAt := time.Time{}
	if r.LastConnectedAt > 0 {
		connectedAt = time.UnixMilli(r.LastConnectedAt)
	}
	return &apigen.ClusterNodeStatus{
		ID:              int32(r.ID),
		NodeID:          int32(r.NodeID),
		LastConnectedAt: connectedAt,
		IsConnected:     r.IsConnected != 0,
	}
}

func nodeToAPI(node *Node) *apigen.ClusterNode {
	if node == nil {
		return nil
	}
	return &apigen.ClusterNode{
		ID:            node.ID,
		Status:        node.Status,
		Name:          node.Name,
		Identifier:    node.Identifier,
		Roles:         node.Roles,
		WgPublicKey:   node.WGPublicKey,
		Addresses:     node.Addresses,
		EnrolledAt:    node.EnrolledAt,
		AllowedSpaces: normalizeAllowedSpaces(node.AllowedSpaces),
	}
}

func nodeRolesJSON(roles []int32) string {
	b := erru.Must(json.Marshal(roles))
	return string(b)
}

func nodeAddressesJSON(addresses []string) string {
	b := erru.Must(json.Marshal(addresses))
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

func parseAllowedSpaces(s string) []int32 {
	var spaces []int32
	if err := json.Unmarshal([]byte(s), &spaces); err != nil {
		// Normalizing an empty list still yields the opendeploy space, so a
		// node is never left allowing nothing.
		return normalizeAllowedSpaces(nil)
	}
	return normalizeAllowedSpaces(spaces)
}

func allowedSpacesJSON(spaces []int32) string {
	b := erru.Must(json.Marshal(normalizeAllowedSpaces(spaces)))
	return string(b)
}
