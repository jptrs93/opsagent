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
