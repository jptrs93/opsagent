// Package ingressplan decides ingress listen collisions on the selector sets
// and expands the accepted claims against the cluster's host address
// inventory. It is a pure function of its inputs: the deployment validation
// layer and the network map publisher both call it, so the answer given at
// save time and the publish set distributed to nodes cannot diverge.
package ingressplan

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	DefaultHTTPSPort  uint32 = 443
	HTTPSRedirectPort uint32 = 80
)

// Route is one ingress route of a virtual-mode deployment.
type Route struct {
	Kind       apigen.IngressKind
	Hostname   string
	HostPort   uint32 // passthrough host port; HTTPS always claims 443 and 80
	PathPrefix string // HTTPS only, normalised
	CertSource string // HTTPS only, e.g. "acme" or "secret:12@3"
	Listen     []apigen.IngressListen
}

// Deployment is the evaluator's view of one deployment.
type Deployment struct {
	ID       uint64
	NodeID   uint64
	Name     string
	HostMode bool
	Routes   []Route
	// TCPPorts are raw TCP port forwards; they conflict with ingress ports
	// node-wide because port forwards have no listen selector.
	TCPPorts []uint32
}

// Node is one cluster member and its reported host addresses. The inventory
// only expands accepted claims into the publish set; collisions are decided
// on the selector sets. An empty address list means the inventory is unknown
// (an agent that does not report it yet): whole-space and family selectors
// then publish the wildcard and literal selectors publish their literal.
type Node struct {
	ID            uint64
	HostAddresses []netip.Addr
}

// Reservation is a listener the platform itself holds: the Web UI's HTTPS and
// HTTP servers on the primary. An invalid Address reserves every address of
// the node on that port.
type Reservation struct {
	NodeID  uint64
	Address netip.Addr
	Port    uint32
	Name    string
}

type Inputs struct {
	Deployments  []Deployment
	Nodes        []Node
	Reservations []Reservation
	// Candidate is the deployment being validated; a collision is an error
	// raised against it. Validation refuses every colliding save, so a render
	// without a candidate never meets a collision.
	Candidate uint64
	// Reachable lists the nodes whose netproxy can dial a route hosted on
	// the given node. Nil means the hosting node only.
	Reachable func(hostingNode uint64) []uint64
}

// Publish is one (address, port) a node forwards to its netproxy. A zero
// Address is the wildcard.
type Publish struct {
	Address netip.Addr
	Port    uint32
}

type Diagnostic struct {
	DeploymentID uint64
	Message      string
}

type Result struct {
	// Publish is the per-node DNAT set, sorted and deduplicated.
	Publish map[uint64][]Publish
	// PublishedDeployments lists, per node, the deployments with at least one
	// published claim on it, sorted by id.
	PublishedDeployments map[uint64][]uint64
	// Errors reject a save and are raised for the candidate. Warnings and
	// Excluded are informational: host-mode deployments beside a wildcard
	// publish, and stored claims a reservation keeps out of the publish set.
	Errors   []Diagnostic
	Warnings []Diagnostic
	Excluded []Diagnostic
}

func (r Result) FirstError() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("%s", r.Errors[0].Message)
}

// Diagnostics returns warnings and exclusions as one list for the UI.
func (r Result) Diagnostics() []Diagnostic {
	out := make([]Diagnostic, 0, len(r.Warnings)+len(r.Excluded))
	out = append(out, r.Excluded...)
	out = append(out, r.Warnings...)
	slices.SortStableFunc(out, func(a, b Diagnostic) int {
		if c := cmp.Compare(a.DeploymentID, b.DeploymentID); c != 0 {
			return c
		}
		return strings.Compare(a.Message, b.Message)
	})
	return out
}

type claim struct {
	deployment *Deployment
	route      *Route
	nodeID     uint64
	set        addrSet
	port       uint32
}

type portKey struct {
	nodeID uint64
	port   uint32
}

