package network

import (
	"net/netip"
	"slices"
)

// AuditRoute is one /128 workload route the manager believes is installed:
// a container's outbound address, or the stable inbound address of the run
// currently activated for a deployment.
type AuditRoute struct {
	Addr      netip.Addr
	LinkIndex int
}

// WGAuditPeer is one desired WireGuard peer of the last applied topology.
type WGAuditPeer struct {
	NodeID     int32
	PublicKey  string
	Endpoint   netip.AddrPort
	AllowedIPs []netip.Prefix
}

// WGAuditState is the WireGuard device state the manager believes it has
// installed: local identity, listen port, and the full peer set.
type WGAuditState struct {
	PublicKey  string
	ListenPort uint16
	Peers      []WGAuditPeer
}

// AuditState is a point-in-time snapshot of the kernel networking state the
// manager believes it has installed, consumed by the netaudit package.
type AuditState struct {
	HostPortRules        []HostPortRule
	WorkloadRoutes       []AuditRoute
	Prefix               Prefix
	HasPrefix            bool
	NetproxyDeploymentID int32
	FilterNets           []*ContainerNet
	PolicyRules          []PolicyRule
	// WG is nil until a topology with an active WireGuard device has been
	// applied; a nil WG with a live device is itself a divergence the audit
	// does not chase (the next reconcile owns it).
	WG *WGAuditState
}

func (s AuditState) FilterState() FilterState {
	return RenderFilterState(s.Prefix, s.HasPrefix, s.NetproxyDeploymentID, s.FilterNets, s.PolicyRules)
}

// AuditSnapshot captures the manager's desired kernel state. The two mutexes
// are taken sequentially, never nested, so the snapshot is only consistent
// per-section — acceptable for an observational audit that rechecks before
// reporting divergence.
func (m *Manager) AuditSnapshot() AuditState {
	var s AuditState
	m.mu.Lock()
	s.Prefix, s.HasPrefix = m.prefix, m.hasPrefix
	s.NetproxyDeploymentID = m.netproxyDeploymentID
	s.FilterNets = m.filterNetList()
	s.PolicyRules = slices.Clone(m.policyRules)
	if m.wgDesired != nil {
		wg := *m.wgDesired
		wg.Peers = slices.Clone(m.wgDesired.Peers)
		s.WG = &wg
	}
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
