package secondary

import (
	"net"
	"testing"
)

func TestResolveDefaultUnderlayAddressUsesRouteSource(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer listener.Close()

	got, err := resolveDefaultUnderlayAddress(listener.LocalAddr().String())
	if err != nil {
		t.Fatalf("resolveDefaultUnderlayAddress: %v", err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("underlay address = %q, want 127.0.0.1", got)
	}
}
