// Package network implements the virtual network layer: derived IPv6 ULA
// addressing, per-container network namespaces, host routing, IPv4 egress NAT,
// and host-port publishing. See docs/engineering/networking.md.
//
// Addresses are pure functions of existing identifiers. Layout of the 80 bits
// after the cluster /48:
//
//	<space:16> : <deployment:24> : <ordinal:12> : <version:20> : <run:8>
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
	VersionBits    = 20
	RunBits        = 8

	MaxSpaceID      int32 = 1<<SpaceBits - 1
	MaxDeploymentID int32 = 1<<DeploymentBits - 1
	MaxOrdinal      int32 = 1<<OrdinalBits - 1
	MaxVersionSlot  int32 = 1<<VersionBits - 1
	MaxRunSlot      int32 = 1<<RunBits - 1

	SpacePrefixBits      = PrefixLen*8 + SpaceBits
	DeploymentPrefixBits = SpacePrefixBits + DeploymentBits
	InstancePrefixBits   = DeploymentPrefixBits + OrdinalBits
	VersionPrefixBits    = InstancePrefixBits + VersionBits

	deploymentShift = OrdinalBits + VersionBits + RunBits
	ordinalShift    = VersionBits + RunBits
	versionShift    = RunBits
)

// LogicalAddr is the decoded identity carried by one cluster logical address.
// VersionSlot and RunSlot are both zero for a stable inbound address and both
// nonzero for a run-scoped outbound address.
type LogicalAddr struct {
	SpaceID      int32
	DeploymentID int32
	Ordinal      int32
	VersionSlot  int32
	RunSlot      int32
}

func (a LogicalAddr) IsInbound() bool { return a.VersionSlot == 0 && a.RunSlot == 0 }

func (a LogicalAddr) IsOutbound() bool { return a.VersionSlot != 0 && a.RunSlot != 0 }

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
		SpaceID:      int32(binary.BigEndian.Uint16(raw[6:8])),
		DeploymentID: int32((lower >> deploymentShift) & uint64(MaxDeploymentID)),
		Ordinal:      int32((lower >> ordinalShift) & uint64(MaxOrdinal)),
		VersionSlot:  int32((lower >> versionShift) & uint64(MaxVersionSlot)),
		RunSlot:      int32(lower & uint64(MaxRunSlot)),
	}
	if decoded.DeploymentID == 0 {
		return LogicalAddr{}, fmt.Errorf("address %s has zero deployment id", addr)
	}
	if !decoded.IsInbound() && !decoded.IsOutbound() {
		return LogicalAddr{}, fmt.Errorf("address %s has invalid version/run slots %d/%d", addr, decoded.VersionSlot, decoded.RunSlot)
	}
	return decoded, nil
}

// ValidateRoutedAddr verifies an address that may appear as a direct workload
// route. Stable inbound and run-scoped outbound addresses are both routable.
func (p Prefix) ValidateRoutedAddr(addr netip.Addr) error {
	_, err := p.ParseAddr(addr)
	return err
}

func (p Prefix) addr(spaceID, deploymentID, ordinal, versionSlot, runSlot int32) (netip.Addr, error) {
	if spaceID < 0 || spaceID > MaxSpaceID {
		return netip.Addr{}, fmt.Errorf("space id %d is outside 0..%d", spaceID, MaxSpaceID)
	}
	if deploymentID < 0 || deploymentID > MaxDeploymentID {
		return netip.Addr{}, fmt.Errorf("deployment id %d is outside 0..%d", deploymentID, MaxDeploymentID)
	}
	if ordinal < 0 || ordinal > MaxOrdinal {
		return netip.Addr{}, fmt.Errorf("instance ordinal %d is outside 0..%d", ordinal, MaxOrdinal)
	}
	if versionSlot < 0 || versionSlot > MaxVersionSlot {
		return netip.Addr{}, fmt.Errorf("version slot %d is outside 0..%d", versionSlot, MaxVersionSlot)
	}
	if runSlot < 0 || runSlot > MaxRunSlot {
		return netip.Addr{}, fmt.Errorf("run slot %d is outside 0..%d", runSlot, MaxRunSlot)
	}

	lower := uint64(uint32(deploymentID))<<deploymentShift |
		uint64(uint32(ordinal))<<ordinalShift |
		uint64(uint32(versionSlot))<<versionShift |
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
// run. Full config versions and run numbers are folded into nonzero address
// slots; callers retain the full values for status, logs, and container ids.
func (p Prefix) OutboundAddr(spaceID, deploymentID, ordinal, configVersion, runNumber int32) (netip.Addr, error) {
	if deploymentID == 0 {
		return netip.Addr{}, fmt.Errorf("deployment id must be positive")
	}
	if configVersion < 1 {
		return netip.Addr{}, fmt.Errorf("config version must be positive, got %d", configVersion)
	}
	if runNumber < 1 {
		return netip.Addr{}, fmt.Errorf("run number must be positive, got %d", runNumber)
	}
	versionSlot := (configVersion-1)%MaxVersionSlot + 1
	runSlot := (runNumber-1)%MaxRunSlot + 1
	return p.addr(spaceID, deploymentID, ordinal, versionSlot, runSlot)
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
