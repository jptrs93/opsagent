package nodepublisher

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

type renderInputs struct {
	nodes            []*apigen.Node
	instances        []apigen.ScheduledInstanceState
	deployments      []*apigen.DeploymentRecord
	policies         []*apigen.NetworkPolicy
	deploymentSpaces map[uint64]uint64
}

const (
	systemSpaceID = uint64(network.SystemSpaceID)
	globalSpaceID = uint64(network.GlobalSpaceID)
)

type placement struct {
	instanceID   uint64
	nodeID       uint64
	deploymentID uint64
	spaceID      uint64
	serving      bool
	netproxy     bool
	placement    netip.Prefix
	instance     netip.Prefix
}

type renderContext struct {
	prefix       network.Prefix
	nodes        map[uint64]apigen.ClusterNetMapNode
	nodeIDs      []uint64
	placements   []placement
	byNode       map[uint64][]int
	bySpace      map[uint64][]int
	byDeployment map[uint64][]int
	services     []apigen.ClusterNetMapService
	serviceIndex map[uint64]int
	policyRules  []apigen.NetPolicyRule
	out          map[uint64]map[uint64]struct{}
	in           map[uint64]map[uint64]struct{}
	published    map[uint64][]uint64
	diagnostics  *apigen.IngressDiagnosticList
}

func prepareRender(prefix network.Prefix, inputs renderInputs, plan ingressPlan) *renderContext {
	rc := &renderContext{
		prefix:       prefix,
		nodes:        make(map[uint64]apigen.ClusterNetMapNode, len(inputs.nodes)),
		byNode:       make(map[uint64][]int),
		bySpace:      make(map[uint64][]int),
		byDeployment: make(map[uint64][]int),
		serviceIndex: make(map[uint64]int),
		out:          make(map[uint64]map[uint64]struct{}),
		in:           make(map[uint64]map[uint64]struct{}),
		published:    plan.published,
		diagnostics:  &apigen.IngressDiagnosticList{Items: []apigen.IngressDiagnostic{}},
	}
	rc.diagnostics.Items = append(rc.diagnostics.Items, plan.diagnostics.Items...)

	for _, node := range inputs.nodes {
		underlay := ""
		if addr := node.Reported.UnderlayAddress.Addr(); addr.IsValid() {
			underlay = addr.Unmap().String()
		}
		rc.nodes[node.ID] = apigen.ClusterNetMapNode{NodeID: node.ID, UnderlayAddress: underlay, WgPublicKey: node.Reported.WgPublicKey, WgListenPort: uint32(network.DefaultWGListenPort), IngressPublish: plan.publish[node.ID]}
		rc.nodeIDs = append(rc.nodeIDs, node.ID)
	}

	type ordinalKey struct {
		deploymentID uint64
		ordinal      uint32
	}
	type ordinalStates struct{ serving, standby, draining bool }
	statesByOrdinal := make(map[ordinalKey]*ordinalStates)
	for _, item := range inputs.instances {
		cfg := item.Config
		inst := item.Instance
		deploymentID := cfg.Deployment.ID
		if cfg.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		if _, ok := rc.serviceIndex[deploymentID]; !ok {
			rc.serviceIndex[deploymentID] = len(rc.services)
			rc.services = append(rc.services, apigen.ClusterNetMapService{Name: network.DNSLabel(cfg.Deployment.Name), SpaceID: cfg.Deployment.SpaceID, DeploymentID: deploymentID})
		}
		if !inst.State.WantsRunning() {
			continue
		}
		placementPrefix, err := prefix.PlacementCIDR(int32(cfg.Deployment.SpaceID), int32(deploymentID), int32(inst.InstanceOrdinal), int32(inst.ID))
		if err != nil {
			panic(fmt.Sprintf("deriving placement prefix for scheduled instance %d: %v", inst.ID, err))
		}
		p := placement{
			instanceID:   inst.ID,
			nodeID:       inst.NodeID,
			deploymentID: deploymentID,
			spaceID:      cfg.Deployment.SpaceID,
			netproxy:     internaldeploy.IsNetproxyIdentity(cfg.Deployment.SpaceID, cfg.Deployment.Name),
			placement:    placementPrefix,
		}
		key := ordinalKey{deploymentID, inst.InstanceOrdinal}
		states := statesByOrdinal[key]
		if states == nil {
			states = &ordinalStates{}
			statesByOrdinal[key] = states
		}
		switch inst.State {
		case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY:
			states.standby = true
		case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING:
			states.draining = true
		default:
			states.serving = true
			instancePrefix, err := prefix.InstanceCIDR(int32(cfg.Deployment.SpaceID), int32(deploymentID), int32(inst.InstanceOrdinal))
			if err != nil {
				panic(fmt.Sprintf("deriving instance prefix for scheduled instance %d: %v", inst.ID, err))
			}
			p.serving = true
			p.instance = instancePrefix
		}
		index := len(rc.placements)
		rc.placements = append(rc.placements, p)
		rc.byNode[p.nodeID] = append(rc.byNode[p.nodeID], index)
		rc.bySpace[p.spaceID] = append(rc.bySpace[p.spaceID], index)
		rc.byDeployment[p.deploymentID] = append(rc.byDeployment[p.deploymentID], index)
	}
	// An ordinal stays established while a standby+draining pair exists. The
	// promotion commits atomically, so no snapshot shows that pair without a
	// serving placement; this keeps the render correct against any writer that
	// is not routed through the atomic flip.
	for key, states := range statesByOrdinal {
		if !states.serving && !(states.standby && states.draining) {
			continue
		}
		service := &rc.services[rc.serviceIndex[key.deploymentID]]
		service.Ordinals = append(service.Ordinals, apigen.ClusterNetMapServiceOrdinal{Ordinal: key.ordinal})
	}
	for i := range rc.services {
		slices.SortFunc(rc.services[i].Ordinals, func(a, b apigen.ClusterNetMapServiceOrdinal) int { return cmp.Compare(a.Ordinal, b.Ordinal) })
	}

	rc.policyRules = renderPolicyRules(inputs.policies, inputs.deploymentSpaces)
	for _, rule := range rc.policyRules {
		addEdge(rc.out, rule.Source.SpaceID, rule.Destination.SpaceID)
		addEdge(rc.in, rule.Destination.SpaceID, rule.Source.SpaceID)
	}
	return rc
}

