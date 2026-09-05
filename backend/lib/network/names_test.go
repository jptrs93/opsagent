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