// addrSet is the address set one listen selector denotes on a node: the zero
// value is every address, a /0 prefix is one family, a single-address prefix
// is one literal, any other prefix is its range. Two sets intersect when one
// contains the other; the whole space intersects everything and the two
// families never intersect each other.
type addrSet struct {
	prefix netip.Prefix
}

func setOf(prefix netip.Prefix) addrSet {
	return addrSet{prefix: prefix.Masked()}
}

func (s addrSet) whole() bool { return !s.prefix.IsValid() }

func (s addrSet) literal() bool { return s.prefix.IsValid() && s.prefix.IsSingleIP() }

func (s addrSet) intersects(o addrSet) bool {
	if s.whole() || o.whole() {
		return true
	}
	return s.prefix.Contains(o.prefix.Addr()) || o.prefix.Contains(s.prefix.Addr())
}

func (s addrSet) String() string {
	switch {
	case s.whole():
		return "every address"
	case s.prefix.Bits() == 0 && s.prefix.Addr().Is4():
		return "every IPv4 address"
	case s.prefix.Bits() == 0:
		return "every IPv6 address"
	case s.literal():
		return "address " + s.prefix.Addr().String()
	}
	return "addresses in " + s.prefix.String()
}

func (s addrSet) expand(inventory []netip.Addr) []netip.Addr {
	if len(inventory) == 0 {
		switch {
		case s.whole(), s.prefix.Bits() == 0:
			return []netip.Addr{{}}
		case s.literal():
			return []netip.Addr{s.prefix.Addr()}
		}
		return nil
	}
	var out []netip.Addr
	for _, addr := range inventory {
		if s.whole() || s.prefix.Contains(addr) {
			out = append(out, addr)
		}
	}
	return out
}

func reservationSet(r *Reservation) addrSet {
	if !r.Address.IsValid() {
		return addrSet{}
	}
	return addrSet{prefix: netip.PrefixFrom(r.Address, r.Address.BitLen())}
}

