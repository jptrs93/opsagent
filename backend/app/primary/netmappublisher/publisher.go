// Package netmappublisher renders and distributes complete cluster network
// maps. The map is derived state: it is never persisted on the primary and is
// re-rendered from the store on every boot. Published snapshots are immutable.
package netmappublisher

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type subscriber struct {
	nodeID  int32
	updates chan *apigen.ClusterNetMap
}

type Publisher struct {
	store  *state.Service
	prefix network.Prefix

	mu        sync.Mutex
	refreshMu sync.Mutex
	current   *apigen.ClusterNetMap
	// lastRenderedSeq is the global write sequence of the newest completed
	// render, whether or not it changed the published map. A decision at seq N
	// is in force once lastRenderedSeq has reached N and the current map is
	// applied everywhere: if the render at N changed no routes, the map already
	// in force encodes it.
	lastRenderedSeq int64
	subscribers       map[*subscriber]struct{}
	nodeUpdates       <-chan apigen.ClusterNode
	instanceUpdates   <-chan apigen.ScheduledInstanceState
	policyUpdates     <-chan apigen.NetworkPolicy
	deploymentUpdates <-chan apigen.Deployment
	unsubscribe       []func()
	closeOnce       sync.Once

	// Acknowledgement state is kept under its own lock so recording a worker's
	// applied sequence never contends with rendering or publishing a map.
	ackMu      sync.Mutex
	applied    map[int32]int64
	ackUpdates chan struct{}
}

func New(store *state.Service, prefix network.Prefix) (*Publisher, error) {
	if store == nil {
		return nil, fmt.Errorf("network-map store is nil")
	}
	if prefix.IsZero() {
		return nil, fmt.Errorf("network-map prefix is not configured")
	}
	nodeSub, unsubscribeNodes := store.SubscribeNodeUpdates()
	_, instanceUpdates, unsubscribeInstances := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	policySub, unsubscribePolicies := store.SubscribeNetworkPolicyUpdates()
	_, deploymentUpdates, unsubscribeDeployments := store.MustFetchDeploymentSnapshotAndSubscribe(nil)
	p := &Publisher{
		store:             store,
		prefix:            prefix,
		subscribers:       make(map[*subscriber]struct{}),
		nodeUpdates:       nodeSub.Ch,
		instanceUpdates:   instanceUpdates,
		policyUpdates:     policySub.Ch,
		deploymentUpdates: deploymentUpdates,
		unsubscribe:       []func(){unsubscribeNodes, unsubscribeInstances, unsubscribePolicies, unsubscribeDeployments},
		applied:           make(map[int32]int64),
		ackUpdates:        make(chan struct{}, 1),
	}
	if err := p.Refresh(); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Publisher) Close() {
	p.closeOnce.Do(func() {
		for _, unsubscribe := range p.unsubscribe {
			unsubscribe()
		}
	})
}

func (p *Publisher) Run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "NetmapPublisher")
	defer p.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-p.nodeUpdates:
			if !ok {
				return
			}
		case _, ok := <-p.instanceUpdates:
			if !ok {
				return
			}
		case _, ok := <-p.policyUpdates:
			if !ok {
				return
			}
		case _, ok := <-p.deploymentUpdates:
			if !ok {
				return
			}
		}
		if err := p.Refresh(); err != nil {
			slog.ErrorContext(ctx, "refreshing cluster network map failed", "err", err)
		}
	}
}

// Refresh synchronously renders changed topology. It is used by enrollment and
// the scheduler to avoid acting on a map from before their own transaction.
func (p *Publisher) Refresh() error {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	inputs := p.store.FetchNetworkMapInputs()
	seq := inputs.Seq
	next, err := render(p.prefix, inputs)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	defer notifyAck(p.ackUpdates)
	if seq > p.lastRenderedSeq {
		p.lastRenderedSeq = seq
	}
	if p.current != nil && slices.Equal(canonicalContent(p.current), canonicalContent(next)) {
		return nil
	}
	// Content changed, so some map-input write happened since the previous
	// render, and every map-input write advances the counter in its own
	// transaction. An equal stamp means a write site is missing its bump.
	if p.current != nil && seq <= p.current.DerivedFromSeq {
		return fmt.Errorf("cluster network map content changed without a global sequence advance (seq %d)", seq)
	}
	next.DerivedFromSeq = seq
	p.current = next
	for sub := range p.subscribers {
		publishLatest(sub.updates, mapForNode(next, sub.nodeID))
	}
	return nil
}

