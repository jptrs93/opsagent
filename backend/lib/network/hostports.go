package network

import (
	"sort"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/sys/unix"
)

// hostPortsEntry records one deployment's published ports and which container
// currently owns them. Ownership prevents a superseded runner's teardown from
// wiping the rules its rollover replacement just installed.
type hostPortsEntry struct {
	owner string // container id
	rules []HostPortRule
}

// ApplyHostPorts sets the desired host-port publishing rules for one
// deployment (owned by containerID) and rebuilds the machine's nftables
// ruleset from the full desired state.
func (m *Manager) ApplyHostPorts(deploymentID int32, containerID string, rules []HostPortRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, hadPrevious := m.hostPorts[deploymentID]
	if len(rules) == 0 {
		delete(m.hostPorts, deploymentID)
	} else {
		m.hostPorts[deploymentID] = hostPortsEntry{owner: containerID, rules: rules}
	}
	if err := m.reconcileNft(); err != nil {
		if hadPrevious {
			m.hostPorts[deploymentID] = previous
		} else {
			delete(m.hostPorts, deploymentID)
		}
		return err
	}
	return nil
}

// ClearHostPorts removes a deployment's host-port rules if containerID still
// owns them. A no-op when another container (a promoted rollover candidate)
// has taken them over.
func (m *Manager) ClearHostPorts(deploymentID int32, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.hostPorts[deploymentID]
	if !ok || entry.owner != containerID {
		return nil
	}
	delete(m.hostPorts, deploymentID)
	if deploymentID == m.netproxyDeploymentID {
		delete(m.current, deploymentID)
	}
	return m.reconcileNft()
}

// SetNetproxyIngress updates the rendered ingress listener set. It is derived
// state, not part of the opendeploy-net deployment spec.
func (m *Manager) SetNetproxyIngress(ingress []*apigen.NetIngress) error {
	ports := netproxyIngressPorts(ingress)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.netproxyIngressPorts = ports
	m.reconcileNetproxyHostPortsLocked()
	return m.reconcileNft()
}

func netproxyIngressPorts(ingress []*apigen.NetIngress) map[uint16]struct{} {
	ports := make(map[uint16]struct{})
	for _, route := range ingress {
		if route == nil {
			continue
		}
		switch route.Kind {
		case apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH:
			if route.TlsPassthrough == nil {
				continue
			}
			port := route.TlsPassthrough.HostPort
			if port >= 1 && port <= 65535 {
				ports[uint16(port)] = struct{}{}
			}
		case apigen.IngressKind_INGRESS_KIND_HTTPS:
			if route.Https == nil {
				continue
			}
			ports[443] = struct{}{}
			ports[80] = struct{}{}
		}
	}
	return ports
}

// PublishNetproxy publishes the current netproxy container and applies the
// ingress listener set already rendered from local deployment state.
func (m *Manager) PublishNetproxy(cn *ContainerNet) error {
	if cn == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previousCurrent, hadCurrent := m.current[m.netproxyDeploymentID]
	previousPorts, hadPorts := m.hostPorts[m.netproxyDeploymentID]
	m.current[m.netproxyDeploymentID] = cn
	m.reconcileNetproxyHostPortsLocked()
	if err := m.reconcileNft(); err != nil {
		if hadCurrent {
			m.current[m.netproxyDeploymentID] = previousCurrent
		} else {
			delete(m.current, m.netproxyDeploymentID)
		}
		if hadPorts {
			m.hostPorts[m.netproxyDeploymentID] = previousPorts
		} else {
			delete(m.hostPorts, m.netproxyDeploymentID)
		}
		return err
	}
	return nil
}

func (m *Manager) reconcileNetproxyHostPortsLocked() {
	if m.netproxyDeploymentID == 0 {
		return
	}
	cn := m.current[m.netproxyDeploymentID]
	if cn == nil || len(m.netproxyIngressPorts) == 0 {
		delete(m.hostPorts, m.netproxyDeploymentID)
		return
	}
	ports := make([]int, 0, len(m.netproxyIngressPorts))
	for port := range m.netproxyIngressPorts {
		ports = append(ports, int(port))
	}
	sort.Ints(ports)
	rules := make([]HostPortRule, 0, len(ports))
	for _, port := range ports {
		rules = append(rules, HostPortRule{
			Protocol:   unix.IPPROTO_TCP,
			HostPort:   uint16(port),
			TargetPort: uint16(port),
			TargetV6:   cn.InboundAddr,
			TargetV4:   cn.V4,
		})
	}
	m.hostPorts[m.netproxyDeploymentID] = hostPortsEntry{owner: cn.ContainerID, rules: rules}
}

// SetCurrentNet records the container currently receiving a deployment's stable
// route after publication or promotion.
func (m *Manager) SetCurrentNet(deploymentID int32, cn *ContainerNet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cn == nil {
		delete(m.current, deploymentID)
		return
	}
	m.current[deploymentID] = cn
}

func (m *Manager) CurrentNet(deploymentID int32) *ContainerNet {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current[deploymentID]
}

// DropCurrentNet clears the registration if containerID still holds it.
func (m *Manager) DropCurrentNet(deploymentID int32, containerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cn, ok := m.current[deploymentID]; ok && cn.ContainerID == containerID {
		delete(m.current, deploymentID)
	}
}
