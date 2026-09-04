//go:build linux

package netaudit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/nftables"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
)

const supported = true

const nftTableName = "opendeploy"

// auditOnce compares desired and kernel state, rechecking once before
// reporting divergence so mid-flight container transitions do not alarm.
func auditOnce(ctx context.Context, m *network.Manager) {
	first, err := collectAndCompare(m)
	if err != nil {
		slog.WarnContext(ctx, "netaudit: reading kernel network state failed", "err", err)
		return
	}
	if first.NotInstalled {
		slog.DebugContext(ctx, "netaudit: no managed kernel state installed yet")
		return
	}
	if first.InSync() {
		logInSync(ctx, m)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(recheckDelay):
	}
	second, err := collectAndCompare(m)
	if err != nil {
		slog.WarnContext(ctx, "netaudit: reading kernel network state failed on recheck", "err", err)
		return
	}
	if second.NotInstalled || second.InSync() {
		slog.DebugContext(ctx, fmt.Sprintf(
			"netaudit: transient divergence resolved on recheck missing_nft=%v unexpected_nft=%v missing_filter=%v unexpected_filter=%v missing_elements=%v unexpected_elements=%v missing_routes=%v unexpected_routes=%v",
			first.MissingNft, first.UnexpectedNft, first.MissingFilter, first.UnexpectedFilter, first.MissingElements, first.UnexpectedElements, first.MissingRoutes, first.UnexpectedRoutes))
		return
	}
	slog.WarnContext(ctx, fmt.Sprintf(
		"netaudit: kernel network state diverged from desired missing_nft=%v unexpected_nft=%v unrecognized_nft=%v missing_filter=%v unexpected_filter=%v missing_elements=%v unexpected_elements=%v missing_masquerade=%v missing_routes=%v wrong_link_routes=%v unexpected_routes=%v missing_fallback_route=%v missing_wg_device=%v unexpected_wg_device=%v wg_device_drift=%v missing_wg_peers=%v unexpected_wg_peers=%v wg_peer_drift=%v",
		second.MissingNft, second.UnexpectedNft, second.UnrecognizedNft, second.MissingFilter, second.UnexpectedFilter, second.MissingElements, second.UnexpectedElements, second.MissingMasquerade,
		second.MissingRoutes, second.WrongLinkRoutes, second.UnexpectedRoutes, second.MissingFallbackRoute,
		second.MissingWGDevice, second.UnexpectedWGDevice, second.WGDeviceDrift, second.MissingWGPeers, second.UnexpectedWGPeers, second.WGPeerDrift))
}

func collectAndCompare(m *network.Manager) (Diff, error) {
	desired := m.AuditSnapshot()
	kernel, err := collectKernel(desired)
	if err != nil {
		return Diff{}, err
	}
	return Compare(desired, kernel), nil
}

func logInSync(ctx context.Context, m *network.Manager) {
	desired := m.AuditSnapshot()
	wgPeers := 0
	if desired.WG != nil {
		wgPeers = len(desired.WG.Peers)
	}
	slog.InfoContext(ctx, fmt.Sprintf("netaudit: kernel network state in sync host_port_rules=%d workload_routes=%d filter_attachments=%d wg_peers=%d",
		len(desired.HostPortRules), len(desired.WorkloadRoutes), len(desired.FilterNets), wgPeers))
}

func collectKernel(desired network.AuditState) (KernelState, error) {
	kernel := KernelState{
		DNAT:     map[string]int{},
		Filter:   map[string]int{},
		Elements: map[string]int{},
		Routes:   map[netip.Addr]int{},
	}
	if err := collectNft(&kernel); err != nil {
		return KernelState{}, err
	}
	if err := collectRoutes(&kernel, desired); err != nil {
		return KernelState{}, err
	}
	if err := collectWG(&kernel); err != nil {
		return KernelState{}, err
	}
	return kernel, nil
}

