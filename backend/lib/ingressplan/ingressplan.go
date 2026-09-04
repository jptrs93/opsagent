// Package ingressplan expands ingress listen selectors against the cluster's
// host address inventory and detects collisions. It is a pure function of its
// inputs: the deployment validation layer and the network map publisher both
// call it, so the answer given at save time and the publish set distributed
// to nodes cannot diverge.
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
	DefaultHTTPSPort  int32 = 443
	HTTPSRedirectPort int32 = 80
)

// Route is one ingress route of a virtual-mode deployment.
type Route struct {
	Kind       apigen.IngressKind
	Hostname   string
	HostPort   int32  // passthrough host port; HTTPS always claims 443 and 80
	PathPrefix string // HTTPS only, normalised
	CertSource string // HTTPS only, e.g. "acme" or "secret:12"
	Listen     []*apigen.IngressListen
}

// Deployment is the evaluator's view of one deployment.
type Deployment struct {
	ID       int32
	NodeID   int32
	Name     string
	HostMode bool
	Routes   []Route
	// TCPPorts are raw TCP port forwards; they conflict with ingress ports
	// node-wide because port forwards have no listen selector.
	TCPPorts []int32
}

// Node is one cluster member and its reported host addresses. An empty
// address list means the inventory is unknown (an agent that does not report
// it yet): wildcard selectors then publish on every local address and literal
// selectors publish their literal.
type Node struct {
	ID            int32
	HostAddresses []netip.Addr
}

// Reservation is a listener the platform itself holds: the Web UI's HTTPS and
// HTTP servers on the primary. An invalid Address is a wildcard bind.
type Reservation struct {
	NodeID  int32
	Address netip.Addr
	Port    int32
	Name    string
}

type Inputs struct {
	Deployments  []Deployment
	Nodes        []Node
	Reservations []Reservation
	// Candidate is the deployment being validated. Collisions involving it are
	// errors; collisions between two stored deployments (which can arise when
	// the inventory changes after save) resolve to the lower id with warnings.
	Candidate int32
	// Reachable lists the nodes whose netproxy can dial a route hosted on
	// the given node. Nil means the hosting node only.
	Reachable func(hostingNode int32) []int32
}

// Publish is one (address, port) a node forwards to its netproxy. A zero
// Address is the wildcard.
type Publish struct {
	Address netip.Addr
	Port    int32
}

type Diagnostic struct {
	DeploymentID int32
	Message      string
}