func addEdge(adjacency map[uint64]map[uint64]struct{}, from, to uint64) {
	if adjacency[from] == nil {
		adjacency[from] = make(map[uint64]struct{})
	}
	adjacency[from][to] = struct{}{}
}

func (rc *renderContext) renderNode(nodeID uint64) *apigen.ClusterNetMap {
	scope := rc.scope(nodeID)
	selected := make([]int, 0, len(scope.placements))
	for index := range scope.placements {
		selected = append(selected, index)
	}
	peers := map[uint64]struct{}{nodeID: {}}
	for _, index := range selected {
		peers[rc.placements[index].nodeID] = struct{}{}
	}
	return rc.assemble(nodeID, selected, peers, scope.catalog, scope.spaces)
}

func (rc *renderContext) renderGlobal() *apigen.ClusterNetMap {
	selected := make([]int, len(rc.placements))
	peers := make(map[uint64]struct{}, len(rc.nodes))
	for i := range rc.placements {
		selected[i] = i
	}
	for id := range rc.nodes {
		peers[id] = struct{}{}
	}
	return rc.assemble(0, selected, peers, nil, nil)
}

func (rc *renderContext) assemble(targetNodeID uint64, selected []int, peers map[uint64]struct{}, catalog map[uint64]struct{}, spaces map[uint64]struct{}) *apigen.ClusterNetMap {
	netNodes := make([]apigen.ClusterNetMapNode, 0, len(peers))
	for id := range peers {
		node := rc.nodes[id]
		node.IngressPublish = slices.Clone(node.IngressPublish)
		netNodes = append(netNodes, node)
	}
	slices.SortFunc(netNodes, func(a, b apigen.ClusterNetMapNode) int { return cmp.Compare(a.NodeID, b.NodeID) })

	routes := make([]apigen.ClusterNetMapRoute, 0, len(selected)*2)
	for _, index := range selected {
		p := &rc.placements[index]
		routes = append(routes, apigen.ClusterNetMapRoute{LogicalPrefix: p.placement.String(), HostingNodeID: p.nodeID})
		if p.serving {
			routes = append(routes, apigen.ClusterNetMapRoute{LogicalPrefix: p.instance.String(), HostingNodeID: p.nodeID})
		}
	}
	slices.SortFunc(routes, func(a, b apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalPrefix, b.LogicalPrefix); c != 0 {
			return c
		}
		return cmp.Compare(a.HostingNodeID, b.HostingNodeID)
	})

	services := make([]apigen.ClusterNetMapService, 0, len(rc.services))
	for i := range rc.services {
		if catalog != nil {
			if _, ok := catalog[rc.services[i].DeploymentID]; !ok {
				continue
			}
		}
		service := rc.services[i]
		service.Ordinals = slices.Clone(service.Ordinals)
		services = append(services, service)
	}
	slices.SortFunc(services, func(a, b apigen.ClusterNetMapService) int {
		if c := cmp.Compare(a.SpaceID, b.SpaceID); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.DeploymentID, b.DeploymentID)
	})

	rules := make([]apigen.NetPolicyRule, 0, len(rc.policyRules))
	for _, rule := range rc.policyRules {
		if spaces != nil {
			_, source := spaces[rule.Source.SpaceID]
			_, destination := spaces[rule.Destination.SpaceID]
			if !source && !destination {
				continue
			}
		}
		rule.Ports = slices.Clone(rule.Ports)
		rules = append(rules, rule)
	}

	return &apigen.ClusterNetMap{
		TargetNodeID: targetNodeID,
		UlaPrefix:    rc.prefix.Bytes(),
		Nodes:        netNodes,
		Routes:       routes,
		PolicyRules:  rules,
		DnsServices:  services,
	}
}

