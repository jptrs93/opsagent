package secondary

import (
	"fmt"
	"net"
)

// resolveDefaultUnderlayAddress returns the local address selected by the
// kernel for traffic to the primary cluster endpoint. UDP connect performs the
// route lookup without sending a packet.
func resolveDefaultUnderlayAddress(primaryClusterAddress string) (string, error) {
	conn, err := net.Dial("udp", primaryClusterAddress)
	if err != nil {
		return "", fmt.Errorf("resolving default underlay address: %w", err)
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "", fmt.Errorf("resolving default underlay address: unexpected local address %v", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}
