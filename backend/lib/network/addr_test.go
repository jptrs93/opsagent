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

	spaceBlock, err := p.SpaceCIDR(17)
	if err != nil {
		t.Fatal(err)
	}
	if spaceBlock.Bits() != SpacePrefixBits || !spaceBlock.Contains(instance0) || !spaceBlock.Contains(otherDeployment) || spaceBlock.Contains(otherSpace) {
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

func TestValidateRoutedAddr(t *testing.T) {
	p := GeneratePrefix()
	instance := mustAddr(p.InstanceAddr(1, 2, 0))
	run := mustAddr(p.RunAddr(1, 2, 3))
	wrappedRun := mustAddr(p.RunAddr(1, 2, 1<<FieldBits))
	service := mustAddr(p.ServiceAddr(1, 2))
	for _, addr := range []netip.Addr{instance, run, wrappedRun} {
		if err := p.ValidateRoutedAddr(addr); err != nil {
			t.Fatalf("ValidateRoutedAddr(%s): %v", addr, err)
		}
	}
	if err := p.ValidateRoutedAddr(service); err == nil {
		t.Fatal("service address was accepted as a direct route")
	}
	other := mustAddr(GeneratePrefix().InstanceAddr(1, 2, 0))
	if err := p.ValidateRoutedAddr(other); err == nil {
		t.Fatal("out-of-prefix address was accepted")
	}
	zeroDeployment := mustAddr(p.InstanceAddr(1, 0, 0))
	if err := p.ValidateRoutedAddr(zeroDeployment); err == nil {
		t.Fatal("zero deployment id was accepted")
	}
}
