package network

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
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
		filterNets:           map[string]*ContainerNet{},
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

	filterNets map[string]*ContainerNet

	policyRules []PolicyRule

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

	// baseDone latches EnsureBase success only; a failed attempt is retried on
	// the next call rather than cached for the process lifetime.
	baseMu   sync.Mutex
	baseDone bool

	// retryPending dedupes scheduled reconcile retries. Guarded by mu.
	retryPending bool

	nftSkeletonReady bool
	nftNatHash       uint64
	nftDstChains     map[string]uint64

	// wgPrivateKey is the node-local transport key loaded at boot; nil until
	// SetWGPrivateKey. Guarded by mu.
	wgPrivateKey *wgtypes.Key

	// wgDesired is the WireGuard state of the last successfully reconciled
	// topology, kept for netaudit. Guarded by mu.
	wgDesired *WGAuditState
}

const reconcileRetryDelay = 5 * time.Second

// scheduleReconcileRetryLocked arranges a background rebuild after a failed
// one. Two failure modes require it: a rejected batch following a
// desired-state removal (the removal is authoritative and cannot be rolled
// back, so the kernel keeps publishing stale rules until a rebuild succeeds),
// and a batch the kernel actually committed although Flush reported an error
// (netlink ack loss), which leaves the kernel out of sync with the rolled-back
// desired state. Rebuilding from desired state converges both. Caller holds
// m.mu.
func (m *Manager) scheduleReconcileRetryLocked() {
	if m.retryPending {
		return
	}
	m.retryPending = true
	time.AfterFunc(reconcileRetryDelay, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.retryPending = false
		if err := m.reconcileNft(); err != nil {
			slog.WarnContext(m.ctx, "retrying nftables rebuild failed", "err", err)
			m.scheduleReconcileRetryLocked()
		}
	})
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
	Filtered   bool
	AllowV4    []netip.Prefix
	AllowV6    []netip.Prefix
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

// DefaultWGListenPort is the cluster-wide WireGuard UDP listen port stamped
// into the network map. Deliberately not the stock 51820, so a host already
// running its own WireGuard does not collide with the managed device.
const DefaultWGListenPort uint16 = 51833

// SetWGPrivateKey installs the node-local WireGuard private key loaded at
// boot. The key never appears in the network map; the map only carries each
// node's registered public key, and the reconciler pairs that with this
// identity to configure the device.
func (m *Manager) SetWGPrivateKey(key wgtypes.Key) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wgPrivateKey = &key
}

func (m *Manager) wgPrivateKeyValue() (wgtypes.Key, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wgPrivateKey == nil {
		return wgtypes.Key{}, false
	}
	return *m.wgPrivateKey, true
}

// Peer describes the WireGuard pairing to one remote cluster node: the
// underlay endpoint plus the remote's registered public key and listen port
// from the network map. Every member node is keyed, so a peer without a key
// is a map rendering bug, not a transport downgrade.
type Peer struct {
	NodeID   int32
	Endpoint netip.Addr
	WGKey    string // base64 public key
	WGPort   uint16
}

// RemoteRoute selects a peer for one routed logical prefix: either a whole
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
	LocalWGPort uint16
	Peers       []Peer
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
// from the cluster stream on secondaries).
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

func (m *Manager) syncFilterNets() error {
	nets := make(map[string]*ContainerNet, len(m.containerNets))
	for id, cn := range m.containerNets {
		nets[id] = cn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.filterNets
	m.filterNets = nets
	if err := m.reconcileNft(); err != nil {
		m.filterNets = previous
		m.scheduleReconcileRetryLocked()
		return err
	}
	return nil
}

func (m *Manager) filterNetList() []*ContainerNet {
	nets := make([]*ContainerNet, 0, len(m.filterNets))
	for _, cn := range m.filterNets {
		nets = append(nets, cn)
	}
	return nets
}

func (m *Manager) SetPolicyRules(rules []PolicyRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.policyRules
	m.policyRules = rules
	if err := m.reconcileNft(); err != nil {
		m.policyRules = previous
		m.scheduleReconcileRetryLocked()
		return err
	}
	return nil
}

func (m *Manager) IsNetproxyDeployment(id int32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.netproxyDeploymentID != 0 && id == m.netproxyDeploymentID
}