// Evaluate expands every route into selector claims and decides collisions
// on the selector sets, so the answer does not depend on the inventory a
// node reports today. The inventory is used only to expand the accepted
// claims into the publish set.
func Evaluate(in Inputs) Result {
	result := Result{Publish: make(map[uint64][]Publish), PublishedDeployments: make(map[uint64][]uint64)}
	nodes := make(map[uint64]Node, len(in.Nodes))
	for _, node := range in.Nodes {
		nodes[node.ID] = node
	}
	hostModeByNode := make(map[uint64][]string)
	deployments := slices.Clone(in.Deployments)
	slices.SortFunc(deployments, func(a, b Deployment) int { return cmp.Compare(a.ID, b.ID) })
	for _, dep := range deployments {
		if dep.HostMode {
			hostModeByNode[dep.NodeID] = append(hostModeByNode[dep.NodeID], dep.Name)
		}
	}
	tcpPorts := make(map[portKey]uint64)
	for i := range deployments {
		dep := &deployments[i]
		if dep.HostMode {
			continue
		}
		for _, port := range dep.TCPPorts {
			key := portKey{dep.NodeID, port}
			if _, ok := tcpPorts[key]; !ok {
				tcpPorts[key] = dep.ID
			}
		}
	}

	accepted := make(map[portKey][]*claim)
	publish := make(map[uint64]map[Publish]struct{})
	published := make(map[uint64]map[uint64]struct{})
	addPublish := func(nodeID, deploymentID uint64, entry Publish) {
		if publish[nodeID] == nil {
			publish[nodeID] = make(map[Publish]struct{})
			published[nodeID] = make(map[uint64]struct{})
		}
		publish[nodeID][entry] = struct{}{}
		published[nodeID][deploymentID] = struct{}{}
	}
	warnedHostMode := make(map[[2]uint64]struct{})

	for i := range deployments {
		dep := &deployments[i]
		if dep.HostMode {
			continue
		}
		for j := range dep.Routes {
			route := &dep.Routes[j]
			for _, c := range expand(dep, route, nodes, in.Reachable) {
				key := portKey{c.nodeID, c.port}
				if reserved := matchReservation(in.Reservations, c); reserved != nil {
					if in.Candidate != 0 && dep.ID == in.Candidate {
						result.Errors = append(result.Errors, Diagnostic{dep.ID, fmt.Sprintf(
							"networking.ingress: %s on node %d port %d (%s) is reserved by the %s", route.Hostname, c.nodeID, c.port, c.set, reserved.Name)})
					} else {
						result.Excluded = append(result.Excluded, Diagnostic{dep.ID, fmt.Sprintf(
							"%s is not published on node %d port %d (%s): reserved by the %s", route.Hostname, c.nodeID, c.port, c.set, reserved.Name)})
					}
					continue
				}
				if _, ok := tcpPorts[key]; ok {
					result.Errors = append(result.Errors, Diagnostic{in.Candidate, fmt.Sprintf("networking: TCP host port %d conflicts with ingress on this node", c.port)})
					continue
				}
				if !resolveClaim(&result, in.Candidate, accepted[key], c) {
					continue
				}
				accepted[key] = append(accepted[key], c)
				addresses := c.set.expand(nodes[c.nodeID].HostAddresses)
				for _, address := range addresses {
					addPublish(c.nodeID, dep.ID, Publish{Address: address, Port: c.port})
				}
				if !c.set.literal() && len(addresses) > 0 {
					warned := [2]uint64{dep.ID, c.nodeID}
					if names := hostModeByNode[c.nodeID]; len(names) > 0 {
						if _, done := warnedHostMode[warned]; !done {
							warnedHostMode[warned] = struct{}{}
							result.Warnings = append(result.Warnings, Diagnostic{dep.ID, fmt.Sprintf(
								"ingress is published on every address of node %d, where host-mode deployment(s) %s may bind the same ports", c.nodeID, strings.Join(names, ", "))})
						}
					}
				}
			}
		}
	}
	// HTTPS claims are expanded once per port (443 and 80), so a collision is
	// found twice; report each finding once.
	result.Errors = dedupe(result.Errors)
	result.Warnings = dedupe(result.Warnings)
	result.Excluded = dedupe(result.Excluded)
	for nodeID, entries := range publish {
		list := make([]Publish, 0, len(entries))
		for entry := range entries {
			list = append(list, entry)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Port != list[j].Port {
				return list[i].Port < list[j].Port
			}
			return list[i].Address.Compare(list[j].Address) < 0
		})
		result.Publish[nodeID] = list
	}
	for nodeID, entries := range published {
		list := make([]uint64, 0, len(entries))
		for id := range entries {
			list = append(list, id)
		}
		slices.Sort(list)
		result.PublishedDeployments[nodeID] = list
	}
	return result
}

func dedupe(diags []Diagnostic) []Diagnostic {
	seen := make(map[Diagnostic]struct{}, len(diags))
	out := diags[:0]
	for _, d := range diags {
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

func routePrefix(route *Route) string {
	if route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		return route.PathPrefix
	}
	return ""
}

// resolveClaim checks c against the claims already accepted on its node and
// port. A collision is an error against the candidate and the claim is not
// published.
func resolveClaim(result *Result, candidate uint64, accepted []*claim, c *claim) bool {
	report := func(message string) bool {
		result.Errors = append(result.Errors, Diagnostic{candidate, "networking.ingress: " + message})
		return false
	}
	var overlapping []*claim
	for _, other := range accepted {
		if other.route.Hostname == c.route.Hostname && other.set.intersects(c.set) {
			overlapping = append(overlapping, other)
		}
	}
	for _, other := range overlapping {
		if other.deployment.ID == c.deployment.ID || other.route.Kind != c.route.Kind || routePrefix(other.route) != routePrefix(c.route) {
			continue
		}
		if c.route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
			return report(fmt.Sprintf("HTTPS route %s%s is already claimed by another deployment on this node", c.route.Hostname, c.route.PathPrefix))
		}
		return report(fmt.Sprintf("%s on host port %d is already claimed by another deployment on this node", c.route.Hostname, c.port))
	}
	for _, other := range overlapping {
		if other.deployment.ID != c.deployment.ID && other.route.Kind != c.route.Kind {
			return report(fmt.Sprintf("%s cannot use both HTTPS and TLS_PASSTHROUGH on host port %d on this node", c.route.Hostname, c.port))
		}
	}
	if c.route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		// Unlike the ownership rule, the cert source must agree within one
		// deployment too: netproxy serves one certificate per hostname.
		for _, other := range overlapping {
			if other.route.Kind == c.route.Kind && other.route.CertSource != c.route.CertSource {
				return report(fmt.Sprintf("certSource for %s must match across all HTTPS routes on this node", c.route.Hostname))
			}
		}
	}
	return true
}