func (p *Publisher) SnapshotForNode(nodeID int32) *apigen.ClusterNetMap {
	p.mu.Lock()
	defer p.mu.Unlock()
	return mapForNode(p.current, nodeID)
}

// SnapshotAndSubscribe atomically returns the latest targeted map and a
// capacity-one stream which replaces queued obsolete maps.
func (p *Publisher) SnapshotAndSubscribe(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sub := &subscriber{nodeID: nodeID, updates: make(chan *apigen.ClusterNetMap, 1)}
	p.subscribers[sub] = struct{}{}
	var once sync.Once
	return mapForNode(p.current, nodeID), sub.updates, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subscribers, sub)
			p.mu.Unlock()
		})
	}
}

func publishLatest(ch chan *apigen.ClusterNetMap, next *apigen.ClusterNetMap) {
	select {
	case ch <- next:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- next:
		default:
		}
	}
}

func mapForNode(source *apigen.ClusterNetMap, nodeID int32) *apigen.ClusterNetMap {
	if source == nil {
		return nil
	}
	next := *source
	next.TargetNodeID = nodeID
	next.UlaPrefix = slices.Clone(source.UlaPrefix)
	next.Nodes = slices.Clone(source.Nodes)
	next.Routes = slices.Clone(source.Routes)
	next.PolicyRules = slices.Clone(source.PolicyRules)
	next.DnsServices = slices.Clone(source.DnsServices)
	return &next
}

func canonicalContent(source *apigen.ClusterNetMap) []byte {
	canonical := mapForNode(source, 0)
	canonical.DerivedFromSeq = 0
	return canonical.Encode()
}

