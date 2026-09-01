// Package network implements the virtual network layer: derived IPv6 ULA
// addressing, per-container network namespaces, host routing, IPv4 egress NAT,
// and host-port publishing. See docs/engineering/networking.md.
//
// Addresses are pure functions of existing identifiers. Layout of the 80 bits
// after the cluster /48:
//
//	<space:16> : <deployment:24> : <ordinal:12> : <placement:20> : <run:8>
//
// The placement slot identifies one scheduled instance: the placement
// incarnation that owns a node assignment. Cross-node routing is prefix-based,
// so the /120 covering a placement's runs is the unit that routes to a node and
// the run slot never appears in a route. Deriving the slot from the scheduled
// instance id rather than the spec version keeps two live placements of one
// spec version distinct, which happens whenever an instance moves node
// without a version change.
package network

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
)

const (
	SpaceBits      = 16
	DeploymentBits = 24
	OrdinalBits    = 12
	PlacementBits  = 20
	RunBits        = 8

	MaxSpaceID       int32 = 1<<SpaceBits - 1
	MaxDeploymentID  int32 = 1<<DeploymentBits - 1
	MaxOrdinal       int32 = 1<<OrdinalBits - 1
	MaxPlacementSlot int32 = 1<<PlacementBits - 1
	MaxRunSlot       int32 = 1<<RunBits - 1

	SpacePrefixBits      = PrefixLen*8 + SpaceBits
	DeploymentPrefixBits = SpacePrefixBits + DeploymentBits
	// InstancePrefixBits covers one (deployment, ordinal) instance: its stable
	// inbound address and every placement of it. PlacementPrefixBits covers one
	// scheduled instance's runs. These two are the only routable prefix lengths.
	InstancePrefixBits  = DeploymentPrefixBits + OrdinalBits
	PlacementPrefixBits = InstancePrefixBits + PlacementBits

	deploymentShift = OrdinalBits + PlacementBits + RunBits
	ordinalShift    = PlacementBits + RunBits
	placementShift  = RunBits
)

// LogicalAddr is the decoded identity carried by one cluster logical address.
// PlacementSlot and RunSlot are both zero for a stable inbound address and both
// nonzero for a run-scoped outbound address.
type LogicalAddr struct {
	SpaceID       int32
	DeploymentID  int32
	Ordinal       int32
	PlacementSlot int32
	RunSlot       int32
}

func (a LogicalAddr) IsInbound() bool { return a.PlacementSlot == 0 && a.RunSlot == 0 }

func (a LogicalAddr) IsOutbound() bool { return a.PlacementSlot != 0 && a.RunSlot != 0 }

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

// CIDR returns the /48 covering the cluster's logical addresses.
func (p Prefix) CIDR() netip.Prefix {
	var a [16]byte
	copy(a[:], p[:])
	return netip.PrefixFrom(netip.AddrFrom16(a), PrefixLen*8)
}

func (p Prefix) String() string { return p.CIDR().String() }

// ParseAddr validates and decodes a stable inbound or run-scoped outbound
// address belonging to this cluster prefix.
func (p Prefix) ParseAddr(addr netip.Addr) (LogicalAddr, error) {
	if !addr.Is6() || addr.Zone() != "" || !p.CIDR().Contains(addr) {
		return LogicalAddr{}, fmt.Errorf("address %s is outside cluster prefix %s", addr, p)
	}
	raw := addr.As16()
	lower := binary.BigEndian.Uint64(raw[8:])
	decoded := LogicalAddr{
		SpaceID:       int32(binary.BigEndian.Uint16(raw[6:8])),
		DeploymentID:  int32((lower >> deploymentShift) & uint64(MaxDeploymentID)),
		Ordinal:       int32((lower >> ordinalShift) & uint64(MaxOrdinal)),
		PlacementSlot: int32((lower >> placementShift) & uint64(MaxPlacementSlot)),
		RunSlot:       int32(lower & uint64(MaxRunSlot)),
	}
	if decoded.DeploymentID == 0 {
		return LogicalAddr{}, fmt.Errorf("address %s has zero deployment id", addr)
	}
	if !decoded.IsInbound() && !decoded.IsOutbound() {
		return LogicalAddr{}, fmt.Errorf("address %s has invalid placement/run slots %d/%d", addr, decoded.PlacementSlot, decoded.RunSlot)
	}
	return decoded, nil
}

// ValidateRoutedAddr verifies an address that may appear as a direct workload
// route. Stable inbound and run-scoped outbound addresses are both routable.
func (p Prefix) ValidateRoutedAddr(addr netip.Addr) error {
	_, err := p.ParseAddr(addr)
	return err
}

// ValidateRoutedPrefix verifies a prefix that may appear in a cluster network
// map. Only whole instances (/100) and whole placements (/120) route to a node;
// anything else would either split an instance across nodes or pin a single run.
func (p Prefix) ValidateRoutedPrefix(prefix netip.Prefix) error {
	if prefix != prefix.Masked() {
		return fmt.Errorf("routed prefix %s has bits set below its length", prefix)
	}
	addr := prefix.Addr()
	if !addr.Is6() || addr.Zone() != "" || !p.CIDR().Contains(addr) {
		return fmt.Errorf("routed prefix %s is outside cluster prefix %s", prefix, p)
	}
	raw := addr.As16()
	lower := binary.BigEndian.Uint64(raw[8:])
	if int32((lower>>deploymentShift)&uint64(MaxDeploymentID)) == 0 {
		return fmt.Errorf("routed prefix %s has zero deployment id", prefix)
	}
	switch prefix.Bits() {
	case InstancePrefixBits:
		return nil
	case PlacementPrefixBits:
		if int32((lower>>placementShift)&uint64(MaxPlacementSlot)) == 0 {
			return fmt.Errorf("routed prefix %s has zero placement slot", prefix)
		}
		return nil
	default:
		return fmt.Errorf("routed prefix %s must be a /%d instance or /%d placement prefix",
			prefix, InstancePrefixBits, PlacementPrefixBits)
	}
}

