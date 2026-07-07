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

func TestAddressLayout(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})

	tests := []struct {
		name string
		got  netip.Addr
		want string
	}{
		{"instance 7/0", p.InstanceAddr(7, 0), "fdab:cdef:123:0:0:7:0:0"},
		{"instance 7/3", p.InstanceAddr(7, 3), "fdab:cdef:123:0:0:7:0:3"},
		{"service 7", p.ServiceAddr(7), "fdab:cdef:123:1:0:7:0:0"},
		{"run 7/12", p.RunAddr(7, 12), "fdab:cdef:123:2:0:7:0:c"},
		{"machine 5", p.MachineAddr(5), "fdab:cdef:123:3:0:0:0:5"},
		{"large deployment id", p.InstanceAddr(0x7fffffff, 1), "fdab:cdef:123:0:7fff:ffff:0:1"},
	}
	for _, tt := range tests {
		want := netip.MustParseAddr(tt.want)
		if tt.got != want {
			t.Errorf("%s = %s, want %s", tt.name, tt.got, want)
		}
	}
}

func TestAddressesAreWithinCIDR(t *testing.T) {
	p := GeneratePrefix()
	cidr := p.CIDR()
	for _, a := range []netip.Addr{
		p.InstanceAddr(1, 0), p.ServiceAddr(2), p.RunAddr(3, 9), p.MachineAddr(4),
	} {
		if !cidr.Contains(a) {
			t.Errorf("%s not contained in %s", a, cidr)
		}
	}
}

func TestDeploymentCIDRMatchesAllOrdinals(t *testing.T) {
	p := GeneratePrefix()
	block := p.DeploymentCIDR(KindInstance, 42)
	if !block.Contains(p.InstanceAddr(42, 0)) || !block.Contains(p.InstanceAddr(42, 1<<20)) {
		t.Fatal("deployment block does not cover its ordinals")
	}
	if block.Contains(p.InstanceAddr(43, 0)) {
		t.Fatal("deployment block covers another deployment")
	}
	if block.Contains(p.RunAddr(42, 1)) {
		t.Fatal("kind-0 block covers kind-2 addresses")
	}
}
