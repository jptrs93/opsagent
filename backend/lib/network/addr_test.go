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
		t.Fatalf("prefix starts with %#x, want 0xfd", p[0])
	}
	if _, err := ParsePrefix(p.Bytes()); err != nil {
		t.Fatal(err)
	}
	if p == GeneratePrefix() {
		t.Fatal("two generated prefixes matched")
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
		{"inbound 5/7/0", mustAddr(p.InboundAddr(5, 7, 0)), "fdab:cdef:123:5:0:700::"},
		{"inbound 5/7/3", mustAddr(p.InboundAddr(5, 7, 3)), "fdab:cdef:123:5:0:700:3000:0"},
		{"outbound 5/7/3/12/3", mustAddr(p.OutboundAddr(5, 7, 3, 12, 3)), "fdab:cdef:123:5:0:700:3000:c03"},
	}
	for _, tt := range tests {
		want := netip.MustParseAddr(tt.want)
		if tt.got != want {
			t.Errorf("%s = %s, want %s", tt.name, tt.got, want)
		}
	}
}

func TestParseLogicalAddress(t *testing.T) {
	p := GeneratePrefix()
	tests := []struct {
		addr netip.Addr
		want LogicalAddr
	}{
		{mustAddr(p.InboundAddr(17, 42, 3)), LogicalAddr{SpaceID: 17, DeploymentID: 42, Ordinal: 3}},
		{mustAddr(p.OutboundAddr(17, 42, 3, 99, 7)), LogicalAddr{SpaceID: 17, DeploymentID: 42, Ordinal: 3, VersionSlot: 99, RunSlot: 7}},
	}
	for _, tt := range tests {
		got, err := p.ParseAddr(tt.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Fatalf("ParseAddr(%s) = %+v, want %+v", tt.addr, got, tt.want)
		}
	}
}

func TestAddressPrefixes(t *testing.T) {
	p := GeneratePrefix()
	inbound0 := mustAddr(p.InboundAddr(17, 42, 0))
	inbound1 := mustAddr(p.InboundAddr(17, 42, 1))
	outbound := mustAddr(p.OutboundAddr(17, 42, 0, 9, 2))
	otherDeployment := mustAddr(p.InboundAddr(17, 43, 0))
	otherSpace := mustAddr(p.InboundAddr(18, 42, 0))

	spaceBlock, err := p.SpaceCIDR(17)
	if err != nil {
		t.Fatal(err)
	}
	if spaceBlock.Bits() != 64 || !spaceBlock.Contains(inbound0) || !spaceBlock.Contains(otherDeployment) || spaceBlock.Contains(otherSpace) {
		t.Fatalf("space block %s does not isolate one logical space", spaceBlock)
	}

	deploymentBlock, err := p.DeploymentCIDR(17, 42)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentBlock.Bits() != 88 || !deploymentBlock.Contains(inbound0) || !deploymentBlock.Contains(inbound1) || !deploymentBlock.Contains(outbound) || deploymentBlock.Contains(otherDeployment) {
		t.Fatalf("deployment block %s does not isolate one deployment", deploymentBlock)
	}
}

func TestAddressLimits(t *testing.T) {
	p := GeneratePrefix()
	if _, err := p.InboundAddr(MaxSpaceID, MaxDeploymentID, MaxOrdinal); err != nil {
		t.Fatalf("maximum inbound address rejected: %v", err)
	}
	if _, err := p.OutboundAddr(MaxSpaceID, MaxDeploymentID, MaxOrdinal, MaxVersionSlot, MaxRunSlot); err != nil {
		t.Fatalf("maximum outbound address rejected: %v", err)
	}
	if _, err := p.InboundAddr(MaxSpaceID+1, 1, 0); err == nil {
		t.Fatal("oversized space id accepted")
	}
	if _, err := p.InboundAddr(1, MaxDeploymentID+1, 0); err == nil {
		t.Fatal("oversized deployment id accepted")
	}
	if _, err := p.InboundAddr(1, 1, MaxOrdinal+1); err == nil {
		t.Fatal("oversized instance ordinal accepted")
	}
}

func TestOutboundAddressSlotsWrapWithoutUsingZero(t *testing.T) {
	p := GeneratePrefix()
	first := mustAddr(p.OutboundAddr(1, 1, 0, 1, 1))
	wrappedVersion := mustAddr(p.OutboundAddr(1, 1, 0, MaxVersionSlot+1, 1))
	wrappedRun := mustAddr(p.OutboundAddr(1, 1, 0, 1, MaxRunSlot+1))
	if first != wrappedVersion || first != wrappedRun {
		t.Fatalf("wrapped addresses differ: first=%s version=%s run=%s", first, wrappedVersion, wrappedRun)
	}
	decoded, err := p.ParseAddr(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.VersionSlot != 1 || decoded.RunSlot != 1 {
		t.Fatalf("wrapped slots = %d/%d, want 1/1", decoded.VersionSlot, decoded.RunSlot)
	}
	if _, err := p.OutboundAddr(1, 1, 0, 0, 1); err == nil {
		t.Fatal("zero config version accepted")
	}
	if _, err := p.OutboundAddr(1, 1, 0, 1, 0); err == nil {
		t.Fatal("zero run number accepted")
	}
}

func TestOutboundAddressSeparatesInstancesVersionsAndRuns(t *testing.T) {
	p := GeneratePrefix()
	base := mustAddr(p.OutboundAddr(1, 2, 0, 1, 1))
	tests := []netip.Addr{
		mustAddr(p.OutboundAddr(1, 2, 1, 1, 1)),
		mustAddr(p.OutboundAddr(1, 2, 0, 2, 1)),
		mustAddr(p.OutboundAddr(1, 2, 0, 1, 2)),
		mustAddr(p.OutboundAddr(1, 2, 0, MaxVersionSlot, 1)),
	}
	for _, other := range tests {
		if other == base {
			t.Fatalf("outbound address %s collided with %s", other, base)
		}
	}
	wrapped := mustAddr(p.OutboundAddr(1, 2, 0, MaxVersionSlot+1, 1))
	if wrapped != base {
		t.Fatalf("wrapped version address = %s, want %s", wrapped, base)
	}
}

func TestValidateRoutedAddr(t *testing.T) {
	p := GeneratePrefix()
	inbound := mustAddr(p.InboundAddr(1, 2, 0))
	outbound := mustAddr(p.OutboundAddr(1, 2, 0, 3, 4))
	for _, addr := range []netip.Addr{inbound, outbound} {
		if err := p.ValidateRoutedAddr(addr); err != nil {
			t.Fatalf("ValidateRoutedAddr(%s): %v", addr, err)
		}
	}
	halfZeroVersion := mustAddr(p.addr(1, 2, 0, 0, 1))
	halfZeroRun := mustAddr(p.addr(1, 2, 0, 1, 0))
	for _, addr := range []netip.Addr{halfZeroVersion, halfZeroRun} {
		if err := p.ValidateRoutedAddr(addr); err == nil {
			t.Fatalf("malformed address %s accepted", addr)
		}
	}
	other := mustAddr(GeneratePrefix().InboundAddr(1, 2, 0))
	if err := p.ValidateRoutedAddr(other); err == nil {
		t.Fatal("address from another prefix accepted")
	}
	zeroDeployment := mustAddr(p.addr(1, 0, 0, 0, 0))
	if err := p.ValidateRoutedAddr(zeroDeployment); err == nil {
		t.Fatal("zero deployment accepted")
	}
}
