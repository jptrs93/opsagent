package network

import (
	"golang.org/x/sys/unix"
	"net/netip"
	"testing"
)

func TestStableHostAddress(t *testing.T) {
	for _, tc := range []struct {
		ip    string
		flags int
		want  bool
	}{
		{"2001:db8::1", 0, true},
		{"2001:db8::1", unix.IFA_F_TEMPORARY, false},
		{"2001:db8::1", unix.IFA_F_DEPRECATED, false},
		{"2001:db8::1", unix.IFA_F_TENTATIVE, false},
		{"192.0.2.1", unix.IFA_F_SECONDARY, true},
	} {
		if got := stableHostAddress(netip.MustParseAddr(tc.ip), tc.flags); got != tc.want {
			t.Errorf("stableHostAddress(%s, %d) = %v, want %v", tc.ip, tc.flags, got, tc.want)
		}
	}
}
