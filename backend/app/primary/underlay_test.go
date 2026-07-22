package primary

import "testing"

func TestResolvePrimaryUnderlayAddressUsesConcreteListenHost(t *testing.T) {
	got, err := resolvePrimaryUnderlayAddress("[2001:db8::1]:9443")
	if err != nil {
		t.Fatalf("resolvePrimaryUnderlayAddress: %v", err)
	}
	if got != "2001:db8::1" {
		t.Fatalf("underlay address = %q, want 2001:db8::1", got)
	}
}
