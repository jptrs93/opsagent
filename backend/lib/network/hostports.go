package network

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
	if len(rules) == 0 {
		delete(m.hostPorts, deploymentID)
	} else {
		m.hostPorts[deploymentID] = hostPortsEntry{owner: containerID, rules: rules}
	}
	return m.reconcileNft()
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
	return m.reconcileNft()
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

// CurrentNet returns the recorded current container network, if any.
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
