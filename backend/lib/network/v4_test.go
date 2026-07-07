package network

import (
	"net/netip"
	"testing"
)

func TestV4PairDisjointSlots(t *testing.T) {
	h0, c0, err := V4Pair(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	h1, c1, err := V4Pair(7, 1)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range []string{h0.String(), c0.String(), h1.String(), c1.String()} {
		if seen[a] {
			t.Fatalf("duplicate address %s", a)
		}
		seen[a] = true
		if !V4CIDR.Contains(netip.MustParseAddr(a)) {
			t.Fatalf("%s outside %s", a, V4CIDR)
		}
	}
	if _, _, err := V4Pair(7, 2); err == nil {
		t.Fatal("slot 2 accepted")
	}
}

func TestV4PairDisjointDeployments(t *testing.T) {
	_, c1, _ := V4Pair(1, 0)
	_, c2, _ := V4Pair(2, 0)
	if c1 == c2 {
		t.Fatal("deployments 1 and 2 share a v4 address")
	}
}