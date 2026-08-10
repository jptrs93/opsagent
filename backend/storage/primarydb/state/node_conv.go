package state

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// nodeRowToNode parses a raw nodes row — roles, addresses, and allowed_spaces
// are JSON strings in the DB — into the domain Node.
func nodeRowToNode(r pq.NodeRow) *Node {
	var eid *int32
	if r.EnrollmentID.Valid {
		v := int32(r.EnrollmentID.Int64)
		eid = &v
	}
	return &Node{
		ID:            int32(r.ID),
		EnrollmentID:  eid,
		Name:          r.Name,
		Identifier:    r.Identifier,
		Roles:         parseNodeRoles(r.Roles),
		Addresses:     parseNodeAddresses(r.Addresses),
		WGPublicKey:   r.WgPublicKey,
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

func parseAllowedSpaces(s string) []int32 {
	var spaces []int32
	if err := json.Unmarshal([]byte(s), &spaces); err != nil {
		// Includes the pre-backfill sentinel. Normalizing an empty list still
		// yields the opendeploy space, so a node is never left allowing nothing.
		return normalizeAllowedSpaces(nil)
	}
	return normalizeAllowedSpaces(spaces)
}

func allowedSpacesJSON(spaces []int32) string {
	b, err := json.Marshal(normalizeAllowedSpaces(spaces))
	if err != nil {
		panic(fmt.Sprintf("marshal node allowed spaces: %v", err))
	}
	return string(b)
}
