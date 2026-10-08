package nodepublisher

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func allowPolicy(id uint64, source, destination apigen.NetworkPolicyPeer) *apigen.NetworkPolicy {
	return &apigen.NetworkPolicy{ID: id, Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW, Source: source, Destination: destination}
}

func spacePeer(spaceID uint64) apigen.NetworkPolicyPeer {
	return apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Space: &apigen.SpacePeer{SpaceID: spaceID}}}}
}

func deploymentPeer(deploymentID uint64) apigen.NetworkPolicyPeer {
	return apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Deployment: &apigen.DeploymentPeer{DeploymentID: deploymentID}}}}
}

func netproxyInstance(instanceID, deploymentID, nodeID uint64) apigen.ScheduledInstanceState {
	item := servingInstance(instanceID, deploymentID, nodeID, uint64(network.SystemSpaceID))
	item.Config.Deployment.Name = internaldeploy.NetproxyName
	item.Instance.InstanceOrdinal = uint32(nodeID)
	return item
}

func routeSet(m *apigen.ClusterNetMap) map[string]uint64 {
	out := make(map[string]uint64, len(m.Routes))
	for _, route := range m.Routes {
		out[route.LogicalPrefix] = route.HostingNodeID
	}
	return out
}

func nodeSet(m *apigen.ClusterNetMap) map[uint64]struct{} {
	out := make(map[uint64]struct{}, len(m.Nodes))
	for _, node := range m.Nodes {
		out[node.NodeID] = struct{}{}
	}
	return out
}

func catalogSet(m *apigen.ClusterNetMap) map[uint64]struct{} {
	out := make(map[uint64]struct{}, len(m.DnsServices))
	for _, service := range m.DnsServices {
		out[service.DeploymentID] = struct{}{}
	}
	return out
}

// TestScopePartitionedSpacesStayLocal is the headline property: a node whose
// placements share no space, policy, or ingress with another node receives
// nothing about it.
func TestScopePartitionedSpacesStayLocal(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA), testNode(2, "192.0.2.2", testWGKeyB), testNode(3, "192.0.2.3", testWGKeyA)}
	instances := []apigen.ScheduledInstanceState{
		netproxyInstance(1, 1, 1), netproxyInstance(2, 1, 2), netproxyInstance(3, 1, 3),
		servingInstance(100, 10, 1, 3), servingInstance(101, 11, 1, 3),
		servingInstance(200, 20, 2, 4),
		servingInstance(300, 30, 3, 4),
	}
	rc := prepareRender(prefix, renderInputs{nodes: nodeList, instances: instances}, ingressPlan{diagnostics: &apigen.IngressDiagnosticList{}})
	one := rc.renderNode(1)
	if got := nodeSet(one); len(got) != 1 {
		t.Fatalf("node 1 peers = %v, want only itself", got)
	}
	if got := routeSet(one); len(got) != 6 {
		t.Fatalf("node 1 routes = %v, want its own three placements only", got)
	}
	if got := catalogSet(one); len(got) != 2 {
		t.Fatalf("node 1 catalog = %v, want deployments 10 and 11", got)
	}
	two := rc.renderNode(2)
	if got := nodeSet(two); len(got) != 2 {
		t.Fatalf("node 2 peers = %v, want itself and node 3 (shared space 4)", got)
	}
	if _, ok := routeSet(two)[rc.placements[rc.byDeployment[30][0]].instance.String()]; !ok {
		t.Fatalf("node 2 routes = %v, want deployment 30 on node 3", routeSet(two))
	}
	if _, ok := routeSet(two)[rc.placements[rc.byDeployment[1][2]].placement.String()]; ok {
		t.Fatal("node 2 must not route node 3's netproxy: space 0 is never shared")
	}
	if one.TargetNodeID != 1 || two.TargetNodeID != 2 {
		t.Fatalf("targets = %d, %d", one.TargetNodeID, two.TargetNodeID)
	}
}

