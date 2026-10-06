package primarybootstrap

import "testing"

func TestResolvePrimaryUnderlayAddressUsesConcreteListenHost(t *testing.T) {
	got, err := ResolvePrimaryUnderlayAddress("", "[2001:db8::1]:9443")
	if err != nil {
		t.Fatalf("ResolvePrimaryUnderlayAddress: %v", err)
	}
	if got.String() != "2001:db8::1" {
		t.Fatalf("underlay address = %q, want 2001:db8::1", got)
	}
}

func TestResolvePrimaryUnderlayAddressPrefersExplicit(t *testing.T) {
	got, err := ResolvePrimaryUnderlayAddress("10.0.0.1", "[2001:db8::1]:9443")
	if err != nil {
		t.Fatalf("ResolvePrimaryUnderlayAddress: %v", err)
	}
	if got.String() != "10.0.0.1" {
		t.Fatalf("underlay address = %q, want 10.0.0.1", got)
	}
}