type Result struct {
	// Publish is the per-node DNAT set, sorted and deduplicated.
	Publish map[int32][]Publish
	// Errors reject a save. Warnings and Excluded are informational: claims a
	// reservation removed, collisions resolved against a deployment, and
	// host-mode deployments beside a wildcard publish.
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

// claim is one expanded (node, address, port) a route wants, with the
// hostname-level identity used for collision checks.
type claim struct {
	deployment *Deployment
	route      *Route
	nodeID     int32
	address    netip.Addr // zero = wildcard (inventory unknown)
	port       int32
	literal    bool // the selector named this exact address
}

type claimKey struct {
	nodeID   int32
	address  netip.Addr
	port     int32
	hostname string
	prefix   string
}

// Evaluate expands every route and applies the reservation and collision
// rules. Deployments are processed in id order, so a collision between two
// stored deployments resolves to the lower id.
func Evaluate(in Inputs) Result {
	result := Result{Publish: make(map[int32][]Publish)}
	nodes := make(map[int32]Node, len(in.Nodes))
	for _, node := range in.Nodes {
		nodes[node.ID] = node
	}
	hostModeByNode := make(map[int32][]string)
	deployments := slices.Clone(in.Deployments)
	slices.SortFunc(deployments, func(a, b Deployment) int { return cmp.Compare(a.ID, b.ID) })
	for _, dep := range deployments {
		if dep.HostMode {
			hostModeByNode[dep.NodeID] = append(hostModeByNode[dep.NodeID], dep.Name)
		}
	}
	tcpPorts := make(map[int32]map[int32]int32) // node -> port -> owner
	for i := range deployments {
		dep := &deployments[i]
		if dep.HostMode {
			continue
		}
		for _, port := range dep.TCPPorts {
			if tcpPorts[dep.NodeID] == nil {
				tcpPorts[dep.NodeID] = make(map[int32]int32)
			}
			if _, ok := tcpPorts[dep.NodeID][port]; !ok {
				tcpPorts[dep.NodeID][port] = dep.ID
			}
		}
	}

	owners := make(map[claimKey]*claim)
	hostKinds := make(map[hostKindKey]*claim)
	hostCerts := make(map[hostKindKey]*claim)
	publish := make(map[int32]map[Publish]struct{})
	addPublish := func(nodeID int32, entry Publish) {
		if publish[nodeID] == nil {
			publish[nodeID] = make(map[Publish]struct{})
		}
		publish[nodeID][entry] = struct{}{}
	}
	warnedHostMode := make(map[[2]int32]struct{})

	for i := range deployments {
		dep := &deployments[i]
		if dep.HostMode {
			continue
		}
		for j := range dep.Routes {
			route := &dep.Routes[j]
			for _, c := range expand(dep, route, nodes, in.Reachable) {
				if reserved := matchReservation(in.Reservations, c); reserved != nil {
					if c.literal {
						result.Errors = append(result.Errors, Diagnostic{dep.ID, fmt.Sprintf(
							"networking.ingress: %s on node %d address %s port %d is reserved by the %s", route.Hostname, c.nodeID, c.address, c.port, reserved.Name)})
					} else {
						result.Excluded = append(result.Excluded, Diagnostic{dep.ID, fmt.Sprintf(
							"%s is not published on node %d port %d%s: reserved by the %s", route.Hostname, c.nodeID, c.port, addressSuffix(c.address), reserved.Name)})
					}
					continue
				}
				if owner, ok := tcpPorts[c.nodeID][c.port]; ok {
					message := fmt.Sprintf("networking: TCP host port %d conflicts with ingress on this node", c.port)
					if in.Candidate != 0 && (dep.ID == in.Candidate || owner == in.Candidate) {
						result.Errors = append(result.Errors, Diagnostic{in.Candidate, message})
					} else {
						result.Warnings = append(result.Warnings, Diagnostic{dep.ID, fmt.Sprintf("%s is not published on node %d port %d: %s", route.Hostname, c.nodeID, c.port, message)})
					}
					continue
				}
				if !resolveClaim(&result, in.Candidate, owners, hostKinds, hostCerts, c) {
					continue
				}
				addPublish(c.nodeID, Publish{Address: c.address, Port: c.port})
				if !c.literal {
					key := [2]int32{dep.ID, c.nodeID}
					if names := hostModeByNode[c.nodeID]; len(names) > 0 {
						if _, done := warnedHostMode[key]; !done {
							warnedHostMode[key] = struct{}{}
							result.Warnings = append(result.Warnings, Diagnostic{dep.ID, fmt.Sprintf(
								"ingress is published on every address of node %d, where host-mode deployment(s) %s may bind the same ports", c.nodeID, strings.Join(names, ", "))})
						}
					}
				}
			}
		}
	}
	// HTTPS claims are expanded once per port (443 and 80), so a collision or
	// exclusion is found twice; report each finding once.
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

type hostKindKey struct {
	nodeID   int32
	address  netip.Addr
	port     int32
	hostname string
}

// resolveClaim records c in the ownership maps or reports why it cannot own
// its key. It returns false when the claim must not be published.
func resolveClaim(result *Result, candidate int32, owners map[claimKey]*claim, hostKinds, hostCerts map[hostKindKey]*claim, c *claim) bool {
	key := claimKey{c.nodeID, c.address, c.port, c.route.Hostname, ""}
	if c.route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		key.prefix = c.route.PathPrefix
	}
	hostKey := hostKindKey{c.nodeID, c.address, c.port, c.route.Hostname}
	report := func(other *claim, message string) bool {
		if candidate != 0 && (c.deployment.ID == candidate || other.deployment.ID == candidate) {
			result.Errors = append(result.Errors, Diagnostic{candidate, "networking.ingress: " + message})
			return false
		}
		if other.deployment.ID == c.deployment.ID {
			result.Warnings = append(result.Warnings, Diagnostic{c.deployment.ID, fmt.Sprintf("%s is not published on node %d%s: %s", c.route.Hostname, c.nodeID, addressSuffix(c.address), message)})
			return false
		}
		result.Warnings = append(result.Warnings, Diagnostic{c.deployment.ID, fmt.Sprintf("%s is not published on node %d%s: %s", c.route.Hostname, c.nodeID, addressSuffix(c.address), message)})
		result.Warnings = append(result.Warnings, Diagnostic{other.deployment.ID, fmt.Sprintf("%s on node %d%s collides with deployment %d (%s); this deployment keeps the route", other.route.Hostname, other.nodeID, addressSuffix(other.address), c.deployment.ID, c.deployment.Name)})
		return false
	}
	if other, ok := owners[key]; ok && other.deployment.ID != c.deployment.ID {
		if c.route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
			return report(other, fmt.Sprintf("HTTPS route %s%s is already claimed by another deployment on this node", c.route.Hostname, c.route.PathPrefix))
		}
		return report(other, fmt.Sprintf("%s on host port %d is already claimed by another deployment on this node", c.route.Hostname, c.port))
	}
	if other, ok := hostKinds[hostKey]; ok && other.route.Kind != c.route.Kind && other.deployment.ID != c.deployment.ID {
		return report(other, fmt.Sprintf("%s cannot use both HTTPS and TLS_PASSTHROUGH on host port %d on this node", c.route.Hostname, c.port))
	}
	if c.route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		// Unlike the ownership keys, the cert source must agree within one
		// deployment too: netproxy serves one certificate per hostname.
		if other, ok := hostCerts[hostKey]; ok && other.route.CertSource != c.route.CertSource {
			return report(other, fmt.Sprintf("certSource for %s must match across all HTTPS routes on this node", c.route.Hostname))
		}
		if _, ok := hostCerts[hostKey]; !ok {
			hostCerts[hostKey] = c
		}
	}
	if _, ok := owners[key]; !ok {
		owners[key] = c
	}
	if _, ok := hostKinds[hostKey]; !ok {
		hostKinds[hostKey] = c
	}
	return true
}

