//go:build !linux

package network

import (
	"fmt"
	"net/netip"
)

// Non-linux stubs so the agent compiles and tests run on development machines.
// The virtual network requires linux; SetupContainerNet failing keeps
// virtual-mode deployments from silently running unwired.

type ContainerNetSpec struct {
	ContainerID              string
	DeploymentID             int32
	InboundAddr              netip.Addr
	OutboundAddr             netip.Addr
	UnprivilegedPortStart    int
	SetUnprivilegedPortStart bool
}

func (m *Manager) EnsureBase() error {
	return fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) SetupContainerNet(spec ContainerNetSpec) (*ContainerNet, error) {
	return nil, fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) RecoverContainerNet(containerID string, deploymentID int32, inboundAddr, outboundAddr netip.Addr) (*ContainerNet, error) {
	return nil, fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) Activate(candidate *ContainerNet) error {
	return fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) TeardownContainerNet(cn *ContainerNet) {}

func (m *Manager) TeardownContainerNetState(containerID string, deploymentID int32) {}

func (m *Manager) CleanupContainerNets(deploymentID int32, keep []*ContainerNet) {}

func (m *Manager) ReconcileTopology(topology Topology) error {
	return fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) reconcileNft() error { return nil }
