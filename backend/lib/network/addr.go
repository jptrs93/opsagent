// Package network implements the virtual network layer: derived IPv6 ULA
// addressing, per-container network namespaces, host routing, IPv4 egress NAT,
// and host-port publishing. See docs/future-work/networking.md.
//
// Addresses are pure functions of existing identifiers. Layout of the 80 bits
// after the cluster /48:
//
//	<node:17> : <space:17> : <deployment:26> : <kind:4> : <field:16>
//
// Node zero denotes a logical address. Cross-machine routing fills the source
// and destination node fields to produce locator addresses, then zeros them
// again before logical policy and workload delivery.
package network

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
)

const (
	NodeBits       = 17
	SpaceBits      = 17
	DeploymentBits = 26
	KindBits       = 4
	FieldBits      = 16

	LogicalNodeID   uint32 = 0
	MaxNodeID       uint32 = 1<<NodeBits - 1
	MaxSpaceID      int32  = 1<<SpaceBits - 1
	MaxDeploymentID int32  = 1<<DeploymentBits - 1
	MaxField        int32  = 1<<FieldBits - 1

	NodePrefixBits       = PrefixLen*8 + NodeBits
	SpacePrefixBits      = NodePrefixBits + SpaceBits
	DeploymentPrefixBits = SpacePrefixBits + DeploymentBits
	KindPrefixBits       = DeploymentPrefixBits + KindBits

	nodeShift       = SpaceBits + DeploymentBits + KindBits
	spaceShift      = DeploymentBits + KindBits
	deploymentShift = KindBits
	nodeMask        = uint64(MaxNodeID) << nodeShift
)

// Kind distinguishes addresses owned by one deployment. Values 3-15 are
// reserved so adding a future kind does not change the address layout.
type Kind uint8

const (
	KindInstance Kind = 0 // field = instance ordinal (0-based)
	KindService  Kind = 1 // field = 0; virtual per-deployment address
	KindRun      Kind = 2 // field = temporary run number/token
)

// PrefixLen is the ULA prefix length in bytes (48 bits).
const PrefixLen = 6

// Prefix is the cluster's RFC 4193 ULA /48 prefix. Generated once at first
// primary startup and immutable thereafter.
type Prefix [PrefixLen]byte

// GeneratePrefix returns a random RFC 4193 ULA /48: 0xfd followed by 40
// random bits (the Global ID).
func GeneratePrefix() Prefix {
	var p Prefix
	if _, err := rand.Read(p[1:]); err != nil {
		panic(fmt.Sprintf("generating ULA prefix: %v", err))
	}
	p[0] = 0xfd
	return p
}

// ParsePrefix validates a stored/distributed prefix value.
func ParsePrefix(b []byte) (Prefix, error) {
	var p Prefix
	if len(b) != PrefixLen {
		return p, fmt.Errorf("ULA prefix must be %d bytes, got %d", PrefixLen, len(b))
	}
	if b[0] != 0xfd {
		return p, fmt.Errorf("ULA prefix must start with 0xfd, got 0x%02x", b[0])
	}
	copy(p[:], b)
	return p, nil
}

func (p Prefix) IsZero() bool { return p == Prefix{} }

func (p Prefix) Bytes() []byte { return p[:] }

// CIDR returns the /48 covering logical and locator addresses in the cluster.
func (p Prefix) CIDR() netip.Prefix {
	var a [16]byte
	copy(a[:], p[:])
	return netip.PrefixFrom(netip.AddrFrom16(a), PrefixLen*8)
}

func (p Prefix) String() string { return p.CIDR().String() }

func (p Prefix) addr(nodeID uint32, spaceID, deploymentID int32, kind Kind, field uint16) (netip.Addr, error) {
	if nodeID > MaxNodeID {
		return netip.Addr{}, fmt.Errorf("network node id %d exceeds %d-bit maximum %d", nodeID, NodeBits, MaxNodeID)
	}
	if spaceID < 0 || spaceID > MaxSpaceID {
		return netip.Addr{}, fmt.Errorf("space id %d is outside 0..%d", spaceID, MaxSpaceID)
	}
	if deploymentID < 0 || deploymentID > MaxDeploymentID {
		return netip.Addr{}, fmt.Errorf("deployment id %d is outside 0..%d", deploymentID, MaxDeploymentID)
	}
	if uint8(kind) >= 1<<KindBits {
		return netip.Addr{}, fmt.Errorf("address kind %d exceeds %d-bit maximum", kind, KindBits)
	}

	upper := uint64(nodeID)<<nodeShift |
		uint64(uint32(spaceID))<<spaceShift |
		uint64(uint32(deploymentID))<<deploymentShift |
		uint64(kind)
	var a [16]byte
	copy(a[:], p[:])
	binary.BigEndian.PutUint64(a[6:14], upper)
	binary.BigEndian.PutUint16(a[14:16], field)
	return netip.AddrFrom16(a), nil
}