func TestScopeGlobalSpaceAndPolicies(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA), testNode(2, "192.0.2.2", testWGKeyB), testNode(3, "192.0.2.3", testWGKeyA)}
	instances := []apigen.ScheduledInstanceState{
		servingInstance(100, 10, 1, 3),
		servingInstance(200, 20, 2, 4),
		servingInstance(300, 30, 3, uint64(network.GlobalSpaceID)),
	}
	policies := []*apigen.NetworkPolicy{allowPolicy(1, spacePeer(3), deploymentPeer(20))}
	rc := prepareRender(prefix, renderInputs{nodes: nodeList, instances: instances, policies: policies, deploymentSpaces: map[uint64]uint64{10: 3, 20: 4, 30: 1}}, ingressPlan{diagnostics: &apigen.IngressDiagnosticList{}})
	one, two, three := rc.renderNode(1), rc.renderNode(2), rc.renderNode(3)
	if got := nodeSet(one); len(got) != 3 {
		t.Fatalf("node 1 peers = %v, want node 2 through the policy and node 3 through the global space", got)
	}
	if got := catalogSet(one); len(got) != 3 {
		t.Fatalf("node 1 catalog = %v, want its own, the policy destination and the global deployment", got)
	}
	if got := nodeSet(two); len(got) != 3 {
		t.Fatalf("node 2 peers = %v, want node 1 for replies and node 3 for the global space", got)
	}
	if got := catalogSet(two); len(got) != 2 {
		t.Fatalf("node 2 catalog = %v, want its own and the global deployment, not the policy source", got)
	}
	if got := nodeSet(three); len(got) != 3 {
		t.Fatalf("node 3 peers = %v, want every node: it hosts the global space", got)
	}
	if got := catalogSet(three); len(got) != 1 {
		t.Fatalf("node 3 catalog = %v, want only its own deployment", got)
	}
	if len(one.PolicyRules) != 1 || len(two.PolicyRules) != 1 || len(three.PolicyRules) != 0 {
		t.Fatalf("policy rules = %d, %d, %d; want the rule on both ends and nowhere else", len(one.PolicyRules), len(two.PolicyRules), len(three.PolicyRules))
	}
}

func TestScopeIngressPublisherReachesBackends(t *testing.T) {
	prefix := network.GeneratePrefix()
	nodeList := []*apigen.Node{testNode(1, "192.0.2.1", testWGKeyA), testNode(2, "192.0.2.2", testWGKeyB), testNode(3, "192.0.2.3", testWGKeyA)}
	instances := []apigen.ScheduledInstanceState{
		netproxyInstance(1, 1, 1), netproxyInstance(2, 1, 2), netproxyInstance(3, 1, 3),
		servingInstance(200, 20, 2, 4),
		servingInstance(300, 30, 3, 5),
	}
	rc := prepareRender(prefix, renderInputs{nodes: nodeList, instances: instances}, ingressPlan{diagnostics: &apigen.IngressDiagnosticList{}})
	rc.published = map[uint64][]uint64{1: {20}}
	one, two, three := rc.renderNode(1), rc.renderNode(2), rc.renderNode(3)
	if got := nodeSet(one); len(got) != 2 {
		t.Fatalf("node 1 peers = %v, want node 2 for the published backend only", got)
	}
	if _, ok := catalogSet(one)[20]; !ok {
		t.Fatalf("node 1 catalog = %v, want the published backend resolvable", catalogSet(one))
	}
	if _, ok := routeSet(two)[rc.placements[rc.byDeployment[1][0]].placement.String()]; !ok {
		t.Fatalf("node 2 routes = %v, want node 1's netproxy for replies", routeSet(two))
	}
	if _, ok := routeSet(two)[rc.placements[rc.byDeployment[1][2]].placement.String()]; ok {
		t.Fatal("node 2 must not route node 3's netproxy: it publishes nothing on node 2")
	}
	if got := nodeSet(three); len(got) != 1 {
		t.Fatalf("node 3 peers = %v, want itself only", got)
	}
}

type modelPlacement struct {
	nodeID, deploymentID, spaceID uint64
	netproxy                      bool
}

