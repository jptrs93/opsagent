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
	Addr                     netip.Addr
	DeprecatedAddrs          []netip.Addr
	UnprivilegedPortStart    int
	SetUnprivilegedPortStart bool
}

func (m *Manager) EnsureBase() error {
	return fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) SetupContainerNet(spec ContainerNetSpec) (*ContainerNet, error) {
	return nil, fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) Promote(oldNet, candidate *ContainerNet, stable netip.Addr) error {
	return fmt.Errorf("virtual networking requires linux")
}

func (m *Manager) TeardownContainerNet(containerID string, deploymentID int32) {}

func (m *Manager) CleanupContainerNets(deploymentID int32, keep func(containerID string) bool) {}

func (m *Manager) reconcileNft() error { return nil }