func matchReservation(reservations []Reservation, c *claim) *Reservation {
	for i := range reservations {
		r := &reservations[i]
		if r.NodeID == c.nodeID && r.Port == c.port && reservationSet(r).intersects(c.set) {
			return r
		}
	}
	return nil
}

// expand lists the selector claims of one route: the selector's nodes, each
// address set of the selector, crossed with the route's ports. The default
// node selector is the deployment's scheduled node; any and named selectors
// are intersected with the reachable set.
func expand(dep *Deployment, route *Route, nodes map[uint64]Node, reachable func(uint64) []uint64) []*claim {
	scheduled := []uint64{dep.NodeID}
	reach := scheduled
	if reachable != nil {
		reach = reachable(dep.NodeID)
	}
	selectors := route.Listen
	if len(selectors) == 0 {
		selectors = []apigen.IngressListen{{}}
	}
	ports := RoutePorts(route)
	type setKey struct {
		node uint64
		set  addrSet
	}
	seen := make(map[setKey]struct{})
	var out []*claim
	for i := range selectors {
		selector := &selectors[i]
		var specific *apigen.SpecificNode
		candidates := scheduled
		if selector.Node.Present {
			specific = selector.Node.Value.Value.Specific
			if selector.Node.Value.Value.Any != nil || specific != nil {
				candidates = reach
			}
		}
		for _, nodeID := range candidates {
			if specific != nil && specific.NodeID != nodeID {
				continue
			}
			if _, known := nodes[nodeID]; !known {
				continue
			}
			for _, set := range selectorSets(selector.Addresses) {
				key := setKey{nodeID, set}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				for _, port := range ports {
					out = append(out, &claim{deployment: dep, route: route, nodeID: nodeID, set: set, port: port})
				}
			}
		}
	}
	return out
}

func selectorSets(addresses []apigen.IpPrefix) []addrSet {
	if len(addresses) == 0 {
		return []addrSet{{}}
	}
	out := make([]addrSet, 0, len(addresses))
	for _, entry := range addresses {
		out = append(out, setOf(entry.Prefix()))
	}
	return out
}

// RoutePorts lists the host ports a route claims: 443 and 80 for HTTPS, the
// configured host port (default 443) for passthrough.
func RoutePorts(route *Route) []uint32 {
	if route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		return []uint32{DefaultHTTPSPort, HTTPSRedirectPort}
	}
	if route.HostPort == 0 {
		return []uint32{DefaultHTTPSPort}
	}
	return []uint32{route.HostPort}
}

// DeploymentFromSpec builds the evaluator's view of one validated deployment
// spec: hostnames and path prefixes are stored in canonical form.
func DeploymentFromSpec(id, nodeID uint64, name string, spec *apigen.DeploymentSpec) Deployment {
	dep := Deployment{ID: id, NodeID: nodeID, Name: name}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		dep.HostMode = spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST
		return dep
	}
	for _, pf := range spec.Networking.PortForwarding {
		if pf.Protocol == apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP {
			dep.TCPPorts = append(dep.TCPPorts, pf.HostPort)
		}
	}
	for _, route := range spec.Networking.Ingress {
		switch {
		case route.Config.Value.TlsPassthrough != nil:
			dep.Routes = append(dep.Routes, Route{Kind: apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, Hostname: route.Hostname, HostPort: route.Config.Value.TlsPassthrough.HostPort.Value, Listen: route.Listen})
		case route.Config.Value.Https != nil:
			https := route.Config.Value.Https
			dep.Routes = append(dep.Routes, Route{Kind: apigen.IngressKind_INGRESS_KIND_HTTPS, Hostname: route.Hostname, PathPrefix: https.PathPrefix, CertSource: CertSourceClaim(https.CertSource), Listen: route.Listen})
		}
	}
	return dep
}

