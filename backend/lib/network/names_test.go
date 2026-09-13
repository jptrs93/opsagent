package network

import "testing"

func TestIsHostVethName(t *testing.T) {
	if got := hostVethName(12, 1); got != "od12s1" {
		t.Fatalf("hostVethName = %q", got)
	}
	for _, name := range []string{"od1s0", "od12s1", "od340s0"} {
		if !IsHostVethName(name) {
			t.Errorf("IsHostVethName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "od", "ods0", "od1s", "od1s0x", "odwg0", "eth0", "od-1s0", "odabs0"} {
		if IsHostVethName(name) {
			t.Errorf("IsHostVethName(%q) = true", name)
		}
	}
}

func TestValidateIssuedName(t *testing.T) {
	prefix := mustPrefix(t, []byte{0xfd, 0x42, 0x00, 0x00, 0x00, 0x01})
	own := mustAddr(prefix.InboundAddr(1, 7, 0)).String()
	sibling := mustAddr(prefix.InboundAddr(1, 8, 0)).String()
	foreign := mustAddr(prefix.InboundAddr(2, 7, 0)).String()
	for _, name := range []string{"api.space-1.internal", "API.Space-1.Internal.", "deep.api.space-1.internal", "example.com", "internal.example.com", "api.internals", own, "2001:db8::1", "203.0.113.5"} {
		if err := ValidateIssuedName(name, 1, 7, prefix, true); err != nil {
			t.Errorf("ValidateIssuedName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"api.space-2.internal", "api.space-12.internal", "space-1.internal", "internal", sibling, foreign} {
		if err := ValidateIssuedName(name, 1, 7, prefix, true); err == nil {
			t.Errorf("ValidateIssuedName(%q) = nil, want error", name)
		}
	}
	if err := ValidateIssuedName(foreign, 1, 7, Prefix{}, false); err != nil {
		t.Errorf("ValidateIssuedName(%q) without a prefix = %v, want nil", foreign, err)
	}
}
