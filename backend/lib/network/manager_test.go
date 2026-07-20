package network

import "testing"

func TestDNSAddrUsesInternalSpaceLogicalAddress(t *testing.T) {
	p := GeneratePrefix()
	m := New(p, 42)
	got, ok := m.DNSAddr()
	if !ok {
		t.Fatal("DNS address is not available")
	}
	want, err := p.InstanceAddr(0, 42, 0)
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
