//go:build linux

package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const TopologySupported = true

const (
	// vethMTU matches wgMTU so the inner path MTU is uniform from workload to
	// workload across the WireGuard hop.
	vethMTU = 1420

	containerIface = "eth0"
	netnsRunDir    = "/run/netns"
)

// hostGateway is the link-local gateway address assigned to the host side of
// every veth (link-local scope, so reusing it per link is fine).
var hostGateway = netip.MustParseAddr("fe80::1")

// ContainerNetSpec describes the netns to build for one container.
type ContainerNetSpec struct {
	ContainerID  string
	DeploymentID int32
	// OutboundAddr is preferred and routed for the full life of this run.
	OutboundAddr netip.Addr
	// InboundAddr is stable, assigned with preferred_lft=0, and routed only when
	// this run is activated as the deployment's current instance.
	InboundAddr netip.Addr
	// UnprivilegedPortStart lowers net.ipv4.ip_unprivileged_port_start inside
	// the netns when SetUnprivilegedPortStart is true. Used by the netproxy
	// deployment to bind :53 without capabilities.
	UnprivilegedPortStart    int
	SetUnprivilegedPortStart bool
}

// EnsureBase applies machine-wide prerequisites: forwarding sysctls and the
// base nftables ruleset (egress masquerade). Success is latched; a failed
// attempt is returned and retried on the next call, so a transient error at
// boot cannot disable networking for the process lifetime.
func (m *Manager) EnsureBase() error {
	if err := m.ensureBase(); err != nil {
		return err
	}
	prefix, ok := m.PrefixValue()
	if ok {
		if err := reconcileClusterFallbackRoute(prefix); err != nil {
			return fmt.Errorf("installing cluster fallback route: %w", err)
		}
	}
	return nil
}

func (m *Manager) ensureBase() error {
	m.baseMu.Lock()
	defer m.baseMu.Unlock()
	if m.baseDone {
		return nil
	}
	// Enabling IPv6 forwarding host-wide flips accept_ra semantics on all
	// interfaces (RA-configured hosts need accept_ra=2); machines relying on
	// SLAAC uplinks should pre-set that. Static-config servers are unaffected.
	for _, s := range [][2]string{
		{"/proc/sys/net/ipv6/conf/all/forwarding", "1"},
		{"/proc/sys/net/ipv4/ip_forward", "1"},
	} {
		if err := os.WriteFile(s[0], []byte(s[1]), 0); err != nil {
			return fmt.Errorf("setting %s: %w", s[0], err)
		}
	}
	m.mu.Lock()
	err := m.reconcileNft()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.baseDone = true
	return nil
}

