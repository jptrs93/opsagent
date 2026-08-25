//go:build linux

package netaudit

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/nftables"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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
			"netaudit: transient divergence resolved on recheck missing_nft=%v unexpected_nft=%v missing_routes=%v unexpected_routes=%v",
			first.MissingNft, first.UnexpectedNft, first.MissingRoutes, first.UnexpectedRoutes))
		return
	}
	slog.WarnContext(ctx, fmt.Sprintf(
		"netaudit: kernel network state diverged from desired missing_nft=%v unexpected_nft=%v unrecognized_nft=%v missing_masquerade=%v missing_routes=%v wrong_link_routes=%v unexpected_routes=%v missing_fallback_route=%v",
		second.MissingNft, second.UnexpectedNft, second.UnrecognizedNft, second.MissingMasquerade,
		second.MissingRoutes, second.WrongLinkRoutes, second.UnexpectedRoutes, second.MissingFallbackRoute))
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
	slog.InfoContext(ctx, fmt.Sprintf("netaudit: kernel network state in sync host_port_rules=%d workload_routes=%d",
		len(desired.HostPortRules), len(desired.WorkloadRoutes)))
}

func collectKernel(desired network.AuditState) (KernelState, error) {
	kernel := KernelState{
		DNAT:   map[string]int{},
		Routes: map[netip.Addr]int{},
	}
	if err := collectNft(&kernel); err != nil {
		return KernelState{}, err
	}
	if err := collectRoutes(&kernel, desired); err != nil {
		return KernelState{}, err
	}
	return kernel, nil
}

func collectNft(kernel *KernelState) error {
	c := &nftables.Conn{}
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
				parsed, ok := parseRuleExprs(rule.Exprs)
				switch {
				case !ok:
					kernel.Unrecognized = append(kernel.Unrecognized, family.name+" "+chain.Name)
				case parsed.Masquerade:
					kernel.Masquerade = true
				case parsed.DNAT:
					kernel.DNAT[dnatKey(family.name, chain.Name, parsed.Proto, parsed.HostPort, parsed.Source, parsed.Target, parsed.TargetPort)]++
				}
			}
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
