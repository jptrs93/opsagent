package network

import (
	"net/netip"
	"sync"
)

// Manager is the machine-local networking reconciler: it owns per-container
// netns/veth/route state and the machine's nftables ruleset (IPv4 egress
// masquerade and port-forwarding DNAT). It runs in-process in the agent; the
// dataplane itself is the kernel, so manager availability is irrelevant to
// existing traffic. All operations are full-state and idempotent.
//
// Default is the process-wide instance, wired by the bootstrap (same pattern
// as runner.Containerd).
var Default = NewManager()

func NewManager() *Manager {
	return &Manager{
		hostPorts: map[int32]hostPortsEntry{},
		current:   map[int32]*ContainerNet{},
	}
}

type Manager struct {
	mu        sync.Mutex
	prefix    Prefix
	hasPrefix bool

	// dataplaneDeploymentID identifies this machine's dataplane system
	// deployment; the local DNS address derives from it.
	dataplaneDeploymentID int32

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

// SetDataplaneDeploymentID records this machine's dataplane system deployment;
// the machine-local DNS address is its instance address. Containers spawned
// afterwards get a resolv.conf pointing at it.
func (m *Manager) SetDataplaneDeploymentID(id int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dataplaneDeploymentID = id
}

// DNSAddr returns the machine-local dataplane DNS address, and whether both
// the prefix and the dataplane deployment are known yet.
func (m *Manager) DNSAddr() (netip.Addr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasPrefix || m.dataplaneDeploymentID == 0 {
		return netip.Addr{}, false
	}
	return m.prefix.InstanceAddr(m.dataplaneDeploymentID, 0), true
}

// IsDataplaneDeployment reports whether id is this machine's dataplane system
// deployment.
func (m *Manager) IsDataplaneDeployment(id int32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dataplaneDeploymentID != 0 && id == m.dataplaneDeploymentID
}
