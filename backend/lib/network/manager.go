package network

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/jptrs93/goutil/logu"
)

// Manager is the machine-local networking reconciler: it owns per-container
// netns/veth/route state and the machine's nftables ruleset (IPv4 egress
// masquerade and port-forwarding DNAT). It runs in-process in the agent; the
// network data plane itself is the kernel, so manager availability is
// irrelevant to existing traffic. All operations are full-state and idempotent.
//
// Default is the process-wide instance, wired by the bootstrap.
var Default = New(Prefix{}, 0)

func New(prefix Prefix, netproxyDeploymentID int32) *Manager {
	return &Manager{
		ctx:                  logu.AddTag(context.Background(), "Network"),
		prefix:               prefix,
		hasPrefix:            !prefix.IsZero(),
		netproxyDeploymentID: netproxyDeploymentID,
		containerNets:        map[string]*ContainerNet{},
		hostPorts:            map[int32]hostPortsEntry{},
		netproxyIngressPorts: map[uint16]struct{}{},
		current:              map[int32]*ContainerNet{},
	}
}

func SetDefault(manager *Manager) {
	Default = manager
}

type Manager struct {
	// ctx is the component root logging context, tagged at construction.
	ctx         context.Context
	mu          sync.Mutex
	containerMu sync.Mutex
	prefix      Prefix
	hasPrefix   bool
	// containerNets tracks networks created or recovered by this process. It is
	// guarded by containerMu and lets stale cleanup preserve concurrent runners.
	containerNets map[string]*ContainerNet

	// netproxyDeploymentID identifies this machine's netproxy system
	// deployment; the local DNS address derives from it.
	netproxyDeploymentID int32

	// hostPorts is the desired DNAT state per deployment; the nftables table is
	// rebuilt from this map on every change.
	hostPorts map[int32]hostPortsEntry

	// netproxyIngressPorts is the node-local ingress listener set rendered into
	// NetState. It is forwarded to the current netproxy container separately
	// from the immutable netproxy deployment spec.
	netproxyIngressPorts map[uint16]struct{}

	// current tracks which container currently receives each deployment's stable
	// route after publication or promotion.
	current map[int32]*ContainerNet

	baseOnce sync.Once
	baseErr  error
}

// HostPortRule publishes one container port on the machine's host interfaces.
// IPv4 traffic DNATs to the container's machine-local v4; IPv6 traffic DNATs
// to the stable inbound address I.
type HostPortRule struct {
	Protocol   uint8
	HostPort   uint16
	TargetPort uint16
	TargetV6   netip.Addr
	TargetV4   netip.Addr
}

// ContainerNet describes the netns wiring of one running container.
type ContainerNet struct {
	ContainerID  string
	DeploymentID int32
	NetnsPath    string // bind-mount path for the OCI spec network namespace
	HostVeth     string // host-side veth name
	// HostVethIndex rejects a reused interface name during teardown.
	HostVethIndex int
	// InboundAddr is stable for the instance and routed only to the current run.
	InboundAddr netip.Addr
	// OutboundAddr is preferred and routed for this network namespace's lifetime.
	OutboundAddr netip.Addr
	V4           netip.Addr // container-side machine-local v4
	HostV4       netip.Addr
	Slot         int
}

func (m *Manager) outboundAddressInUse(deploymentID int32, containerID string, addr netip.Addr) bool {
	for _, cn := range m.containerNets {
		if cn.DeploymentID == deploymentID && cn.ContainerID != containerID && cn.OutboundAddr == addr {
			return true
		}
	}
	return false
}

func validateContainerAddressIdentity(prefix Prefix, deploymentID int32, inboundAddr, outboundAddr netip.Addr) error {
	outbound, err := prefix.ParseAddr(outboundAddr)
	if err != nil {
		return fmt.Errorf("invalid outbound address %v: %w", outboundAddr, err)
	}
	if !outbound.IsOutbound() {
		return fmt.Errorf("outbound address %v uses the inbound address form", outboundAddr)
	}
	inbound, err := prefix.ParseAddr(inboundAddr)
	if err != nil {
		return fmt.Errorf("invalid inbound address %v: %w", inboundAddr, err)
	}
	if !inbound.IsInbound() {
		return fmt.Errorf("inbound address %v uses the outbound address form", inboundAddr)
	}
	if outbound.SpaceID != inbound.SpaceID || outbound.DeploymentID != inbound.DeploymentID || outbound.Ordinal != inbound.Ordinal || inbound.DeploymentID != deploymentID {
		return fmt.Errorf("container inbound and outbound identities do not match deployment %d", deploymentID)
	}
	return nil
}