func render(prefix network.Prefix, inputs renderInputs) (*apigen.ClusterNetMap, *apigen.IngressDiagnosticList) {
	rc := prepareRender(prefix, inputs, renderIngressPlan(inputs))
	return rc.renderGlobal(), rc.diagnostics
}

type ingressPlan struct {
	publish     map[uint64][]apigen.IngressPublish
	published   map[uint64][]uint64
	diagnostics *apigen.IngressDiagnosticList
}

// renderIngressPlan expands every deployment's listen selectors over the node
// inventory. Collisions were refused at write time, so what remains here is
// expansion plus the legacy fallback for claims stored before set semantics,
// reported as diagnostics.
func renderIngressPlan(inputs renderInputs) ingressPlan {
	in := ingressplan.Inputs{}
	for _, node := range inputs.nodes {
		in.Nodes = append(in.Nodes, ingressplan.Node{ID: node.ID, HostAddresses: ingressplan.HostAddresses(node.Reported.HostAddresses)})
	}
	for _, cfg := range inputs.deployments {
		if internaldeploy.IsInternalConfig(cfg) {
			continue
		}
		in.Deployments = append(in.Deployments, ingressplan.DeploymentFromSpec(cfg.Deployment.ID, cfg.Deployment.PlacementNodeID(), cfg.Deployment.Name, &cfg.Deployment.Spec))
	}
	result := ingressplan.Evaluate(in)
	plan := ingressPlan{publish: make(map[uint64][]apigen.IngressPublish, len(result.Publish)), published: result.PublishedDeployments, diagnostics: &apigen.IngressDiagnosticList{Items: []apigen.IngressDiagnostic{}}}
	for nodeID, entries := range result.Publish {
		for _, entry := range entries {
			address := ""
			if entry.Address.IsValid() {
				address = entry.Address.String()
			}
			plan.publish[nodeID] = append(plan.publish[nodeID], apigen.IngressPublish{Address: address, Port: entry.Port})
		}
	}
	diagnostics := append(result.Diagnostics(), result.Errors...)
	slices.SortStableFunc(diagnostics, func(a, b ingressplan.Diagnostic) int {
		if c := cmp.Compare(a.DeploymentID, b.DeploymentID); c != 0 {
			return c
		}
		return strings.Compare(a.Message, b.Message)
	})
	for _, diag := range diagnostics {
		plan.diagnostics.Items = append(plan.diagnostics.Items, apigen.IngressDiagnostic{DeploymentID: diag.DeploymentID, Message: diag.Message})
	}
	return plan
}

func canonicalContent(source *apigen.ClusterNetMap) []byte {
	canonical := *source
	canonical.DerivedFromSeq = 0
	return canonical.Encode()
}

// renderPolicyRules resolves stored single-id peer anchors to wire tuples: a
// deployment peer becomes (current space, deployment id), a space peer becomes
// (space, 0). A rule referencing a deleted deployment cannot be resolved and
// is not distributed — the deleted deployment's addresses are vacant anyway.
func renderPolicyRules(policies []*apigen.NetworkPolicy, deploymentSpaces map[uint64]uint64) []apigen.NetPolicyRule {
	rules := make([]apigen.NetPolicyRule, 0, len(policies))
	for _, policy := range policies {
		if policy.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
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
		ports := make([]apigen.NetPortMatch, 0, len(policy.Ports))
		for _, port := range policy.Ports {
			ports = append(ports, apigen.NetPortMatch{Protocol: port.Protocol, Range: port.Range})
		}
		rules = append(rules, apigen.NetPolicyRule{Source: source, Destination: destination, Ports: ports})
	}
	slices.SortFunc(rules, comparePolicyRules)
	return rules
}

func resolvePolicyPeer(peer apigen.NetworkPolicyPeer, deploymentSpaces map[uint64]uint64) (apigen.NetPolicyPeer, bool) {
	target := peer.Target.Value
	if target.Space != nil {
		return apigen.NetPolicyPeer{SpaceID: target.Space.SpaceID}, true
	}
	spaceID, ok := deploymentSpaces[target.Deployment.DeploymentID]
	if !ok {
		return apigen.NetPolicyPeer{}, false
	}
	return apigen.NetPolicyPeer{SpaceID: spaceID, DeploymentID: target.Deployment.DeploymentID}, true
}

func comparePolicyRules(a, b apigen.NetPolicyRule) int {
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
		if c := cmp.Compare(a.Ports[i].Range.Start, b.Ports[i].Range.Start); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Ports[i].Range.End, b.Ports[i].Range.End); c != 0 {
			return c
		}
	}
	return 0
}

func comparePolicyPeers(a, b apigen.NetPolicyPeer) int {
	if c := cmp.Compare(a.SpaceID, b.SpaceID); c != 0 {
		return c
	}
	return cmp.Compare(a.DeploymentID, b.DeploymentID)
}