// InstanceAddr returns the stable logical address of an instance. It survives
// restarts, upgrades, and rescheduling, but changes when the deployment moves
// to another space.
func (p Prefix) InstanceAddr(spaceID, deploymentID, ordinal int32) (netip.Addr, error) {
	if ordinal < 0 || ordinal > MaxField {
		return netip.Addr{}, fmt.Errorf("instance ordinal %d is outside 0..%d", ordinal, MaxField)
	}
	return p.addr(LogicalNodeID, spaceID, deploymentID, KindInstance, uint16(ordinal))
}

// ServiceAddr returns the logical virtual address representing a deployment.
// It remains unrouted until socket-level load balancing is implemented.
func (p Prefix) ServiceAddr(spaceID, deploymentID int32) (netip.Addr, error) {
	return p.addr(LogicalNodeID, spaceID, deploymentID, KindService, 0)
}

// RunAddr returns a logical temporary address for a rollover candidate. Run
// fields wrap after 16 bits; only concurrently live runs must be distinct.
func (p Prefix) RunAddr(spaceID, deploymentID, runNumber int32) (netip.Addr, error) {
	if runNumber < 1 {
		return netip.Addr{}, fmt.Errorf("run number must be positive, got %d", runNumber)
	}
	return p.addr(LogicalNodeID, spaceID, deploymentID, KindRun, uint16(uint32(runNumber)))
}

// NodeCIDR returns the locator prefix owned by a node. Node zero is the logical
// address root; real routing node ids start at one.
func (p Prefix) NodeCIDR(nodeID uint32) (netip.Prefix, error) {
	a, err := p.addr(nodeID, 0, 0, KindInstance, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, NodePrefixBits), nil
}

// SpaceCIDR covers every logical address in one space.
func (p Prefix) SpaceCIDR(spaceID int32) (netip.Prefix, error) {
	a, err := p.addr(LogicalNodeID, spaceID, 0, KindInstance, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, SpacePrefixBits), nil
}

// DeploymentCIDR covers every logical kind and field owned by a deployment.
func (p Prefix) DeploymentCIDR(spaceID, deploymentID int32) (netip.Prefix, error) {
	a, err := p.addr(LogicalNodeID, spaceID, deploymentID, KindInstance, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, DeploymentPrefixBits), nil
}

// WithNode fills or zeros the node field without changing space, deployment,
// kind, or field. It is the address operation used by future cross-node
// logical-to-locator translation.
func (p Prefix) WithNode(addr netip.Addr, nodeID uint32) (netip.Addr, error) {
	if !addr.Is6() || !p.CIDR().Contains(addr) {
		return netip.Addr{}, fmt.Errorf("address %v is outside cluster prefix %s", addr, p)
	}
	if nodeID > MaxNodeID {
		return netip.Addr{}, fmt.Errorf("network node id %d exceeds %d-bit maximum %d", nodeID, NodeBits, MaxNodeID)
	}
	a := addr.As16()
	upper := binary.BigEndian.Uint64(a[6:14])
	upper = upper&^nodeMask | uint64(nodeID)<<nodeShift
	binary.BigEndian.PutUint64(a[6:14], upper)
	return netip.AddrFrom16(a), nil
}

// NodeID returns the logical zero or routing node encoded in a cluster address.
func (p Prefix) NodeID(addr netip.Addr) (uint32, error) {
	if !addr.Is6() || !p.CIDR().Contains(addr) {
		return 0, fmt.Errorf("address %v is outside cluster prefix %s", addr, p)
	}
	a := addr.As16()
	return uint32(binary.BigEndian.Uint64(a[6:14]) >> nodeShift), nil
}
