// Package network implements the virtual network layer: derived IPv6 ULA
// addressing, per-container network namespaces, host routing, IPv4 egress NAT,
// and host-port publishing. See docs/future-work/networking.md.
//
// Addresses are pure functions of existing identifiers — there is no IPAM
// state, allocator, or reuse policy. Layout of the 80 host bits after the /48
// ULA prefix:
//
//	<ULA /48> : <kind:16> : <deployment id:32> : <field:32>
package network

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Address kinds. Only KindInstance and KindRun route in Phase 1; KindService
// and KindMachine are reserved so later phases are purely additive.
const (
	KindInstance uint16 = 0 // field = instance ordinal (0-based)
	KindService  uint16 = 1 // field = 0; virtual per-deployment address (unrouted until socket LB)
	KindRun      uint16 = 2 // field = run number; preferred warmup source for rollover candidates
	KindMachine  uint16 = 3 // field = machine id, deployment id bits zero
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

// CIDR returns the routable /48 covering every address in the cluster.
func (p Prefix) CIDR() netip.Prefix {
	var a [16]byte
	copy(a[:], p[:])
	return netip.PrefixFrom(netip.AddrFrom16(a), PrefixLen*8)
}

func (p Prefix) String() string { return p.CIDR().String() }

func (p Prefix) addr(kind uint16, deploymentID uint32, field uint32) netip.Addr {
	var a [16]byte
	copy(a[:], p[:])
	binary.BigEndian.PutUint16(a[6:8], kind)
	binary.BigEndian.PutUint32(a[8:12], deploymentID)
	binary.BigEndian.PutUint32(a[12:16], field)
	return netip.AddrFrom16(a)
}

// InstanceAddr is the stable kind-0 address of instance (deploymentID, ordinal).
// It survives restarts, upgrades, and (in later phases) rescheduling.
func (p Prefix) InstanceAddr(deploymentID int32, ordinal int32) netip.Addr {
	return p.addr(KindInstance, uint32(deploymentID), uint32(ordinal))
}

// ServiceAddr is the kind-1 virtual per-deployment address. Reserved from day
// one; not routed until socket-level load balancing exists.
func (p Prefix) ServiceAddr(deploymentID int32) netip.Addr {
	return p.addr(KindService, uint32(deploymentID), 0)
}

// RunAddr is the kind-2 run-scoped temporary address used as a rollover
// candidate's preferred outbound source during warmup. The stable instance
// address is also preassigned as deprecated/non-preferred so promotion only has
// to flip the host route.
func (p Prefix) RunAddr(deploymentID int32, runNumber int32) netip.Addr {
	return p.addr(KindRun, uint32(deploymentID), uint32(runNumber))
}

// MachineAddr is the kind-3 mesh address of a machine (deployment id bits
// zero). Unrouted in Phase 1; becomes the host's own mesh address in Phase 2.
func (p Prefix) MachineAddr(machineID uint32) netip.Addr {
	return p.addr(KindMachine, 0, machineID)
}

// DeploymentCIDR covers every address of one deployment (all kinds share the
// deployment id bits only within a kind, so this is the kind-0 /80 block).
// Policy and filtering rules for a deployment match this single prefix.
func (p Prefix) DeploymentCIDR(kind uint16, deploymentID int32) netip.Prefix {
	return netip.PrefixFrom(p.addr(kind, uint32(deploymentID), 0), 96)
}
