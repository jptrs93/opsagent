package network

import (
	"net/netip"
	"slices"
	"sort"

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
		m.scheduleReconcileRetryLocked()
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
	// The removal is authoritative — the container is going away regardless —
	// so a failed rebuild is not rolled back. Without a retry the kernel would
	// keep forwarding the cleared ports until the next unrelated rebuild.
	if err := m.reconcileNft(); err != nil {
		m.scheduleReconcileRetryLocked()
		return err
	}
	return nil
}

// SetNetproxyPublish replaces the ingress publish set the primary evaluated
// for this node. It is derived state, not part of the opendeploy-net
// deployment spec.
func (m *Manager) SetNetproxyPublish(entries []IngressPublish) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.netproxyPublish = slices.Clone(entries)
	m.reconcileNetproxyHostPortsLocked()
	// The publish set is authoritative derived state; converge the kernel by
	// retrying rather than rolling back.
	if err := m.reconcileNft(); err != nil {
		m.scheduleReconcileRetryLocked()
		return err
	}
	return nil
}

// netproxyHostPortRules groups a publish set by port: one rule per port whose
// Dest lists the addresses published on it. A wildcard entry for a port
// widens that port to every local address.
func netproxyHostPortRules(cn *ContainerNet, entries []IngressPublish) []HostPortRule {
	type portDests struct {
		wildcard bool
		dests    []netip.Prefix
	}
	byPort := make(map[uint16]*portDests)
	for _, entry := range entries {
		if entry.Port == 0 {
			continue
		}
		group := byPort[entry.Port]
		if group == nil {
			group = &portDests{}
			byPort[entry.Port] = group
		}
		if !entry.Address.IsValid() {
			group.wildcard = true
			continue
		}
		addr := entry.Address.Unmap()
		group.dests = append(group.dests, netip.PrefixFrom(addr, addr.BitLen()))
	}
	ports := make([]int, 0, len(byPort))
	for port := range byPort {
		ports = append(ports, int(port))
	}
	sort.Ints(ports)
	rules := make([]HostPortRule, 0, len(ports))
	for _, port := range ports {
		group := byPort[uint16(port)]
		rule := HostPortRule{
			Protocol:   unix.IPPROTO_TCP,
			HostPort:   uint16(port),
			TargetPort: uint16(port),
			TargetV6:   cn.InboundAddr,
			TargetV4:   cn.V4,
		}
		if !group.wildcard {
			slices.SortFunc(group.dests, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
			rule.Dest = slices.Compact(group.dests)
		}
		rules = append(rules, rule)
	}
	return rules
}

// PublishNetproxy publishes the current netproxy container and applies the
// ingress publish set already received from the primary.
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
		m.scheduleReconcileRetryLocked()
		return err
	}
	return nil
}

func (m *Manager) reconcileNetproxyHostPortsLocked() {
	if m.netproxyDeploymentID == 0 {
		return
	}
	cn := m.current[m.netproxyDeploymentID]
	if cn == nil {
		delete(m.hostPorts, m.netproxyDeploymentID)
		return
	}
	rules := netproxyHostPortRules(cn, m.netproxyPublish)
	if len(rules) == 0 {
		delete(m.hostPorts, m.netproxyDeploymentID)
		return
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