func CertSourceClaim(source apigen.Maybe[apigen.CertSource]) string {
	if source.Present && source.Value.Value.Secret != nil {
		return "secret:" + source.Value.Value.Secret.Secret.String()
	}
	return "acme"
}

// HostAddresses converts a node's stored address inventory, which the node
// write path keeps canonical, deduplicated and sorted.
func HostAddresses(values []apigen.IpAddress) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		out = append(out, value.Addr())
	}
	return out
}

// WebUIReservations derives the platform's own listeners from the resolved
// cluster settings. An empty listen host is a wildcard bind.
func WebUIReservations(primaryNodeID uint64, httpsEnabled bool, httpsListen string, httpEnabled bool, httpListen string) []Reservation {
	var out []Reservation
	if httpsEnabled {
		if r, ok := listenReservation(primaryNodeID, httpsListen, "primary Web UI (https_web.listen)"); ok {
			out = append(out, r)
		}
	}
	if httpEnabled {
		if r, ok := listenReservation(primaryNodeID, httpListen, "primary Web UI (http_web.listen)"); ok {
			out = append(out, r)
		}
	}
	return out
}

func listenReservation(nodeID uint64, listen, name string) (Reservation, bool) {
	host, port, err := splitListen(listen)
	if err != nil || port == 0 {
		return Reservation{}, false
	}
	r := Reservation{NodeID: nodeID, Port: port, Name: name}
	if host != "" {
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return Reservation{}, false
		}
		if !addr.IsUnspecified() {
			r.Address = addr
		}
	}
	return r, true
}

func splitListen(value string) (string, uint32, error) {
	value = strings.TrimSpace(value)
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("listen %q has no port", value)
	}
	host := strings.Trim(value[:idx], "[]")
	var port uint32
	for _, ch := range value[idx+1:] {
		if ch < '0' || ch > '9' {
			return "", 0, fmt.Errorf("listen %q has a non-numeric port", value)
		}
		port = port*10 + uint32(ch-'0')
		if port > 65535 {
			return "", 0, fmt.Errorf("listen %q port out of range", value)
		}
	}
	return host, port, nil
}

// SelectorSummary renders one listen entry for diagnostics and the UI.
func SelectorSummary(entry *apigen.IngressListen, nodeName func(uint64) string) string {
	node := "scheduled node"
	if entry != nil && entry.Node.Present {
		switch {
		case entry.Node.Value.Value.Any != nil:
			node = "any node"
		case entry.Node.Value.Value.Specific != nil:
			node = "node " + nodeName(entry.Node.Value.Value.Specific.NodeID)
		}
	}
	address := "any address"
	if entry != nil && len(entry.Addresses) > 0 {
		parts := make([]string, 0, len(entry.Addresses))
		for _, prefix := range entry.Addresses {
			parts = append(parts, PrefixSummary(prefix))
		}
		address = strings.Join(parts, ", ")
	}
	return node + ", " + address
}

func PrefixSummary(entry apigen.IpPrefix) string {
	prefix := entry.Prefix()
	switch {
	case !prefix.IsValid():
		return "invalid address"
	case prefix.Bits() == 0 && prefix.Addr().Is4():
		return "any IPv4 address"
	case prefix.Bits() == 0:
		return "any IPv6 address"
	case prefix.IsSingleIP():
		return prefix.Addr().String()
	}
	return prefix.String()
}