func (p Prefix) addr(spaceID, deploymentID, ordinal, placementSlot, runSlot int32) (netip.Addr, error) {
	if spaceID < 0 || spaceID > MaxSpaceID {
		return netip.Addr{}, fmt.Errorf("space id %d is outside 0..%d", spaceID, MaxSpaceID)
	}
	if deploymentID < 0 || deploymentID > MaxDeploymentID {
		return netip.Addr{}, fmt.Errorf("deployment id %d is outside 0..%d", deploymentID, MaxDeploymentID)
	}
	if ordinal < 0 || ordinal > MaxOrdinal {
		return netip.Addr{}, fmt.Errorf("instance ordinal %d is outside 0..%d", ordinal, MaxOrdinal)
	}
	if placementSlot < 0 || placementSlot > MaxPlacementSlot {
		return netip.Addr{}, fmt.Errorf("placement slot %d is outside 0..%d", placementSlot, MaxPlacementSlot)
	}
	if runSlot < 0 || runSlot > MaxRunSlot {
		return netip.Addr{}, fmt.Errorf("run slot %d is outside 0..%d", runSlot, MaxRunSlot)
	}

	lower := uint64(uint32(deploymentID))<<deploymentShift |
		uint64(uint32(ordinal))<<ordinalShift |
		uint64(uint32(placementSlot))<<placementShift |
		uint64(uint32(runSlot))
	var raw [16]byte
	copy(raw[:], p[:])
	binary.BigEndian.PutUint16(raw[6:8], uint16(spaceID))
	binary.BigEndian.PutUint64(raw[8:], lower)
	return netip.AddrFrom16(raw), nil
}

// InboundAddr returns the stable address clients use to reach an instance. It
// survives restarts, upgrades, and rescheduling, but changes with its space or
// ordinal.
func (p Prefix) InboundAddr(spaceID, deploymentID, ordinal int32) (netip.Addr, error) {
	if deploymentID == 0 {
		return netip.Addr{}, fmt.Errorf("deployment id must be positive")
	}
	return p.addr(spaceID, deploymentID, ordinal, 0, 0)
}

// OutboundAddr returns the preferred source address for one complete container
// run. Full scheduled instance ids and run numbers are folded into nonzero
// address slots; callers retain the full values for status, logs, and container
// ids.
func (p Prefix) OutboundAddr(spaceID, deploymentID, ordinal, scheduledInstanceID, runNumber int32) (netip.Addr, error) {
	if runNumber < 1 {
		return netip.Addr{}, fmt.Errorf("run number must be positive, got %d", runNumber)
	}
	slot, err := placementSlot(deploymentID, scheduledInstanceID)
	if err != nil {
		return netip.Addr{}, err
	}
	return p.addr(spaceID, deploymentID, ordinal, slot, (runNumber-1)%MaxRunSlot+1)
}

// placementSlot folds a scheduled instance id into a nonzero slot, reserving
// zero for the stable inbound address. Two live placements of one instance can
// only collide after MaxPlacementSlot intervening scheduled instances, which
// SetupContainerNet rejects rather than silently sharing an address.
func placementSlot(deploymentID, scheduledInstanceID int32) (int32, error) {
	if deploymentID == 0 {
		return 0, fmt.Errorf("deployment id must be positive")
	}
	if scheduledInstanceID < 1 {
		return 0, fmt.Errorf("scheduled instance id must be positive, got %d", scheduledInstanceID)
	}
	return (scheduledInstanceID-1)%MaxPlacementSlot + 1, nil
}

// InstanceCIDR covers one (deployment, ordinal) instance: its stable inbound
// address and every placement of it. Cross-node routing points this prefix at
// the node hosting the serving placement.
func (p Prefix) InstanceCIDR(spaceID, deploymentID, ordinal int32) (netip.Prefix, error) {
	if deploymentID == 0 {
		return netip.Prefix{}, fmt.Errorf("deployment id must be positive")
	}
	a, err := p.addr(spaceID, deploymentID, ordinal, 0, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, InstancePrefixBits), nil
}

// PlacementCIDR covers every run of one scheduled instance. Cross-node routing
// points it at that placement's node for the placement's whole life, so reply
// traffic keeps reaching a draining placement after the instance prefix has
// been flipped to its replacement.
func (p Prefix) PlacementCIDR(spaceID, deploymentID, ordinal, scheduledInstanceID int32) (netip.Prefix, error) {
	slot, err := placementSlot(deploymentID, scheduledInstanceID)
	if err != nil {
		return netip.Prefix{}, err
	}
	a, err := p.addr(spaceID, deploymentID, ordinal, slot, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, PlacementPrefixBits), nil
}

// SpaceCIDR covers every logical address in one space.
func (p Prefix) SpaceCIDR(spaceID int32) (netip.Prefix, error) {
	a, err := p.addr(spaceID, 0, 0, 0, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, SpacePrefixBits), nil
}

// DeploymentCIDR covers every logical address owned by a deployment.
func (p Prefix) DeploymentCIDR(spaceID, deploymentID int32) (netip.Prefix, error) {
	a, err := p.addr(spaceID, deploymentID, 0, 0, 0)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, DeploymentPrefixBits), nil
}
