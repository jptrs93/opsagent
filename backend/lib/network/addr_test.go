package network

import (
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, b []byte) Prefix {
	t.Helper()
	p, err := ParsePrefix(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustAddr(addr netip.Addr, err error) netip.Addr {
	if err != nil {
		panic(err)
	}
	return addr
}

func TestGeneratePrefixIsULA(t *testing.T) {
	p := GeneratePrefix()
	if p[0] != 0xfd {
		t.Fatalf("prefix does not start with fd: %v", p)
	}
	if _, err := ParsePrefix(p.Bytes()); err != nil {
		t.Fatalf("generated prefix does not parse: %v", err)
	}
	if p == GeneratePrefix() {
		t.Fatal("two generated prefixes are identical")
	}
}

func TestParsePrefixRejectsInvalid(t *testing.T) {
	if _, err := ParsePrefix([]byte{0xfd, 1, 2}); err == nil {
		t.Fatal("short prefix accepted")
	}
	if _, err := ParsePrefix([]byte{0x20, 1, 2, 3, 4, 5}); err == nil {
		t.Fatal("non-ULA prefix accepted")
	}
}

func TestLogicalAddressLayout(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})

	tests := []struct {
		name string
		got  netip.Addr
		want string
	}{
		{"instance 5/7/0", mustAddr(p.InstanceAddr(5, 7, 0)), "fdab:cdef:123:0:1:4000:70:0"},
		{"instance 5/7/3", mustAddr(p.InstanceAddr(5, 7, 3)), "fdab:cdef:123:0:1:4000:70:3"},
		{"service 5/7", mustAddr(p.ServiceAddr(5, 7)), "fdab:cdef:123:0:1:4000:71:0"},
		{"run 5/7/12", mustAddr(p.RunAddr(5, 7, 12)), "fdab:cdef:123:0:1:4000:72:c"},
	}
	for _, tt := range tests {
		want := netip.MustParseAddr(tt.want)
		if tt.got != want {
			t.Errorf("%s = %s, want %s", tt.name, tt.got, want)
		}
		nodeID, err := p.NodeID(tt.got)
		if err != nil {
			t.Fatal(err)
		}
		if nodeID != LogicalNodeID {
			t.Errorf("%s node id = %d, want logical node %d", tt.name, nodeID, LogicalNodeID)
		}
	}
}

func TestNodeTranslationOnlyChangesNode(t *testing.T) {
	p := GeneratePrefix()
	logical := mustAddr(p.InstanceAddr(17, 42, 9))
	locator := mustAddr(p.WithNode(logical, 12345))

	nodeID, err := p.NodeID(locator)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != 12345 {
		t.Fatalf("locator node id = %d, want 12345", nodeID)
	}
	restored := mustAddr(p.WithNode(locator, LogicalNodeID))
	if restored != logical {
		t.Fatalf("restored logical address = %s, want %s", restored, logical)
	}
}

func TestAddressPrefixes(t *testing.T) {
	p := GeneratePrefix()
	instance0 := mustAddr(p.InstanceAddr(17, 42, 0))
	instance1 := mustAddr(p.InstanceAddr(17, 42, 1))
	service := mustAddr(p.ServiceAddr(17, 42))
	run := mustAddr(p.RunAddr(17, 42, 9))
	otherDeployment := mustAddr(p.InstanceAddr(17, 43, 0))
	otherSpace := mustAddr(p.InstanceAddr(18, 42, 0))
	remote := mustAddr(p.WithNode(instance0, 7))

	nodeBlock, err := p.NodeCIDR(7)
	if err != nil {
		t.Fatal(err)
	}
	if nodeBlock.Bits() != NodePrefixBits || !nodeBlock.Contains(remote) || nodeBlock.Contains(instance0) {
		t.Fatalf("node block %s does not isolate node locator addresses", nodeBlock)
	}

	spaceBlock, err := p.SpaceCIDR(17)
	if err != nil {
		t.Fatal(err)
	}
	if spaceBlock.Bits() != SpacePrefixBits || !spaceBlock.Contains(instance0) || !spaceBlock.Contains(otherDeployment) || spaceBlock.Contains(otherSpace) || spaceBlock.Contains(remote) {
		t.Fatalf("space block %s does not isolate one logical space", spaceBlock)
	}

	deploymentBlock, err := p.DeploymentCIDR(17, 42)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentBlock.Bits() != DeploymentPrefixBits ||
		!deploymentBlock.Contains(instance0) || !deploymentBlock.Contains(instance1) ||
		!deploymentBlock.Contains(service) || !deploymentBlock.Contains(run) ||
		deploymentBlock.Contains(otherDeployment) {
		t.Fatalf("deployment block %s does not cover every deployment address kind", deploymentBlock)
	}
}

func TestAddressLimits(t *testing.T) {
	p := GeneratePrefix()
	if _, err := p.InstanceAddr(MaxSpaceID, MaxDeploymentID, MaxField); err != nil {
		t.Fatalf("maximum logical address rejected: %v", err)
	}
	if _, err := p.InstanceAddr(MaxSpaceID+1, 1, 0); err == nil {
		t.Fatal("oversized space id accepted")
	}
	if _, err := p.InstanceAddr(1, MaxDeploymentID+1, 0); err == nil {
		t.Fatal("oversized deployment id accepted")
	}
	if _, err := p.InstanceAddr(1, 1, MaxField+1); err == nil {
		t.Fatal("oversized instance ordinal accepted")
	}
	if _, err := p.NodeCIDR(MaxNodeID + 1); err == nil {
		t.Fatal("oversized node id accepted")
	}
}

func TestRunAddressFieldWraps(t *testing.T) {
	p := GeneratePrefix()
	first := mustAddr(p.RunAddr(1, 1, 1))
	wrapped := mustAddr(p.RunAddr(1, 1, (1<<FieldBits)+1))
	if first != wrapped {
		t.Fatalf("wrapped run address = %s, want %s", wrapped, first)
	}
	if _, err := p.RunAddr(1, 1, 0); err == nil {
		t.Fatal("zero run number accepted")
	}
}

func TestWithNodeRejectsAddressOutsideCluster(t *testing.T) {
	p := GeneratePrefix()
	if _, err := p.WithNode(netip.MustParseAddr("fd00::1"), 1); err == nil {
		t.Fatal("address outside cluster accepted")
	}
}