// TestScopeClosureProperty checks the per-node render against a brute-force
// may-initiate relation on random topologies: both ends of every allowed
// connection carry each other's routes and peer entries, every per-node map is
// a subset of the global map, and every forward-reachable deployment resolves.
func TestScopeClosureProperty(t *testing.T) {
	prefix := network.GeneratePrefix()
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 150; round++ {
		nodeCount := 2 + rng.Intn(5)
		nodeList := make([]*apigen.Node, 0, nodeCount)
		for i := 1; i <= nodeCount; i++ {
			nodeList = append(nodeList, testNode(uint64(i), fmt.Sprintf("192.0.2.%d", i), testWGKeyA))
		}
		spaceCount := 1 + rng.Intn(4)
		var instances []apigen.ScheduledInstanceState
		var model []modelPlacement
		deploymentSpaces := make(map[uint64]uint64)
		for i := 1; i <= nodeCount; i++ {
			if rng.Intn(4) > 0 {
				instances = append(instances, netproxyInstance(uint64(i), 1, uint64(i)))
				model = append(model, modelPlacement{nodeID: uint64(i), deploymentID: 1, spaceID: 0, netproxy: true})
			}
		}
		deploymentID := uint64(10)
		instanceID := uint64(100)
		for n := 0; n < 1+rng.Intn(6); n++ {
			nodeID := uint64(1 + rng.Intn(nodeCount))
			spaceID := uint64(1 + rng.Intn(spaceCount+1))
			item := servingInstance(instanceID, deploymentID, nodeID, spaceID)
			switch rng.Intn(4) {
			case 0:
				item.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
			case 1:
				item.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
			}
			instances = append(instances, item)
			model = append(model, modelPlacement{nodeID: nodeID, deploymentID: deploymentID, spaceID: spaceID})
			deploymentSpaces[deploymentID] = spaceID
			deploymentID++
			instanceID++
		}
		var policies []*apigen.NetworkPolicy
		out := make(map[uint64]map[uint64]struct{})
		for n := 0; n < rng.Intn(4); n++ {
			source := uint64(1 + rng.Intn(spaceCount+1))
			destination := uint64(1 + rng.Intn(spaceCount+1))
			policies = append(policies, allowPolicy(uint64(n+1), spacePeer(source), spacePeer(destination)))
			addEdge(out, source, destination)
		}
		published := make(map[uint64][]uint64)
		for i := 1; i <= nodeCount; i++ {
			for id := range deploymentSpaces {
				if rng.Intn(6) == 0 {
					published[uint64(i)] = append(published[uint64(i)], id)
				}
			}
		}

		rc := prepareRender(prefix, renderInputs{nodes: nodeList, instances: instances, policies: policies, deploymentSpaces: deploymentSpaces}, ingressPlan{diagnostics: &apigen.IngressDiagnosticList{}})
		rc.published = published
		global := rc.renderGlobal()
		globalRoutes := routeSet(global)
		globalCatalog := catalogSet(global)
		maps := make(map[uint64]*apigen.ClusterNetMap)
		for i := 1; i <= nodeCount; i++ {
			maps[uint64(i)] = rc.renderNode(uint64(i))
			if maps[uint64(i)] == nil {
				t.Fatalf("round %d: node %d has no map", round, i)
			}
			for route, host := range routeSet(maps[uint64(i)]) {
				if globalRoutes[route] != host {
					t.Fatalf("round %d: node %d carries route %s not in the global map", round, i, route)
				}
			}
			for id := range catalogSet(maps[uint64(i)]) {
				if _, ok := globalCatalog[id]; !ok {
					t.Fatalf("round %d: node %d resolves deployment %d not in the global catalog", round, i, id)
				}
			}
		}

		mayInitiate := func(a, b modelPlacement) bool {
			if a.netproxy {
				return slices.Contains(published[a.nodeID], b.deploymentID)
			}
			if b.spaceID == 0 {
				return false
			}
			if a.spaceID == b.spaceID || b.spaceID == globalSpaceID {
				return true
			}
			_, ok := out[a.spaceID][b.spaceID]
			return ok
		}
		for ai, a := range model {
			for bi, b := range model {
				if !mayInitiate(a, b) {
					continue
				}
				pa, pb := rc.placements[ai], rc.placements[bi]
				for _, side := range []struct {
					from uint64
					want placement
				}{{a.nodeID, pb}, {b.nodeID, pa}} {
					m := maps[side.from]
					routes := routeSet(m)
					if routes[side.want.placement.String()] != side.want.nodeID {
						t.Fatalf("round %d: node %d lacks placement route %s for %+v -> %+v", round, side.from, side.want.placement, a, b)
					}
					if side.want.serving && routes[side.want.instance.String()] != side.want.nodeID {
						t.Fatalf("round %d: node %d lacks instance route %s", round, side.from, side.want.instance)
					}
					if _, ok := nodeSet(m)[side.want.nodeID]; !ok {
						t.Fatalf("round %d: node %d lacks peer %d", round, side.from, side.want.nodeID)
					}
				}
				if _, ok := catalogSet(maps[a.nodeID])[b.deploymentID]; !ok {
					t.Fatalf("round %d: node %d cannot resolve deployment %d it may initiate toward", round, a.nodeID, b.deploymentID)
				}
			}
		}
	}
}
