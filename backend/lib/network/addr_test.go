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
		{"outbound space5 dep7 ord3 placement12 run3", mustAddr(p.OutboundAddr(5, 7, 3, 12, 3)), "fdab:cdef:123:5:0:700:3000:c03"},
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
		{mustAddr(p.OutboundAddr(17, 42, 3, 99, 7)), LogicalAddr{SpaceID: 17, DeploymentID: 42, Ordinal: 3, PlacementSlot: 99, RunSlot: 7}},
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
	if _, err := p.OutboundAddr(MaxSpaceID, MaxDeploymentID, MaxOrdinal, MaxPlacementSlot, MaxRunSlot); err != nil {
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
	wrappedPlacement := mustAddr(p.OutboundAddr(1, 1, 0, MaxPlacementSlot+1, 1))
	wrappedRun := mustAddr(p.OutboundAddr(1, 1, 0, 1, MaxRunSlot+1))
	if first != wrappedPlacement || first != wrappedRun {
		t.Fatalf("wrapped addresses differ: first=%s placement=%s run=%s", first, wrappedPlacement, wrappedRun)
	}
	decoded, err := p.ParseAddr(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PlacementSlot != 1 || decoded.RunSlot != 1 {
		t.Fatalf("wrapped slots = %d/%d, want 1/1", decoded.PlacementSlot, decoded.RunSlot)
	}
	if _, err := p.OutboundAddr(1, 1, 0, 0, 1); err == nil {
		t.Fatal("zero scheduled instance id accepted")
	}
	if _, err := p.OutboundAddr(1, 1, 0, 1, 0); err == nil {
		t.Fatal("zero run number accepted")
	}
}

func TestOutboundAddressSeparatesOrdinalsPlacementsAndRuns(t *testing.T) {
	p := GeneratePrefix()
	base := mustAddr(p.OutboundAddr(1, 2, 0, 1, 1))
	tests := []netip.Addr{
		mustAddr(p.OutboundAddr(1, 2, 1, 1, 1)),
		mustAddr(p.OutboundAddr(1, 2, 0, 2, 1)),
		mustAddr(p.OutboundAddr(1, 2, 0, 1, 2)),
		mustAddr(p.OutboundAddr(1, 2, 0, MaxPlacementSlot, 1)),
	}
	for _, other := range tests {
		if other == base {
			t.Fatalf("outbound address %s collided with %s", other, base)
		}
	}
	wrapped := mustAddr(p.OutboundAddr(1, 2, 0, MaxPlacementSlot+1, 1))
	if wrapped != base {
		t.Fatalf("wrapped placement address = %s, want %s", wrapped, base)
	}
}

// TestRoutedPrefixesNestCorrectly pins the property cross-node routing relies
// on: a placement's prefix sits inside its instance prefix and is more specific,
// so longest-prefix match sends a draining placement's replies to its own node
// while the instance prefix already points at the replacement.
func TestRoutedPrefixesNestCorrectly(t *testing.T) {
	p := GeneratePrefix()
	instance, err := p.InstanceCIDR(17, 42, 0)
	if err != nil {
		t.Fatal(err)
	}
	old, err := p.PlacementCIDR(17, 42, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := p.PlacementCIDR(17, 42, 0, 9)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Bits() != InstancePrefixBits || old.Bits() != PlacementPrefixBits {
		t.Fatalf("prefix lengths = /%d and /%d, want /%d and /%d",
			instance.Bits(), old.Bits(), InstancePrefixBits, PlacementPrefixBits)
	}
	if !instance.Contains(old.Addr()) || !instance.Contains(replacement.Addr()) {
		t.Fatalf("instance %s does not contain placements %s and %s", instance, old, replacement)
	}
	if old.Overlaps(replacement) {
		t.Fatalf("placements %s and %s overlap: two live placements would fight over one route", old, replacement)
	}
	// Every run of a placement must fall inside that placement's prefix, or a
	// restart would escape the route installed for it.
	for run := int32(1); run <= 4; run++ {
		addr, err := p.OutboundAddr(17, 42, 0, 8, run)
		if err != nil {
			t.Fatal(err)
		}
		if !old.Contains(addr) {
			t.Fatalf("run %d address %s falls outside placement prefix %s", run, addr, old)
		}
	}
	// The stable inbound address is covered by the instance prefix but by no
	// placement prefix, so it follows the serving placement and nothing else.
	inbound := mustAddr(p.InboundAddr(17, 42, 0))
	if !instance.Contains(inbound) || old.Contains(inbound) || replacement.Contains(inbound) {
		t.Fatalf("inbound %s is not covered by the instance prefix alone", inbound)
	}
}

func TestValidateRoutedPrefix(t *testing.T) {
	p := GeneratePrefix()
	instance, err := p.InstanceCIDR(1, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := p.PlacementCIDR(1, 2, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []netip.Prefix{instance, placement} {
		if err := p.ValidateRoutedPrefix(prefix); err != nil {
			t.Fatalf("ValidateRoutedPrefix(%s): %v", prefix, err)
		}
	}

	inbound := mustAddr(p.InboundAddr(1, 2, 0))
	rejected := []struct {
		name   string
		prefix netip.Prefix
	}{
		{"host route", netip.PrefixFrom(inbound, 128)},
		{"whole deployment", netip.PrefixFrom(inbound, DeploymentPrefixBits)},
		{"whole space", netip.PrefixFrom(inbound, SpacePrefixBits)},
		{"zero deployment", netip.PrefixFrom(mustAddr(p.addr(1, 0, 0, 0, 0)), InstancePrefixBits)},
		{"zero placement slot", netip.PrefixFrom(inbound, PlacementPrefixBits)},
		{"foreign prefix", netip.PrefixFrom(mustAddr(GeneratePrefix().InboundAddr(1, 2, 0)), InstancePrefixBits)},
		{"unmasked", netip.PrefixFrom(mustAddr(p.OutboundAddr(1, 2, 0, 3, 4)), PlacementPrefixBits)},
	}
	for _, tt := range rejected {
		if err := p.ValidateRoutedPrefix(tt.prefix); err == nil {
			t.Errorf("%s: %s accepted as a routed prefix", tt.name, tt.prefix)
		}
	}
}

func TestPlacementSlotWrapsWithoutUsingZero(t *testing.T) {
	p := GeneratePrefix()
	first, err := p.PlacementCIDR(1, 2, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := p.PlacementCIDR(1, 2, 0, MaxPlacementSlot+1)
	if err != nil {
		t.Fatal(err)
	}
	if first != wrapped {
		t.Fatalf("wrapped placement prefix = %s, want %s", wrapped, first)
	}
	if _, err := p.PlacementCIDR(1, 2, 0, 0); err == nil {
		t.Fatal("zero scheduled instance id accepted")
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
	halfZeroPlacement := mustAddr(p.addr(1, 2, 0, 0, 1))
	halfZeroRun := mustAddr(p.addr(1, 2, 0, 1, 0))
	for _, addr := range []netip.Addr{halfZeroPlacement, halfZeroRun} {
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