// collectWG reads the managed WireGuard device's live configuration. A
// missing device is represented as nil rather than an error; whether that is
// a divergence depends on the desired state.
func collectWG(kernel *KernelState) error {
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("opening WireGuard control socket: %w", err)
	}
	defer client.Close()
	device, err := client.Device(network.WGLinkName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading WireGuard device: %w", err)
	}
	state := &KernelWGState{
		PublicKey:  device.PublicKey.String(),
		ListenPort: uint16(device.ListenPort),
		Peers:      make(map[string]KernelWGPeer, len(device.Peers)),
	}
	for _, peer := range device.Peers {
		endpoint := ""
		if peer.Endpoint != nil {
			endpoint = peer.Endpoint.String()
		}
		prefixes := make([]string, 0, len(peer.AllowedIPs))
		for _, allowed := range peer.AllowedIPs {
			ones, _ := allowed.Mask.Size()
			addr, ok := netip.AddrFromSlice(allowed.IP)
			if !ok {
				continue
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr.Unmap(), ones).String())
		}
		sort.Strings(prefixes)
		state.Peers[peer.PublicKey.String()] = KernelWGPeer{Endpoint: endpoint, AllowedIPs: strings.Join(prefixes, ",")}
	}
	kernel.WG = state
	return nil
}

func collectNft(kernel *KernelState) error {
	c, err := network.NewNftConn()
	if err != nil {
		return err
	}
	for _, family := range []struct {
		family nftables.TableFamily
		name   string
		found  *bool
	}{
		{nftables.TableFamilyIPv4, "ip", &kernel.TableV4},
		{nftables.TableFamilyIPv6, "ip6", &kernel.TableV6},
	} {
		tables, err := c.ListTablesOfFamily(family.family)
		if err != nil {
			return err
		}
		var table *nftables.Table
		for _, t := range tables {
			if t.Name == nftTableName {
				table = t
				break
			}
		}
		if table == nil {
			continue
		}
		*family.found = true
		if err := collectSetElements(c, kernel, table, family.name); err != nil {
			return err
		}
		chains, err := c.ListChainsOfTableFamily(family.family)
		if err != nil {
			return err
		}
		for _, chain := range chains {
			if chain.Table == nil || chain.Table.Name != nftTableName {
				continue
			}
			rules, err := c.GetRules(table, chain)
			if err != nil {
				return err
			}
			for _, rule := range rules {
				if isFilterChain(chain.Name) {
					parsed, ok := parseFilterRuleExprs(rule.Exprs)
					if !ok {
						kernel.Unrecognized = append(kernel.Unrecognized, family.name+" "+chain.Name)
						continue
					}
					kernel.Filter[parsed.Key(family.name, chain.Name)]++
					continue
				}
				parsed, ok := parseRuleExprs(rule.Exprs)
				switch {
				case !ok:
					kernel.Unrecognized = append(kernel.Unrecognized, family.name+" "+chain.Name)
				case parsed.Masquerade:
					kernel.Masquerade = true
				case parsed.DNAT:
					kernel.DNAT[dnatKey(family.name, chain.Name, parsed.Proto, parsed.HostPort, parsed.Source, parsed.Dest, parsed.Target, parsed.TargetPort)]++
				}
			}
		}
	}
	return nil
}

func collectSetElements(c *nftables.Conn, kernel *KernelState, table *nftables.Table, familyName string) error {
	sets, err := c.GetSets(table)
	if err != nil {
		return err
	}
	for _, set := range sets {
		elems, err := c.GetSetElements(set)
		if err != nil {
			return err
		}
		for _, elem := range elems {
			key, ok := parseSetElement(familyName, set.Name, elem)
			if !ok {
				kernel.Unrecognized = append(kernel.Unrecognized, familyName+" set "+set.Name)
				continue
			}
			kernel.Elements[key]++
		}
	}
	return nil
}

func collectRoutes(kernel *KernelState, desired network.AuditState) error {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{
		Protocol: network.AuditRouteProtocol(),
		Table:    unix.RT_TABLE_MAIN,
	}, netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE)
	if err != nil {
		return err
	}
	var fallbackDst netip.Prefix
	if desired.HasPrefix {
		fallbackDst = desired.Prefix.CIDR()
	}
	for _, route := range routes {
		if route.Dst == nil {
			continue
		}
		ones, bits := route.Dst.Mask.Size()
		addr, ok := netip.AddrFromSlice(route.Dst.IP)
		if !ok || bits != 128 {
			continue
		}
		if route.Type == unix.RTN_UNREACHABLE {
			if fallbackDst.IsValid() && netip.PrefixFrom(addr, ones).Masked() == fallbackDst {
				kernel.FallbackRoute = true
			}
			continue
		}
		if ones == 128 {
			kernel.Routes[addr] = route.LinkIndex
		}
		// Remote workload prefixes (/100, /120) are reconciled from the network
		// map, not tracked by the manager; they are deliberately not audited.
	}
	return nil
}