// SetupContainerNet creates (or recreates) the network namespace, veth pair,
// addresses, and routes for one container. Idempotent: any existing state for
// the same container id is torn down first.
func (m *Manager) SetupContainerNet(spec ContainerNetSpec) (*ContainerNet, error) {
	if err := m.EnsureBase(); err != nil {
		return nil, err
	}
	if !spec.OutboundAddr.Is6() || !spec.InboundAddr.Is6() {
		return nil, fmt.Errorf("container addresses must be IPv6, got outbound=%v inbound=%v", spec.OutboundAddr, spec.InboundAddr)
	}
	prefix, ok := m.PrefixValue()
	if !ok {
		return nil, fmt.Errorf("cluster prefix is not known")
	}
	if err := validateContainerAddressIdentity(prefix, spec.DeploymentID, spec.InboundAddr, spec.OutboundAddr); err != nil {
		return nil, err
	}
	m.containerMu.Lock()
	defer m.containerMu.Unlock()

	// Recreate from scratch so a crash respawn never inherits half-built state.
	m.teardownContainerNet(spec.ContainerID, spec.DeploymentID, "", 0)
	if m.outboundAddressInUse(spec.DeploymentID, spec.ContainerID, spec.OutboundAddr) {
		return nil, fmt.Errorf("outbound address %s is already used by a live run", spec.OutboundAddr)
	}

	slot, err := freeV4Slot(spec.DeploymentID)
	if err != nil {
		return nil, err
	}
	hostV4, contV4, err := V4Pair(spec.DeploymentID, slot)
	if err != nil {
		return nil, err
	}
	hostVeth := hostVethName(spec.DeploymentID, slot)

	nsHandle, err := createNamedNetns(spec.ContainerID, spec.UnprivilegedPortStart, spec.SetUnprivilegedPortStart)
	if err != nil {
		return nil, fmt.Errorf("creating netns %s: %w", spec.ContainerID, err)
	}
	defer nsHandle.Close()

	cleanup := func(setupErr error) (*ContainerNet, error) {
		m.teardownContainerNet(spec.ContainerID, spec.DeploymentID, hostVeth, 0)
		return nil, setupErr
	}

	peerName := fmt.Sprintf("odp%ds%d", spec.DeploymentID, slot)
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostVeth, MTU: vethMTU},
		PeerName:  peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		deleteNamedNetns(m.ctx, spec.ContainerID)
		return nil, fmt.Errorf("creating veth %s: %w", hostVeth, err)
	}
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		return cleanup(fmt.Errorf("looking up veth peer: %w", err))
	}
	if err := netlink.LinkSetNsFd(peer, int(nsHandle)); err != nil {
		return cleanup(fmt.Errorf("moving veth peer into netns: %w", err))
	}

	nsNetlink, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return cleanup(fmt.Errorf("opening netlink handle in netns: %w", err))
	}
	defer nsNetlink.Close()
	if err := configureContainerSide(nsNetlink, peerName, spec.OutboundAddr, spec.InboundAddr, contV4, hostV4); err != nil {
		return cleanup(err)
	}

	hostLink, err := netlink.LinkByName(hostVeth)
	if err != nil {
		return cleanup(fmt.Errorf("looking up host veth: %w", err))
	}
	if err := netlink.AddrAdd(hostLink, &netlink.Addr{
		IPNet: netipPrefixToIPNet(netip.PrefixFrom(hostGateway, 64)),
		Flags: unix.IFA_F_NODAD,
	}); err != nil {
		return cleanup(fmt.Errorf("assigning host gateway address: %w", err))
	}
	if err := netlink.AddrAdd(hostLink, &netlink.Addr{IPNet: netipPrefixToIPNet(netip.PrefixFrom(hostV4, 30))}); err != nil {
		return cleanup(fmt.Errorf("assigning host v4 address: %w", err))
	}
	if err := netlink.LinkSetUp(hostLink); err != nil {
		return cleanup(fmt.Errorf("bringing host veth up: %w", err))
	}
	if err := reconcileLocalWorkloadRoute(prefix, spec.OutboundAddr, hostLink.Attrs().Index); err != nil {
		return cleanup(fmt.Errorf("adding host route for %v: %w", spec.OutboundAddr, err))
	}

	slog.InfoContext(m.ctx, fmt.Sprintf("container netns configured container=%s outbound=%s inbound=%s v4=%s veth=%s",
		spec.ContainerID, spec.OutboundAddr, spec.InboundAddr, contV4, hostVeth))
	cn := &ContainerNet{
		ContainerID:   spec.ContainerID,
		DeploymentID:  spec.DeploymentID,
		NetnsPath:     filepath.Join(netnsRunDir, spec.ContainerID),
		HostVeth:      hostVeth,
		HostVethIndex: hostLink.Attrs().Index,
		InboundAddr:   spec.InboundAddr,
		OutboundAddr:  spec.OutboundAddr,
		V4:            contV4,
		HostV4:        hostV4,
		Slot:          slot,
	}
	m.containerNets[cn.ContainerID] = cn
	if err := m.syncFilterNets(); err != nil {
		return cleanup(fmt.Errorf("applying container filter rules: %w", err))
	}
	return cn, nil
}

// RecoverContainerNet reconstructs manager ownership for a running container
// whose network namespace survived an agent restart.
func (m *Manager) RecoverContainerNet(containerID string, deploymentID int32, inboundAddr, outboundAddr netip.Addr) (*ContainerNet, error) {
	if err := m.EnsureBase(); err != nil {
		return nil, err
	}
	prefix, ok := m.PrefixValue()
	if !ok {
		return nil, fmt.Errorf("cluster prefix is not known")
	}
	if err := validateContainerAddressIdentity(prefix, deploymentID, inboundAddr, outboundAddr); err != nil {
		return nil, err
	}
	m.containerMu.Lock()
	defer m.containerMu.Unlock()
	if m.outboundAddressInUse(deploymentID, containerID, outboundAddr) {
		return nil, fmt.Errorf("outbound address %s is already used by a live run", outboundAddr)
	}
	netnsPath := filepath.Join(netnsRunDir, containerID)
	hostLink, slot, err := findContainerVeth(containerID, deploymentID)
	if err != nil {
		return nil, err
	}
	if err := validateContainerV6Addresses(containerID, inboundAddr, outboundAddr); err != nil {
		return nil, err
	}
	hostV4, containerV4, err := V4Pair(deploymentID, slot)
	if err != nil {
		return nil, err
	}
	if err := reconcileLocalWorkloadRoute(prefix, outboundAddr, hostLink.Attrs().Index); err != nil {
		return nil, fmt.Errorf("restoring outbound route for %v: %w", outboundAddr, err)
	}
	if err := reconcileLocalWorkloadRoute(prefix, inboundAddr, hostLink.Attrs().Index); err != nil {
		return nil, fmt.Errorf("restoring inbound route for %v: %w", inboundAddr, err)
	}
	cn := &ContainerNet{
		ContainerID:   containerID,
		DeploymentID:  deploymentID,
		NetnsPath:     netnsPath,
		HostVeth:      hostLink.Attrs().Name,
		HostVethIndex: hostLink.Attrs().Index,
		InboundAddr:   inboundAddr,
		OutboundAddr:  outboundAddr,
		V4:            containerV4,
		HostV4:        hostV4,
		Slot:          slot,
	}
	m.containerNets[cn.ContainerID] = cn
	m.cleanupContainerNets(deploymentID, []*ContainerNet{cn})
	if err := m.syncFilterNets(); err != nil {
		delete(m.containerNets, containerID)
		return nil, fmt.Errorf("applying container filter rules: %w", err)
	}
	return cn, nil
}

