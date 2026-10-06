package legacyconv

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func Node(old *apigenold.Node) (*apigen.Node, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Node", "", "nil payload")
		return checked("Node", (*apigen.Node)(nil), c.err)
	}
	var roles []apigen.NodeRole
	for _, r := range old.Operator.Roles {
		roles = append(roles, apigen.NodeRole(r))
	}
	out := &apigen.Node{
		ID:                    c.id("Node", "id", int64(old.ID)),
		Status:                apigen.NodeLifecycleStatus(old.Status),
		EnrollmentRequestedAt: millis(old.EnrollmentRequestedAt),
		Operator: apigen.NodeOperator{
			Name:          old.Operator.Name,
			Roles:         roles,
			AllowedSpaces: c.ids("NodeOperator", "allowed_spaces", old.Operator.AllowedSpaces),
			EnrolledTime:  millis(old.Operator.EnrolledTime),
		},
		Reported: c.nodeReported(&old.Reported),
	}
	return checked("Node", out, c.err)
}

func (c *conv) nodeReported(old *apigenold.NodeReported) apigen.NodeReported {
	out := apigen.NodeReported{
		Identifier:           old.Identifier,
		UnderlayAddress:      c.addr("NodeReported", "underlay_address", old.UnderlayAddress),
		WgPublicKey:          old.WgPublicKey,
		HostAddressesUnknown: old.HostAddressesUnknown,
	}
	for _, s := range old.HostAddresses {
		out.HostAddresses = append(out.HostAddresses, c.addr("NodeReported", "host_addresses", s))
	}
	return out
}

func NodeStatus(old *apigenold.NodeStatus) (*apigen.NodeStatus, error) {
	c := &conv{}
	if old == nil {
		c.refuse("NodeStatus", "", "nil payload")
		return checked("NodeStatus", (*apigen.NodeStatus)(nil), c.err)
	}
	out := &apigen.NodeStatus{
		NodeID:            c.id("NodeStatus", "node_id", int64(old.NodeID)),
		UpdatedAt:         apigen.TimeOf(old.UpdatedAt),
		IsConnected:       old.IsConnected,
		LastConnectedAt:   apigen.TimeOf(old.LastConnectedAt),
		RemoteAddress:     old.RemoteAddress,
		OpendeployVersion: old.OpendeployVersion,
		RuntimeVersions:   old.RuntimeVersions,
	}
	return checked("NodeStatus", out, c.err)
}
