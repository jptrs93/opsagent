package network

import (
	"net/netip"
	"sync"
)

// Manager is the machine-local networking reconciler: it owns per-container
// netns/veth/route state and the machine's nftables ruleset (IPv4 egress
// masquerade and port-forwarding DNAT). It runs in-process in the agent; the
// network data plane itself is the kernel, so manager availability is
// irrelevant to existing traffic. All operations are full-state and idempotent.
//
// Default is the process-wide instance, wired by the bootstrap.
var Default = New(Prefix{}, 0)

// New returns a manager initialized with the machine's cluster network identity.
func New(prefix Prefix, netproxyDeploymentID int32) *Manager {
	return &Manager{
		prefix:               prefix,
		hasPrefix:            !prefix.IsZero(),
		netproxyDeploymentID: netproxyDeploymentID,
		hostPorts:            map[int32]hostPortsEntry{},
		current:              map[int32]*ContainerNet{},
	}
}

// SetDefault installs the process-wide manager during application startup.
func SetDefault(manager *Manager) {
	Default = manager
}

type Manager struct {
	mu        sync.Mutex
	prefix    Prefix
	hasPrefix bool

	// netproxyDeploymentID identifies this machine's netproxy system
	// deployment; the local DNS address derives from it.
	netproxyDeploymentID int32

	// hostPorts is the desired DNAT state per deployment; the nftables table is
	// rebuilt from this map on every change.
	hostPorts map[int32]hostPortsEntry

	// current tracks which container currently receives each deployment's stable
	// route after publication or promotion.
	current map[int32]*ContainerNet

	baseOnce sync.Once
	baseErr  error
}

// HostPortRule publishes one container port on the machine's host interfaces.
// IPv4 traffic DNATs to the container's machine-local v4; IPv6 traffic DNATs
// to the stable instance address.
type HostPortRule struct {
	Protocol   uint8
	HostPort   uint16
	TargetPort uint16
	TargetV6   netip.Addr
	TargetV4   netip.Addr
}

// ContainerNet describes the netns wiring of one running container.
type ContainerNet struct {
	ContainerID string
	NetnsPath   string     // bind-mount path for the OCI spec network namespace
	HostVeth    string     // host-side veth name
	Addr        netip.Addr // routed v6 address (stable instance addr, or run addr for candidates)
	V4          netip.Addr // container-side machine-local v4
	HostV4      netip.Addr
	Slot        int
}

// SetPrefix installs the cluster ULA prefix (from primary config locally, or
// from the cluster stream on workers).
func (m *Manager) SetPrefix(p Prefix) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prefix = p
	m.hasPrefix = !p.IsZero()
}

// PrefixValue returns the cluster ULA prefix, and whether it is known yet.
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
	return m.prefix.InstanceAddr(m.netproxyDeploymentID, 0), true
}

// IsNetproxyDeployment reports whether id is this machine's netproxy system
// deployment.
func (m *Manager) IsNetproxyDeployment(id int32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.netproxyDeploymentID != 0 && id == m.netproxyDeploymentID
}