// Tunnel describes one fixed protocol-41 tunnel to a remote cluster node.
// Local and Remote must belong to the same underlay address family.
type Tunnel struct {
	NodeID int32
	Local  netip.Addr
	Remote netip.Addr
}

// RemoteRoute selects a tunnel for one routed logical prefix: either a whole
// instance (/100) or a whole placement (/120).
type RemoteRoute struct {
	Prefix netip.Prefix
	NodeID int32
}

// Topology is the complete remote dataplane state derived from a network-map
// snapshot. Local workload routes remain owned by container lifecycle methods.
type Topology struct {
	Prefix      Prefix
	LocalNodeID int32
	Tunnels     []Tunnel
	Routes      []RemoteRoute
}

type retainedContainerNets struct {
	containerIDs map[string]struct{}
	hostVeths    map[string]struct{}
}

func newRetainedContainerNets(nets []*ContainerNet) retainedContainerNets {
	retained := retainedContainerNets{
		containerIDs: make(map[string]struct{}, len(nets)),
		hostVeths:    make(map[string]struct{}, len(nets)),
	}
	for _, cn := range nets {
		if cn == nil {
			continue
		}
		retained.containerIDs[cn.ContainerID] = struct{}{}
		retained.hostVeths[cn.HostVeth] = struct{}{}
	}
	return retained
}

func (m *Manager) retainedContainerNetsForDeployment(deploymentID int32, nets []*ContainerNet) retainedContainerNets {
	all := make([]*ContainerNet, 0, len(nets)+len(m.containerNets))
	all = append(all, nets...)
	for _, cn := range m.containerNets {
		if cn.DeploymentID == deploymentID {
			all = append(all, cn)
		}
	}
	return newRetainedContainerNets(all)
}

func (r retainedContainerNets) keepsContainer(containerID string) bool {
	_, ok := r.containerIDs[containerID]
	return ok
}

func (r retainedContainerNets) keepsHostVeth(hostVeth string) bool {
	_, ok := r.hostVeths[hostVeth]
	return ok
}

func vethPeerIndexesMatch(hostIndex, hostPeerIndex, containerIndex, containerPeerIndex int) bool {
	// IFLA_LINK is exposed as ParentIndex. For veths it contains the peer's
	// ifindex, including when the peer is in another network namespace.
	return hostIndex > 0 && containerIndex > 0 &&
		hostPeerIndex == containerIndex && containerPeerIndex == hostIndex
}

// SetPrefix installs the cluster ULA prefix (from primary config locally, or
// from the cluster stream on workers).
func (m *Manager) SetPrefix(p Prefix) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prefix = p
	m.hasPrefix = !p.IsZero()
}

func (m *Manager) PrefixValue() (Prefix, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prefix, m.hasPrefix
}

// SetNetproxyDeploymentID records this machine's netproxy system deployment;
// the machine-local DNS address is its instance address. Containers spawned
// afterwards get a resolv.conf pointing at it.
func (m *Manager) SetNetproxyDeploymentID(id int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.netproxyDeploymentID = id
}

// DNSAddr returns the machine-local netproxy DNS address, and whether both the
// prefix and the netproxy deployment are known yet.
func (m *Manager) DNSAddr() (netip.Addr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasPrefix || m.netproxyDeploymentID == 0 {
		return netip.Addr{}, false
	}
	addr, err := m.prefix.InboundAddr(0, m.netproxyDeploymentID, 0)
	return addr, err == nil
}

func (m *Manager) IsNetproxyDeployment(id int32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.netproxyDeploymentID != 0 && id == m.netproxyDeploymentID
}