func render(prefix network.Prefix, inputs state.NetworkMapInputs) (*apigen.ClusterNetMap, error) {
	nodes, instances := inputs.Nodes, inputs.Instances
	netNodes := make([]*apigen.ClusterNetMapNode, 0, len(nodes))
	knownNodes := make(map[int32]struct{}, len(nodes))
	underlayBits := 0
	for _, node := range nodes {
		if node == nil || node.ID <= 0 {
			continue
		}
		underlay := ""
		if len(node.Addresses) > 0 {
			underlay = strings.TrimSpace(node.Addresses[0])
			if underlay != "" {
				addr, err := netip.ParseAddr(underlay)
				if err != nil || addr.Zone() != "" {
					return nil, fmt.Errorf("node %d has invalid underlay address %q", node.ID, underlay)
				}
				addr = addr.Unmap()
				if underlayBits != 0 && addr.BitLen() != underlayBits {
					return nil, fmt.Errorf("node %d underlay address family differs from cluster", node.ID)
				}
				underlayBits = addr.BitLen()
				underlay = addr.String()
			}
		}
		if node.WGPublicKey == "" {
			return nil, fmt.Errorf("node %d has no WireGuard public key", node.ID)
		}
		knownNodes[node.ID] = struct{}{}
		netNodes = append(netNodes, &apigen.ClusterNetMapNode{NodeID: node.ID, UnderlayAddress: underlay, WgPublicKey: node.WGPublicKey, WgListenPort: int32(network.DefaultWGListenPort)})
	}
	slices.SortFunc(netNodes, func(a, b *apigen.ClusterNetMapNode) int { return cmp.Compare(a.NodeID, b.NodeID) })

	// Routes are a pure function of assignments. Nothing here reads runner status:
	// a placement's route follows its target state, so a container restart, a
	// crash, or a status message arriving out of order cannot move a route or
	// churn the map's sequence.
	//
	// Every live placement owns the /120 covering its runs, for its whole life.
	// The serving placement's node additionally owns the /100 covering the whole
	// instance, which is what carries the stable inbound address. Because a
	// draining placement keeps its own /120, flipping the /100 to a replacement
	// never strands replies to work still in flight on the old one.
	routesByPrefix := make(map[netip.Prefix]int32, len(instances)+len(instances)/2)
	setRoute := func(destination netip.Prefix, nodeID int32) error {
		if existing, ok := routesByPrefix[destination]; ok && existing != nodeID {
			return fmt.Errorf("prefix %s is claimed by nodes %d and %d", destination, existing, nodeID)
		}
		routesByPrefix[destination] = nodeID
		return nil
	}
	servicesByDeployment := make(map[int32]*apigen.ClusterNetMapService)
	type ordinalKey struct{ deploymentID, ordinal int32 }
	type ordinalStates struct{ serving, standby, draining bool }
	statesByOrdinal := make(map[ordinalKey]*ordinalStates)
	for _, item := range instances {
		cfg := item.Config
		inst := item.Instance
		if inst.ID <= 0 || cfg.ID <= 0 ||
			cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		if servicesByDeployment[cfg.ID] == nil {
			servicesByDeployment[cfg.ID] = &apigen.ClusterNetMapService{Name: network.DNSLabel(cfg.Name), SpaceID: cfg.SpaceID, DeploymentID: cfg.ID}
		}
		if !inst.State.WantsRunning() {
			continue
		}
		if _, ok := knownNodes[inst.NodeID]; !ok {
			return nil, fmt.Errorf("scheduled instance %d references unknown node %d", inst.ID, inst.NodeID)
		}
		placement, err := prefix.PlacementCIDR(cfg.SpaceID, cfg.ID, inst.InstanceOrdinal, inst.ID)
		if err != nil {
			return nil, fmt.Errorf("deriving placement prefix for scheduled instance %d: %w", inst.ID, err)
		}
		if err := setRoute(placement, inst.NodeID); err != nil {
			return nil, err
		}
		key := ordinalKey{cfg.ID, inst.InstanceOrdinal}
		states := statesByOrdinal[key]
		if states == nil {
			states = &ordinalStates{}
			statesByOrdinal[key] = states
		}
		switch inst.State {
		case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY:
			states.standby = true
			continue
		case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING:
			states.draining = true
			continue
		}
		states.serving = true
		instancePrefix, err := prefix.InstanceCIDR(cfg.SpaceID, cfg.ID, inst.InstanceOrdinal)
		if err != nil {
			return nil, fmt.Errorf("deriving instance prefix for scheduled instance %d: %w", inst.ID, err)
		}
		// Two serving placements of one ordinal is a scheduler bug, not a
		// transient: the map has no way to express it and must not guess.
		if err := setRoute(instancePrefix, inst.NodeID); err != nil {
			return nil, fmt.Errorf("deployment %d ordinal %d has more than one serving placement: %w",
				cfg.ID, inst.InstanceOrdinal, err)
		}
	}
	// An ordinal stays established while a standby+draining pair exists. The
	// promotion commits atomically, so no snapshot shows that pair without a
	// serving placement; this keeps the render correct against any writer that
	// is not routed through the atomic flip.
	for key, states := range statesByOrdinal {
		if !states.serving && !(states.standby && states.draining) {
			continue
		}
		service := servicesByDeployment[key.deploymentID]
		service.Ordinals = append(service.Ordinals, &apigen.ClusterNetMapServiceOrdinal{Ordinal: key.ordinal})
	}
	dnsServices := make([]*apigen.ClusterNetMapService, 0, len(servicesByDeployment))
	for _, service := range servicesByDeployment {
		slices.SortFunc(service.Ordinals, func(a, b *apigen.ClusterNetMapServiceOrdinal) int { return cmp.Compare(a.Ordinal, b.Ordinal) })
		dnsServices = append(dnsServices, service)
	}
	slices.SortFunc(dnsServices, func(a, b *apigen.ClusterNetMapService) int {
		if c := cmp.Compare(a.SpaceID, b.SpaceID); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.DeploymentID, b.DeploymentID)
	})
	routes := make([]*apigen.ClusterNetMapRoute, 0, len(routesByPrefix))
	for destination, nodeID := range routesByPrefix {
		routes = append(routes, &apigen.ClusterNetMapRoute{LogicalPrefix: destination.String(), HostingNodeID: nodeID})
	}
	slices.SortFunc(routes, func(a, b *apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalPrefix, b.LogicalPrefix); c != 0 {
			return c
		}
		return cmp.Compare(a.HostingNodeID, b.HostingNodeID)
	})
	return &apigen.ClusterNetMap{
		UlaPrefix:   prefix.Bytes(),
		Nodes:       netNodes,
		Routes:      routes,
		PolicyRules: renderPolicyRules(inputs.Policies, inputs.DeploymentSpaces),
		DnsServices: dnsServices,
	}, nil
}

