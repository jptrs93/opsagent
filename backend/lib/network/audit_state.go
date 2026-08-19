package network

import "net/netip"

// AuditRoute is one /128 workload route the manager believes is installed:
// a container's outbound address, or the stable inbound address of the run
// currently activated for a deployment.
type AuditRoute struct {
	Addr      netip.Addr
	LinkIndex int
}

// AuditState is a point-in-time snapshot of the kernel networking state the
// manager believes it has installed, consumed by the netaudit package.
type AuditState struct {
	HostPortRules  []HostPortRule
	WorkloadRoutes []AuditRoute
	Prefix         Prefix
	HasPrefix      bool
}

// AuditSnapshot captures the manager's desired kernel state. The two mutexes
// are taken sequentially, never nested, so the snapshot is only consistent
// per-section — acceptable for an observational audit that rechecks before
// reporting divergence.
func (m *Manager) AuditSnapshot() AuditState {
	var s AuditState
	m.mu.Lock()
	s.Prefix, s.HasPrefix = m.prefix, m.hasPrefix
	for _, entry := range m.hostPorts {
		s.HostPortRules = append(s.HostPortRules, entry.rules...)
	}
	for _, cn := range m.current {
		if cn != nil && cn.InboundAddr.Is6() && cn.HostVethIndex > 0 {
			s.WorkloadRoutes = append(s.WorkloadRoutes, AuditRoute{Addr: cn.InboundAddr, LinkIndex: cn.HostVethIndex})
		}
	}
	m.mu.Unlock()

	m.containerMu.Lock()
	for _, cn := range m.containerNets {
		if cn != nil && cn.OutboundAddr.Is6() && cn.HostVethIndex > 0 {
			s.WorkloadRoutes = append(s.WorkloadRoutes, AuditRoute{Addr: cn.OutboundAddr, LinkIndex: cn.HostVethIndex})
		}
	}
	m.containerMu.Unlock()
	return s
}