func matchReservation(reservations []Reservation, c *claim) *Reservation {
	for i := range reservations {
		r := &reservations[i]
		if r.NodeID != c.nodeID || r.Port != c.port {
			continue
		}
		if !r.Address.IsValid() || !c.address.IsValid() || r.Address == c.address {
			return r
		}
	}
	return nil
}

func addressSuffix(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	return " address " + addr.String()
}

// expand lists the concrete claims of one route: the selector's nodes, each
// node's inventory filtered by the address selector, crossed with the route's
// ports. The default node selector is the deployment's scheduled node; any
// and named selectors are intersected with the reachable set.
func expand(dep *Deployment, route *Route, nodes map[int32]Node, reachable func(int32) []int32) []*claim {
	scheduled := []int32{dep.NodeID}
	reach := scheduled
	if reachable != nil {
		reach = reachable(dep.NodeID)
	}
	selectors := route.Listen
	if len(selectors) == 0 {
		selectors = []*apigen.IngressListen{{}}
	}
	ports := RoutePorts(route)
	type addrKey struct {
		node int32
		addr netip.Addr
	}
	seen := make(map[addrKey]struct{})
	var out []*claim
	for _, selector := range selectors {
		if selector == nil {
			selector = &apigen.IngressListen{}
		}
		candidates := scheduled
		if selector.Node != nil && (selector.Node.Any || selector.Node.NodeID != 0) {
			candidates = reach
		}
		for _, nodeID := range candidates {
			if selector.Node != nil && !selector.Node.Any && selector.Node.NodeID != 0 && selector.Node.NodeID != nodeID {
				continue
			}
			node, known := nodes[nodeID]
			if !known {
				continue
			}
			for _, expanded := range expandAddresses(selector.Address, node.HostAddresses) {
				key := addrKey{nodeID, expanded.addr}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				for _, port := range ports {
					out = append(out, &claim{deployment: dep, route: route, nodeID: nodeID, address: expanded.addr, port: port, literal: expanded.literal})
				}
			}
		}
	}
	return out
}

type expandedAddress struct {
	addr    netip.Addr
	literal bool
}

// expandAddresses filters a node's inventory by one address selector. With an
// unknown inventory a wildcard or family selector yields the wildcard entry
// and literal prefixes yield their literal address; CIDR prefixes yield
// nothing, because there is no inventory to intersect them with.
func expandAddresses(selector *apigen.AddressSelector, inventory []netip.Addr) []expandedAddress {
	family := apigen.AddressFamily_ADDRESS_FAMILY_ANY
	var prefixes []netip.Prefix
	var literals []netip.Addr
	if selector != nil {
		family = selector.Family
		for _, raw := range selector.Prefixes {
			if addr, err := netip.ParseAddr(raw); err == nil {
				literals = append(literals, addr.Unmap())
				continue
			}
			if prefix, err := netip.ParsePrefix(raw); err == nil {
				prefixes = append(prefixes, prefix.Masked())
			}
		}
	}
	familyMatches := func(addr netip.Addr) bool {
		switch family {
		case apigen.AddressFamily_ADDRESS_FAMILY_IPV4:
			return addr.Is4()
		case apigen.AddressFamily_ADDRESS_FAMILY_IPV6:
			return addr.Is6()
		}
		return true
	}
	var out []expandedAddress
	if len(prefixes) == 0 && len(literals) == 0 {
		if len(inventory) == 0 {
			return []expandedAddress{{}}
		}
		for _, addr := range inventory {
			if familyMatches(addr) {
				out = append(out, expandedAddress{addr: addr})
			}
		}
		return out
	}
	if len(inventory) == 0 {
		for _, addr := range literals {
			if familyMatches(addr) {
				out = append(out, expandedAddress{addr: addr, literal: true})
			}
		}
		return out
	}
	for _, addr := range inventory {
		if !familyMatches(addr) {
			continue
		}
		if slices.Contains(literals, addr) {
			out = append(out, expandedAddress{addr: addr, literal: true})
			continue
		}
		for _, prefix := range prefixes {
			if prefix.Contains(addr) {
				out = append(out, expandedAddress{addr: addr})
				break
			}
		}
	}
	return out
}