// Activate routes the stable inbound address to one run. Every run already has
// that address assigned before workload start, so activation and rollover do
// not mutate the network namespace.
func (m *Manager) Activate(candidate *ContainerNet) error {
	if candidate == nil {
		return fmt.Errorf("activate: container network is nil")
	}
	if !candidate.InboundAddr.Is6() {
		return fmt.Errorf("activate: inbound address must be IPv6, got %v", candidate.InboundAddr)
	}
	m.containerMu.Lock()
	defer m.containerMu.Unlock()

	hostLink, err := netlink.LinkByName(candidate.HostVeth)
	if err != nil {
		return fmt.Errorf("activate: container host veth: %w", err)
	}
	if candidate.HostVethIndex <= 0 || hostLink.Type() != "veth" || hostLink.Attrs().Index != candidate.HostVethIndex {
		return fmt.Errorf("activate: container host veth %s was replaced", candidate.HostVeth)
	}
	prefix, _ := m.PrefixValue()
	if err := reconcileLocalWorkloadRoute(prefix, candidate.InboundAddr, hostLink.Attrs().Index); err != nil {
		return fmt.Errorf("activate: routing inbound address: %w", err)
	}

	slog.InfoContext(m.ctx, fmt.Sprintf("activated container network container=%s inbound=%s outbound=%s",
		candidate.ContainerID, candidate.InboundAddr, candidate.OutboundAddr))
	return nil
}

// TeardownContainerNet removes the exact network tracked for a container.
func (m *Manager) TeardownContainerNet(cn *ContainerNet) {
	if cn == nil {
		return
	}
	m.containerMu.Lock()
	defer m.containerMu.Unlock()
	hostVeth := cn.HostVeth
	if cn.HostVethIndex == 0 {
		hostVeth = ""
	}
	m.teardownContainerNet(cn.ContainerID, 0, hostVeth, cn.HostVethIndex)
}

// TeardownContainerNetState removes one untracked container's network by
// proving its host peer through the surviving named namespace.
func (m *Manager) TeardownContainerNetState(containerID string, deploymentID int32) {
	m.containerMu.Lock()
	defer m.containerMu.Unlock()
	m.teardownContainerNet(containerID, deploymentID, "", 0)
}

func (m *Manager) teardownContainerNet(containerID string, deploymentID int32, hostVeth string, hostVethIndex int) {
	if active := m.containerNets[containerID]; active != nil && hostVethIndex > 0 && active.HostVethIndex != hostVethIndex {
		return
	}
	if hostVeth == "" {
		if link, _, err := findContainerVeth(containerID, deploymentID); err == nil {
			hostVeth = link.Attrs().Name
			hostVethIndex = link.Attrs().Index
		}
	}
	if hostVeth != "" {
		link, err := netlink.LinkByName(hostVeth)
		if err == nil && (hostVethIndex == 0 || link.Attrs().Index == hostVethIndex) {
			if err := netlink.LinkDel(link); err != nil {
				slog.WarnContext(m.ctx, fmt.Sprintf("deleting container veth %s of container %s", hostVeth, containerID), "err", err)
			}
		}
	}
	_, tracked := m.containerNets[containerID]
	delete(m.containerNets, containerID)
	deleteNamedNetns(m.ctx, containerID)
	if tracked {
		if err := m.syncFilterNets(); err != nil {
			slog.WarnContext(m.ctx, fmt.Sprintf("rebuilding nftables ruleset after teardown of container %s", containerID), "err", err)
		}
	}
}

