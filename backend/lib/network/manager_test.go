package network

import (
	"net/netip"
	"testing"
)

func TestDNSAddrUsesInternalSpaceLogicalAddress(t *testing.T) {
	p := GeneratePrefix()
	m := New(p, 42)
	got, ok := m.DNSAddr()
	if !ok {
		t.Fatal("DNS address is not available")
	}
	want, err := p.InboundAddr(0, 42, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DNS address = %s, want internal-space logical address %s", got, want)
	}
}

func TestRetainedContainerNets(t *testing.T) {
	empty := newRetainedContainerNets(nil)
	if empty.keepsContainer("opendeploy-10-v26") || empty.keepsHostVeth("od10s0") {
		t.Fatal("empty retained set kept network state")
	}

	current := &ContainerNet{ContainerID: "opendeploy-10-v26", HostVeth: "od10s0"}
	candidate := &ContainerNet{ContainerID: "opendeploy-10-v27", HostVeth: "od10s1"}
	retained := newRetainedContainerNets([]*ContainerNet{current, nil, candidate})

	for _, containerID := range []string{current.ContainerID, candidate.ContainerID} {
		if !retained.keepsContainer(containerID) {
			t.Fatalf("container %q was not retained", containerID)
		}
	}
	for _, hostVeth := range []string{current.HostVeth, candidate.HostVeth} {
		if !retained.keepsHostVeth(hostVeth) {
			t.Fatalf("host veth %q was not retained", hostVeth)
		}
	}
	if retained.keepsContainer("opendeploy-10-v25") {
		t.Fatal("stale container was retained")
	}
	if retained.keepsHostVeth("od10s2") {
		t.Fatal("stale host veth was retained")
	}
}

func TestRetainedContainerNetsIncludeActiveRunners(t *testing.T) {
	m := New(Prefix{}, 0)
	current := &ContainerNet{ContainerID: "opendeploy-10-v26", DeploymentID: 10, HostVeth: "od10s0"}
	candidate := &ContainerNet{ContainerID: "opendeploy-10-v27", DeploymentID: 10, HostVeth: "od10s1"}
	otherDeployment := &ContainerNet{ContainerID: "opendeploy-11-v1", DeploymentID: 11, HostVeth: "od11s0"}
	m.containerNets[current.ContainerID] = current
	m.containerNets[candidate.ContainerID] = candidate
	m.containerNets[otherDeployment.ContainerID] = otherDeployment

	retained := m.retainedContainerNetsForDeployment(10, nil)
	for _, cn := range []*ContainerNet{current, candidate} {
		if !retained.keepsContainer(cn.ContainerID) || !retained.keepsHostVeth(cn.HostVeth) {
			t.Fatalf("active network was not retained: %+v", cn)
		}
	}
	if retained.keepsContainer(otherDeployment.ContainerID) || retained.keepsHostVeth(otherDeployment.HostVeth) {
		t.Fatal("network from another deployment was retained")
	}
}

func TestOutboundAddressInUse(t *testing.T) {
	m := New(Prefix{}, 0)
	addr := mustAddr(GeneratePrefix().OutboundAddr(1, 10, 0, 2, 1))
	m.containerNets["old"] = &ContainerNet{ContainerID: "old", DeploymentID: 10, OutboundAddr: addr}
	if !m.outboundAddressInUse(10, "candidate", addr) {
		t.Fatal("live outbound collision was not detected")
	}
	if m.outboundAddressInUse(10, "old", addr) {
		t.Fatal("container collided with its own recovered address")
	}
	if m.outboundAddressInUse(11, "candidate", addr) {
		t.Fatal("address from another deployment was considered a local collision")
	}
}

func TestValidateContainerAddressIdentity(t *testing.T) {
	prefix := GeneratePrefix()
	inbound := mustAddr(prefix.InboundAddr(1, 10, 2))
	outbound := mustAddr(prefix.OutboundAddr(1, 10, 2, 3, 4))
	if err := validateContainerAddressIdentity(prefix, 10, inbound, outbound); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	tests := []struct {
		name              string
		deploymentID      int32
		inbound, outbound netip.Addr
	}{
		{"wrong deployment", 11, inbound, outbound},
		{"wrong space", 10, mustAddr(prefix.InboundAddr(2, 10, 2)), outbound},
		{"wrong ordinal", 10, mustAddr(prefix.InboundAddr(1, 10, 1)), outbound},
		{"inbound used as outbound", 10, inbound, inbound},
		{"outbound used as inbound", 10, outbound, outbound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateContainerAddressIdentity(prefix, tt.deploymentID, tt.inbound, tt.outbound); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestVethPeerIndexesMatch(t *testing.T) {
	tests := []struct {
		name                                          string
		hostIndex, hostPeer, containerIndex, contPeer int
		want                                          bool
	}{
		{name: "mutual peers", hostIndex: 630, hostPeer: 629, containerIndex: 629, contPeer: 630, want: true},
		{name: "host mismatch", hostIndex: 630, hostPeer: 628, containerIndex: 629, contPeer: 630},
		{name: "container mismatch", hostIndex: 630, hostPeer: 629, containerIndex: 629, contPeer: 631},
		{name: "missing host index", hostPeer: 629, containerIndex: 629, contPeer: 630},
		{name: "missing container index", hostIndex: 630, hostPeer: 629, contPeer: 630},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vethPeerIndexesMatch(tt.hostIndex, tt.hostPeer, tt.containerIndex, tt.contPeer)
			if got != tt.want {
				t.Fatalf("vethPeerIndexesMatch(%d, %d, %d, %d) = %v, want %v", tt.hostIndex, tt.hostPeer, tt.containerIndex, tt.contPeer, got, tt.want)
			}
		})
	}
}
