package primary

import (
	"fmt"
	"net"
	"strings"
)

func resolvePrimaryUnderlayAddress(clusterListen string) (string, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(clusterListen))
	if err != nil {
		return "", fmt.Errorf("resolving primary underlay address from cluster listen address: %w", err)
	}
	host = strings.Trim(host, "[]")
	if host != "" {
		ipAddr, resolveErr := net.ResolveIPAddr("ip", host)
		if resolveErr == nil && ipAddr.IP != nil && !ipAddr.IP.IsUnspecified() {
			return ipAddr.IP.String(), nil
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("listing addresses for primary underlay: %w", err)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.IsGlobalUnicast() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback address found for primary underlay")
}
