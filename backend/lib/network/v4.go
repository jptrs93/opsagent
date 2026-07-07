package network

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// IPv4 egress addressing. Containers get a machine-local private IPv4 for
// internet egress only: the same fixed range is reused identically on every
// machine, never routed off-host, and masqueraded to the host's default
// interface. IPv4 is never used for mesh, service, or discovery traffic.
//
// Each (deployment, slot) pair maps to a /30: host side .1, container side .2.
// Two slots per deployment cover the only case where two containers of one
// deployment run concurrently (a rollover candidate beside the current
// container). The slot is chosen at netns setup time from live kernel state
// (which host veths exist), so there is no allocation store.

// v4Base is the machine-local egress range. Chosen to avoid the common
// defaults of docker (172.17/16), podman (10.88/16), and Tailscale (100.64/10).
var v4Base = netip.MustParseAddr("10.201.0.0")

// V4CIDR is the whole machine-local egress range (for the masquerade rule).
var V4CIDR = netip.PrefixFrom(v4Base, 16)

// v4SlotsPerDeployment is the number of concurrent containers one deployment
// can have on a machine (current + rollover candidate).
const v4SlotsPerDeployment = 2

// V4Pair returns the host-side and container-side addresses of the /30 for
// (deploymentID, slot). Deployment ids collide in this space only modulo 8192,
// i.e. after 8192 deployments on one machine.
func V4Pair(deploymentID int32, slot int) (host netip.Addr, container netip.Addr, err error) {
	if slot < 0 || slot >= v4SlotsPerDeployment {
		return host, container, fmt.Errorf("v4 slot out of range: %d", slot)
	}
	base := binary.BigEndian.Uint32(v4Base.AsSlice())
	offset := (uint32(deploymentID)&0x1FFF)*4*v4SlotsPerDeployment + uint32(slot)*4
	var h, c [4]byte
	binary.BigEndian.PutUint32(h[:], base+offset+1)
	binary.BigEndian.PutUint32(c[:], base+offset+2)
	return netip.AddrFrom4(h), netip.AddrFrom4(c), nil
}