// CleanupContainerNets removes stale netns and veth state for a deployment,
// keeping only the explicitly supplied networks. A failed container can leave
// a host veth after its named netns has disappeared, so inspect both sources of
// kernel state.
func (m *Manager) CleanupContainerNets(deploymentID int32, keep []*ContainerNet) {
	m.containerMu.Lock()
	defer m.containerMu.Unlock()
	m.cleanupContainerNets(deploymentID, keep)
}

func (m *Manager) cleanupContainerNets(deploymentID int32, keep []*ContainerNet) {
	retained := m.retainedContainerNetsForDeployment(deploymentID, keep)
	prefix := fmt.Sprintf("opendeploy-%d-", deploymentID)
	entries, err := os.ReadDir(netnsRunDir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, prefix) || retained.keepsContainer(name) {
				continue
			}
			slog.InfoContext(m.ctx, fmt.Sprintf("cleaning up stale container netns %s", name))
			m.teardownContainerNet(name, deploymentID, "", 0)
		}
	}
	for slot := range v4SlotsPerDeployment {
		hostVeth := hostVethName(deploymentID, slot)
		link, linkErr := netlink.LinkByName(hostVeth)
		if linkErr != nil || retained.keepsHostVeth(hostVeth) {
			continue
		}
		slog.InfoContext(m.ctx, fmt.Sprintf("cleaning up stale container veth %s", link.Attrs().Name))
		if err := netlink.LinkDel(link); err != nil {
			slog.WarnContext(m.ctx, fmt.Sprintf("deleting stale container veth %s", link.Attrs().Name), "err", err)
		}
	}
}

func deleteNamedNetns(ctx context.Context, containerID string) {
	if err := netns.DeleteNamed(containerID); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.WarnContext(ctx, fmt.Sprintf("deleting netns of container %s", containerID), "err", err)
	}
}

func hostVethName(deploymentID int32, slot int) string {
	return "od" + strconv.Itoa(int(deploymentID)) + "s" + strconv.Itoa(slot)
}

func findContainerVeth(containerID string, deploymentID int32) (netlink.Link, int, error) {
	nsHandle, err := netns.GetFromName(containerID)
	if err != nil {
		return nil, 0, fmt.Errorf("opening netns %s: %w", containerID, err)
	}
	defer nsHandle.Close()
	nsNetlink, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return nil, 0, fmt.Errorf("opening netlink handle in netns %s: %w", containerID, err)
	}
	defer nsNetlink.Close()
	containerLink, err := nsNetlink.LinkByName(containerIface)
	if err != nil {
		return nil, 0, fmt.Errorf("finding %s in netns %s: %w", containerIface, containerID, err)
	}
	containerAttrs := containerLink.Attrs()
	for slot := range v4SlotsPerDeployment {
		hostLink, err := netlink.LinkByName(hostVethName(deploymentID, slot))
		if err != nil || hostLink.Type() != "veth" {
			continue
		}
		hostAttrs := hostLink.Attrs()
		if vethPeerIndexesMatch(hostAttrs.Index, hostAttrs.ParentIndex, containerAttrs.Index, containerAttrs.ParentIndex) {
			return hostLink, slot, nil
		}
	}
	return nil, 0, fmt.Errorf("finding veth peer for container %s", containerID)
}

func validateContainerV6Addresses(containerID string, inboundAddr, outboundAddr netip.Addr) error {
	nsHandle, err := netns.GetFromName(containerID)
	if err != nil {
		return fmt.Errorf("opening netns %s: %w", containerID, err)
	}
	defer nsHandle.Close()
	nsNetlink, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		return fmt.Errorf("opening netlink handle in netns %s: %w", containerID, err)
	}
	defer nsNetlink.Close()
	link, err := nsNetlink.LinkByName(containerIface)
	if err != nil {
		return fmt.Errorf("finding %s in netns %s: %w", containerIface, containerID, err)
	}
	addrs, err := nsNetlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return fmt.Errorf("listing IPv6 addresses in netns %s: %w", containerID, err)
	}
	foundInbound := false
	foundOutbound := false
	for _, addr := range addrs {
		ip, ok := netip.AddrFromSlice(addr.IP)
		if !ok {
			continue
		}
		switch ip {
		case inboundAddr:
			if addr.PreferedLft != 0 {
				return fmt.Errorf("inbound address %s is not deprecated in netns %s", inboundAddr, containerID)
			}
			foundInbound = true
		case outboundAddr:
			if addr.PreferedLft == 0 {
				return fmt.Errorf("outbound address %s is deprecated in netns %s", outboundAddr, containerID)
			}
			foundOutbound = true
		}
	}
	if !foundInbound || !foundOutbound {
		return fmt.Errorf("netns %s does not contain expected inbound/outbound addresses %s/%s", containerID, inboundAddr, outboundAddr)
	}
	return nil
}