// RoutePorts lists the host ports a route claims: 443 and 80 for HTTPS, the
// configured host port (default 443) for passthrough.
func RoutePorts(route *Route) []int32 {
	if route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS {
		return []int32{DefaultHTTPSPort, HTTPSRedirectPort}
	}
	if route.HostPort == 0 {
		return []int32{DefaultHTTPSPort}
	}
	return []int32{route.HostPort}
}

// DeploymentFromSpec builds the evaluator's view of one deployment. Hostnames
// and prefixes are expected to be normalised already (validation runs before
// save); the function lower-cases hostnames defensively.
func DeploymentFromSpec(id, nodeID int32, name string, spec *apigen.DeploymentSpec) Deployment {
	dep := Deployment{ID: id, NodeID: nodeID, Name: name}
	if spec == nil {
		return dep
	}
	if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		dep.HostMode = spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST
		return dep
	}
	for _, pf := range spec.Networking.PortForwarding {
		if pf != nil && pf.Protocol == apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP && pf.HostPort >= 1 && pf.HostPort <= 65535 {
			dep.TCPPorts = append(dep.TCPPorts, pf.HostPort)
		}
	}
	for _, route := range spec.Networking.Ingress {
		if route == nil {
			continue
		}
		hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.Hostname)), ".")
		if hostname == "" {
			continue
		}
		switch {
		case route.Kind == apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH && route.TlsPassthroughConfig != nil:
			dep.Routes = append(dep.Routes, Route{Kind: route.Kind, Hostname: hostname, HostPort: route.TlsPassthroughConfig.HostPort, Listen: route.Listen})
		case route.Kind == apigen.IngressKind_INGRESS_KIND_HTTPS && route.HttpsConfig != nil:
			prefix := strings.TrimSpace(route.HttpsConfig.PathPrefix)
			if prefix == "" {
				prefix = "/"
			}
			dep.Routes = append(dep.Routes, Route{Kind: route.Kind, Hostname: hostname, PathPrefix: prefix, CertSource: CertSourceClaim(route.HttpsConfig.CertSource), Listen: route.Listen})
		}
	}
	return dep
}

func CertSourceClaim(source *apigen.CertSource) string {
	if source == nil || source.Acme != nil {
		return "acme"
	}
	if source.Secret != nil {
		return fmt.Sprintf("secret:%d", source.Secret.SecretVersionID)
	}
	return "acme"
}

// ParseHostAddresses converts stored address strings to addresses, dropping
// anything that does not parse.
func ParseHostAddresses(values []string) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || addr.Zone() != "" {
			continue
		}
		out = append(out, addr.Unmap())
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out)
}

// WebUIReservations derives the platform's own listeners from the resolved
// cluster settings. An empty listen host is a wildcard bind.
func WebUIReservations(primaryNodeID int32, httpsEnabled bool, httpsListen string, httpEnabled bool, httpListen string) []Reservation {
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

func listenReservation(nodeID int32, listen, name string) (Reservation, bool) {
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
			r.Address = addr.Unmap()
		}
	}
	return r, true
}

func splitListen(value string) (string, int32, error) {
	value = strings.TrimSpace(value)
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return "", 0, fmt.Errorf("listen %q has no port", value)
	}
	host := strings.Trim(value[:idx], "[]")
	var port int32
	for _, ch := range value[idx+1:] {
		if ch < '0' || ch > '9' {
			return "", 0, fmt.Errorf("listen %q has a non-numeric port", value)
		}
		port = port*10 + int32(ch-'0')
		if port > 65535 {
			return "", 0, fmt.Errorf("listen %q port out of range", value)
		}
	}
	return host, port, nil
}

// SelectorSummary renders one listen entry for diagnostics and the UI.
func SelectorSummary(entry *apigen.IngressListen, nodeName func(int32) string) string {
	node := "scheduled node"
	if entry != nil && entry.Node != nil {
		switch {
		case entry.Node.Any:
			node = "any node"
		case entry.Node.NodeID != 0:
			node = "node " + nodeName(entry.Node.NodeID)
		}
	}
	address := "any address"
	if entry != nil && entry.Address != nil {
		switch {
		case len(entry.Address.Prefixes) > 0:
			address = strings.Join(entry.Address.Prefixes, ", ")
		case entry.Address.Family == apigen.AddressFamily_ADDRESS_FAMILY_IPV4:
			address = "any IPv4 address"
		case entry.Address.Family == apigen.AddressFamily_ADDRESS_FAMILY_IPV6:
			address = "any IPv6 address"
		}
	}
	return node + ", " + address
}