// renderPolicyRules resolves stored single-id peer anchors to wire tuples: a
// deployment peer becomes (current space, deployment id), a space peer becomes
// (space, 0). A rule referencing a deleted deployment cannot be resolved and
// is not distributed — the deleted deployment's addresses are vacant anyway.
func renderPolicyRules(policies []*apigen.NetworkPolicy, deploymentSpaces map[int32]int32) []*apigen.NetPolicyRule {
	rules := make([]*apigen.NetPolicyRule, 0, len(policies))
	for _, policy := range policies {
		if policy == nil || policy.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
			continue
		}
		source, ok := resolvePolicyPeer(policy.Source, deploymentSpaces)
		if !ok {
			continue
		}
		destination, ok := resolvePolicyPeer(policy.Destination, deploymentSpaces)
		if !ok {
			continue
		}
		ports := make([]*apigen.NetPortMatch, 0, len(policy.Ports))
		for _, port := range policy.Ports {
			if port == nil {
				continue
			}
			ports = append(ports, &apigen.NetPortMatch{Protocol: port.Protocol, Port: port.Port, PortEnd: port.PortEnd})
		}
		rules = append(rules, &apigen.NetPolicyRule{Source: source, Destination: destination, Ports: ports})
	}
	slices.SortFunc(rules, comparePolicyRules)
	return rules
}

func resolvePolicyPeer(ref *apigen.NetworkPolicyPeerRef, deploymentSpaces map[int32]int32) (*apigen.NetPolicyPeer, bool) {
	if ref == nil {
		return nil, false
	}
	switch ref.Kind {
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		return &apigen.NetPolicyPeer{SpaceID: ref.ID}, true
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		spaceID, ok := deploymentSpaces[ref.ID]
		if !ok {
			return nil, false
		}
		return &apigen.NetPolicyPeer{SpaceID: spaceID, DeploymentID: ref.ID}, true
	default:
		return nil, false
	}
}

func comparePolicyRules(a, b *apigen.NetPolicyRule) int {
	if c := comparePolicyPeers(a.Source, b.Source); c != 0 {
		return c
	}
	if c := comparePolicyPeers(a.Destination, b.Destination); c != 0 {
		return c
	}
	if c := cmp.Compare(len(a.Ports), len(b.Ports)); c != 0 {
		return c
	}
	for i := range a.Ports {
		if c := cmp.Compare(a.Ports[i].Protocol, b.Ports[i].Protocol); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Ports[i].Port, b.Ports[i].Port); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Ports[i].PortEnd, b.Ports[i].PortEnd); c != 0 {
			return c
		}
	}
	return 0
}

func comparePolicyPeers(a, b *apigen.NetPolicyPeer) int {
	if c := cmp.Compare(a.SpaceID, b.SpaceID); c != 0 {
		return c
	}
	return cmp.Compare(a.DeploymentID, b.DeploymentID)
}