// freeV4Slot picks the first slot whose host veth does not exist. Slots are
// derived from live kernel state, so there is no allocation store; at most two
// containers of a deployment run concurrently (current + rollover candidate).
func freeV4Slot(deploymentID int32) (int, error) {
	for slot := range v4SlotsPerDeployment {
		if _, err := netlink.LinkByName(hostVethName(deploymentID, slot)); err != nil {
			return slot, nil
		}
	}
	return 0, fmt.Errorf("no free v4 slot for deployment %d (both veths exist)", deploymentID)
}

// createNamedNetns creates a bind-mounted named netns and applies per-netns
// sysctls while the calling thread is inside it. Restores the original netns
// before returning.
func createNamedNetns(name string, unprivilegedPortStart int, setUnprivilegedPortStart bool) (netns.NsHandle, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	orig, err := netns.Get()
	if err != nil {
		return netns.None(), fmt.Errorf("getting current netns: %w", err)
	}
	defer orig.Close()
	handle, err := netns.NewNamed(name)
	if err != nil {
		return netns.None(), err
	}
	// /proc/sys/net is per-netns while this thread is inside the new ns.
	if setUnprivilegedPortStart {
		err = os.WriteFile("/proc/sys/net/ipv4/ip_unprivileged_port_start",
			[]byte(strconv.Itoa(unprivilegedPortStart)), 0)
	}
	if restoreErr := netns.Set(orig); restoreErr != nil {
		// The thread is stuck in the wrong ns; do not return it to the scheduler.
		panic(fmt.Sprintf("restoring original netns: %v", restoreErr))
	}
	if err != nil {
		handle.Close()
		_ = netns.DeleteNamed(name)
		return netns.None(), fmt.Errorf("setting ip_unprivileged_port_start: %w", err)
	}
	return handle, nil
}

func configureContainerSide(h *netlink.Handle, peerName string, outboundAddr, inboundAddr, contV4, hostV4 netip.Addr) error {
	lo, err := h.LinkByName("lo")
	if err == nil {
		_ = h.LinkSetUp(lo)
	}
	link, err := h.LinkByName(peerName)
	if err != nil {
		return fmt.Errorf("container peer link: %w", err)
	}
	if err := h.LinkSetName(link, containerIface); err != nil {
		return fmt.Errorf("renaming container link: %w", err)
	}
	link, err = h.LinkByName(containerIface)
	if err != nil {
		return fmt.Errorf("container %s: %w", containerIface, err)
	}
	if err := addContainerV6Addr(h, link, outboundAddr, false); err != nil {
		return err
	}
	if err := addContainerV6Addr(h, link, inboundAddr, true); err != nil {
		return err
	}
	if err := h.AddrAdd(link, &netlink.Addr{IPNet: netipPrefixToIPNet(netip.PrefixFrom(contV4, 30))}); err != nil {
		return fmt.Errorf("assigning container v4 address: %w", err)
	}
	if err := h.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing container link up: %w", err)
	}
	idx := link.Attrs().Index
	if err := h.RouteAdd(&netlink.Route{
		LinkIndex: idx,
		Gw:        net.IP(hostGateway.AsSlice()),
	}); err != nil {
		return fmt.Errorf("container v6 default route: %w", err)
	}
	if err := h.RouteAdd(&netlink.Route{
		LinkIndex: idx,
		Gw:        net.IP(hostV4.AsSlice()),
	}); err != nil {
		return fmt.Errorf("container v4 default route: %w", err)
	}
	return nil
}

const deprecatedAddrValidLft = 1<<31 - 1

func addContainerV6Addr(h *netlink.Handle, link netlink.Link, addr netip.Addr, deprecated bool) error {
	nlAddr := &netlink.Addr{
		IPNet: netipPrefixToIPNet(netip.PrefixFrom(addr, 128)),
		Flags: unix.IFA_F_NODAD,
	}
	if deprecated {
		// preferred_lft=0 keeps this address usable as a destination without
		// making it the preferred source for candidate warmup connections.
		nlAddr.PreferedLft = 0
		nlAddr.ValidLft = deprecatedAddrValidLft
	}
	if err := h.AddrAdd(link, nlAddr); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("assigning container v6 address %v: %w", addr, err)
	}
	return nil
}

func netipPrefixToIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}
