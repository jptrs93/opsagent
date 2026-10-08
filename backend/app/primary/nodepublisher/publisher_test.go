package nodepublisher

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/networkpolicies"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/scheduledinstances"
	"github.com/jptrs93/opsagent/backend/lib/acmestate"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const testWGKeyC = "Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M="

type testCluster struct {
	t      *testing.T
	store  *state.Service
	prefix network.Prefix
	nodes  []*nodes.Node
}

func newTestCluster(t *testing.T, dbPath string) *testCluster {
	t.Helper()
	store := state.Open(dbPath)
	t.Cleanup(func() { store.Close() })
	c := &testCluster{t: t, store: store, prefix: network.GeneratePrefix()}
	c.nodes = append(c.nodes, c.addPrimaryNode("primary", "primary-id", "192.0.2.10", testWGKeyA, "192.0.2.10", "2001:db8::10"))
	c.nodes = append(c.nodes, c.addPrimaryNode("second", "second-id", "192.0.2.20", testWGKeyB, "192.0.2.20"))
	return c
}

func (c *testCluster) addPrimaryNode(name, identifier, underlay, wgKey string, hostAddresses ...string) *nodes.Node {
	node := nodes.EnsurePrimaryNode(c.store, name, identifier, netip.MustParseAddr(underlay), wgKey)
	return c.report(node, underlay, wgKey, hostAddresses...)
}

func (c *testCluster) report(node *nodes.Node, underlay, wgKey string, hostAddresses ...string) *nodes.Node {
	addresses := make([]apigen.IpAddress, 0, len(hostAddresses))
	for _, a := range hostAddresses {
		addresses = append(addresses, mustAddr(a))
	}
	return nodes.ReportNode(c.store, node.Identifier, apigen.NodeReported{Identifier: node.Identifier, UnderlayAddress: mustAddr(underlay), WgPublicKey: wgKey, HostAddresses: addresses})
}

func (c *testCluster) enroll(name, identifier, underlay, wgKey string) *nodes.Node {
	c.t.Helper()
	request, seq, err := nodes.UpsertEnrollmentRequest(c.store, "192.0.2.99", "test", apigen.NodeReported{Identifier: identifier, UnderlayAddress: mustAddr(underlay), WgPublicKey: wgKey, HostAddresses: []apigen.IpAddress{mustAddr(underlay)}})
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := nodes.AcceptEnrollmentRequest(c.store, request.ID, name, identifier, seq); err != nil {
		c.t.Fatal(err)
	}
	id := erru.Must(nodes.NodeIDByIdentifier(c.store.Queries(), identifier))
	for _, node := range nodes.ListNodes(c.store.Queries()) {
		if node.ID == id {
			return node
		}
	}
	c.t.Fatalf("enrolled node %s not listed", identifier)
	return nil
}

func (c *testCluster) seq() int64 {
	return erru.Must(c.store.Queries().GetGlobalSeq(context.Background()))
}

func virtualSpec(hostname string) *apigen.DeploymentSpec {
	spec := statetest.NonEmptySpec()
	spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	if hostname != "" {
		spec.Networking.Ingress = []apigen.Ingress{{Hostname: hostname, Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{ContainerPort: 8080, PathPrefix: "/", BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1}}}}}
	}
	return spec
}

// assertCacheMatchesTables is the cache oracle: the cache folded from the
// commits must equal one seeded from the tables now.
func assertCacheMatchesTables(t *testing.T, p *Publisher, stage string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	want, seq, err := seedCache(context.Background(), p.store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	if p.appliedSeq != seq {
		t.Fatalf("%s: applied seq %d, tables at %d", stage, p.appliedSeq, seq)
	}
	wantInputs := want.inputs()
	gotInputs := p.cache.inputs()
	if !bytes.Equal(encodeInputs(wantInputs), encodeInputs(gotInputs)) {
		t.Fatalf("%s: folded cache inputs differ from the tables\n got: %s\nwant: %s", stage, describeInputs(gotInputs), describeInputs(wantInputs))
	}
	if len(p.cache.versions) != len(want.versions) {
		t.Fatalf("%s: cached versions %d, tables %d", stage, len(p.cache.versions), len(want.versions))
	}
	for key, entry := range want.versions {
		got := p.cache.versions[key]
		if got == nil || got.pins != entry.pins || !bytes.Equal(got.record.Encode(), entry.record.Encode()) {
			t.Fatalf("%s: version %+v differs: got %+v want %+v", stage, key, got, entry)
		}
	}
	if len(p.cache.watermarks) != len(want.watermarks) {
		t.Fatalf("%s: watermarks %v, tables %v", stage, p.cache.watermarks, want.watermarks)
	}
	for id, at := range want.watermarks {
		if !p.cache.watermarks[id].Equal(at) {
			t.Fatalf("%s: watermark of %d = %v, tables %v", stage, id, p.cache.watermarks[id], at)
		}
	}
}

func encodeInputs(in renderInputs) []byte {
	var buf bytes.Buffer
	for _, node := range in.nodes {
		buf.Write(node.Encode())
	}
	for i := range in.instances {
		buf.Write(in.instances[i].Encode())
	}
	for _, record := range in.deployments {
		buf.Write(record.Encode())
	}
	for _, policy := range in.policies {
		buf.Write(policy.Encode())
	}
	fmt.Fprintf(&buf, "%v", in.deploymentSpaces)
	return buf.Bytes()
}

func describeInputs(in renderInputs) string {
	var ids []string
	for _, node := range in.nodes {
		ids = append(ids, fmt.Sprintf("node %d %s", node.ID, node.Reported.WgPublicKey))
	}
	for i := range in.instances {
		ids = append(ids, fmt.Sprintf("instance %d@%d dep %d v%d %v", in.instances[i].Instance.ID, in.instances[i].Instance.NodeID, in.instances[i].Instance.Deployment.DeploymentID, in.instances[i].Instance.Deployment.Version, in.instances[i].Instance.State))
	}
	for _, record := range in.deployments {
		ids = append(ids, fmt.Sprintf("deployment %d v%d %s space %d", record.Deployment.ID, record.Meta.Version, record.Deployment.Name, record.Deployment.SpaceID))
	}
	for _, policy := range in.policies {
		ids = append(ids, fmt.Sprintf("policy %d", policy.ID))
	}
	return fmt.Sprint(ids)
}

// assertMapsMatchFullRender is the dirty-set oracle: every map held after an
// incremental render equals the map a full render over the tables produces.
func assertMapsMatchFullRender(t *testing.T, p *Publisher, stage string) map[uint64][]byte {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	tables, _, err := seedCache(context.Background(), p.store.Queries())
	if err != nil {
		t.Fatal(err)
	}
	inputs := tables.inputs()
	rc := prepareRender(p.prefix, inputs, renderIngressPlan(inputs))
	contents := make(map[uint64][]byte)
	for _, nodeID := range rc.nodeIDs {
		want := canonicalContent(rc.renderNode(nodeID))
		got := p.maps[nodeID]
		if got == nil {
			t.Fatalf("%s: node %d has no map; full render has one", stage, nodeID)
		}
		if !bytes.Equal(got.content, want) {
			t.Fatalf("%s: node %d map differs from a full render\n got: %+v\nwant: %+v", stage, nodeID, got.current, rc.renderNode(nodeID))
		}
		if got.current.DerivedFromSeq > p.appliedSeq {
			t.Fatalf("%s: node %d stamped %d past the applied seq %d", stage, nodeID, got.current.DerivedFromSeq, p.appliedSeq)
		}
		contents[nodeID] = want
	}
	for nodeID := range p.maps {
		if _, ok := contents[nodeID]; !ok {
			t.Fatalf("%s: node %d holds a map but is outside the full render", stage, nodeID)
		}
	}
	return contents
}

type shadow struct {
	instances map[uint64]apigen.NodeInstance
	netMap    *apigen.ClusterNetMap
	acme      *apigen.AcmeState
}

func (s *shadow) apply(update *apigen.NodeProjection) {
	for _, row := range update.Instances {
		if row.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			delete(s.instances, row.Instance.ID)
			continue
		}
		s.instances[row.Instance.ID] = row
	}
	if update.NetMap.Present {
		m := update.NetMap.Value
		s.netMap = &m
	}
	if update.Acme.Present {
		a := update.Acme.Value
		s.acme = &a
	}
}

func shadowOf(snapshot apigen.NodeProjection) *shadow {
	s := &shadow{instances: make(map[uint64]apigen.NodeInstance)}
	for _, row := range snapshot.Instances {
		s.instances[row.Instance.ID] = row
	}
	m := snapshot.NetMap.Value
	s.netMap = &m
	return s
}

func (s *shadow) assertEquals(t *testing.T, snapshot apigen.NodeProjection, nodeID uint64) {
	t.Helper()
	if len(s.instances) != len(snapshot.Instances) {
		t.Fatalf("node %d: folded %d instances, snapshot has %d", nodeID, len(s.instances), len(snapshot.Instances))
	}
	for _, row := range snapshot.Instances {
		got, ok := s.instances[row.Instance.ID]
		got.StatusWatermark, row.StatusWatermark = apigen.Maybe[time.Time]{}, apigen.Maybe[time.Time]{}
		if !ok || !bytes.Equal(got.Encode(), row.Encode()) {
			t.Fatalf("node %d: folded instance %d = %+v, snapshot %+v", nodeID, row.Instance.ID, got, row)
		}
	}
	if !bytes.Equal(s.netMap.Encode(), snapshot.NetMap.Value.Encode()) {
		t.Fatalf("node %d: folded map %+v, snapshot %+v", nodeID, s.netMap, snapshot.NetMap.Value)
	}
}

func mustSnapshot(t *testing.T, p *Publisher, nodeID uint64) apigen.NodeProjection {
	t.Helper()
	snapshot, err := p.SnapshotForNode(nodeID)
	if err != nil {
		t.Fatalf("snapshot of node %d: %v", nodeID, err)
	}
	return snapshot
}

func mustSubscribe(t *testing.T, p *Publisher, nodeID uint64) (apigen.NodeProjection, <-chan *apigen.NodeProjection, func()) {
	t.Helper()
	snapshot, updates, unsubscribe, err := p.Subscribe(nodeID)
	if err != nil {
		t.Fatalf("subscribe node %d: %v", nodeID, err)
	}
	return snapshot, updates, unsubscribe
}

func drainOne(t *testing.T, updates <-chan *apigen.NodeProjection, nodeID uint64, stage string) *apigen.NodeProjection {
	t.Helper()
	var got *apigen.NodeProjection
	for {
		select {
		case update, ok := <-updates:
			if !ok {
				t.Fatalf("%s: node %d queue closed", stage, nodeID)
			}
			if got != nil {
				t.Fatalf("%s: node %d received two updates for one commit: %d and %d", stage, nodeID, got.Seq, update.Seq)
			}
			got = update
		default:
			return got
		}
	}
}

func TestPublisherFoldMatchesTablesAndFullRender(t *testing.T) {
	for _, seed := range []int64{1, 7, 23, 101} {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testPublisherFoldMatchesTablesAndFullRender(t, seed)
		})
	}
}

func testPublisherFoldMatchesTablesAndFullRender(t *testing.T, seed int64) {
	c := newTestCluster(t, filepath.Join(t.TempDir(), "primary.db"))
	third := c.enroll("third", "third-id", "192.0.2.30", testWGKeyC)
	c.nodes = append(c.nodes, third)
	holder := acmestate.NewHolder()
	p, err := New(c.store, c.prefix, holder)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	assertCacheMatchesTables(t, p, "seed")
	previous := assertMapsMatchFullRender(t, p, "seed")

	type sub struct {
		updates <-chan *apigen.NodeProjection
		shadow  *shadow
	}
	subs := make(map[uint64]*sub)
	for _, node := range c.nodes {
		snapshot, updates, unsubscribe := mustSubscribe(t, p, node.ID)
		defer unsubscribe()
		if snapshot.Seq != c.seq() {
			t.Fatalf("node %d snapshot seq %d, store at %d", node.ID, snapshot.Seq, c.seq())
		}
		subs[node.ID] = &sub{updates: updates, shadow: shadowOf(snapshot)}
	}

	rng := rand.New(rand.NewSource(seed))
	type instance struct {
		id    uint64
		state apigen.ScheduledInstanceTarget
	}
	var deployments []uint64
	nextOrdinal := make(map[uint64]uint32)
	var instances []*instance
	var policies []uint64
	ctx := apigen.Context{}
	spaces := []uint64{nodes.DefaultSpaceID, 2, 3}
	pickNode := func() *nodes.Node { return c.nodes[rng.Intn(len(c.nodes))] }
	step := 0
	run := func(what string, op func()) {
		step++
		before := c.seq()
		op()
		stage := fmt.Sprintf("step %d (%s)", step, what)
		assertCacheMatchesTables(t, p, stage)
		contents := assertMapsMatchFullRender(t, p, stage)
		after := c.seq()
		for nodeID, content := range contents {
			if prev, ok := previous[nodeID]; ok && bytes.Equal(prev, content) {
				continue
			}
			p.mu.Lock()
			stamp := p.maps[nodeID].current.DerivedFromSeq
			p.mu.Unlock()
			if after == before {
				t.Fatalf("%s: node %d map changed without a commit", stage, nodeID)
			}
			if stamp != after {
				t.Fatalf("%s: node %d map changed and is stamped %d, commit was %d", stage, nodeID, stamp, after)
			}
		}
		previous = contents
		for nodeID, s := range subs {
			update := drainOne(t, s.updates, nodeID, stage)
			if update == nil {
				continue
			}
			if update.Seq != after {
				t.Fatalf("%s: node %d update seq %d, commit was %d", stage, nodeID, update.Seq, after)
			}
			s.shadow.apply(update)
		}
	}

	for i := 0; i < 160; i++ {
		switch choice := rng.Intn(14); {
		case choice <= 1 || len(deployments) == 0:
			run("create deployment", func() {
				hostname := ""
				if rng.Intn(3) == 0 {
					hostname = fmt.Sprintf("app%d.example.test", step)
				}
				record := statetest.MustCreateDeploymentForNode(c.store, ctx, spaces[rng.Intn(len(spaces))], fmt.Sprintf("d%d", step), pickNode().ID, virtualSpec(hostname))
				deployments = append(deployments, record.Deployment.ID)
			})
		case choice == 2:
			run("update spec", func() {
				id := deployments[rng.Intn(len(deployments))]
				statetest.UpdateDeploymentSpec(c.store, ctx, id, virtualSpec(fmt.Sprintf("v%d.example.test", step)))
			})
		case choice == 3:
			run("rename", func() {
				id := deployments[rng.Intn(len(deployments))]
				statetest.RenameDeployment(c.store, ctx, id, fmt.Sprintf("renamed%d", step))
			})
		case choice == 4:
			run("move space", func() {
				id := deployments[rng.Intn(len(deployments))]
				statetest.MoveDeploymentSpace(c.store, ctx, id, spaces[rng.Intn(len(spaces))])
			})
		case choice == 5 && len(deployments) > 1:
			run("delete deployment", func() {
				index := rng.Intn(len(deployments))
				statetest.DeleteDeployment(c.store, ctx, deployments[index])
				deployments = slices.Delete(deployments, index, index+1)
			})
		case choice <= 8:
			run("create instance", func() {
				id := deployments[rng.Intn(len(deployments))]
				record := erru.Must(c.store.Queries().GetLatestDeployment(context.Background(), id))
				ordinal := nextOrdinal[id]
				nextOrdinal[id]++
				target := apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING
				if rng.Intn(4) == 0 {
					target = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY
				}
				inst := statetest.CreateScheduledInstance(c.store, id, record.Meta.Version, pickNode().ID, ordinal, target)
				instances = append(instances, &instance{id: inst.ID, state: target})
			})
		case choice <= 10 && len(instances) > 0:
			run("transition instance", func() {
				index := rng.Intn(len(instances))
				inst := instances[index]
				var next apigen.ScheduledInstanceTarget
				switch inst.state {
				case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING:
					next = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING
				case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY:
					next = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
				case apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_DRAINING:
					next = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_TERMINATE
				default:
					next = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
				}
				statetest.SetScheduledInstanceState(c.store, inst.id, next)
				inst.state = next
				if next == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
					instances = slices.Delete(instances, index, index+1)
				}
			})
		case choice == 11 && len(instances) > 0:
			run("replicated status", func() {
				inst := instances[rng.Intn(len(instances))]
				scheduledinstances.WriteReplicatedStatus(c.store, &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.id, UpdatedAt: apigen.Some(time.Now())})
			})
		case choice == 12:
			run("policy", func() {
				policy := &apigen.NetworkPolicy{
					Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
					Source:      apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Space: &apigen.SpacePeer{SpaceID: spaces[rng.Intn(len(spaces))]}}}},
					Destination: apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Deployment: &apigen.DeploymentPeer{DeploymentID: deployments[rng.Intn(len(deployments))]}}}},
					Ports:       []apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Range: apigen.PortRange{Start: 8080, End: 8080}}},
				}
				switch {
				case len(policies) == 0 || rng.Intn(3) == 0:
					event, err := networkpolicies.Create(c.store, 1, policy)
					if err != nil {
						t.Fatal(err)
					}
					policies = append(policies, event.NetworkPolicyID)
				case rng.Intn(2) == 0:
					id := policies[rng.Intn(len(policies))]
					if _, err := networkpolicies.Update(c.store, id, 0, 1, policy); err != nil {
						t.Fatal(err)
					}
				default:
					index := rng.Intn(len(policies))
					if err := networkpolicies.Delete(c.store, policies[index], 1); err != nil {
						t.Fatal(err)
					}
					policies = slices.Delete(policies, index, index+1)
				}
			})
		default:
			run("report node", func() {
				node := pickNode()
				underlay := fmt.Sprintf("192.0.2.%d", 10*int(node.ID)+rng.Intn(3))
				c.report(node, underlay, node.WGPublicKey, underlay, fmt.Sprintf("2001:db8::%d", node.ID))
			})
		}
	}

	for nodeID, s := range subs {
		s.shadow.assertEquals(t, mustSnapshot(t, p, nodeID), nodeID)
	}

	run("evict", func() {
		if _, err := nodes.EvictNode(ctx, c.store, third.Identifier, 0, true); err != nil {
			t.Fatal(err)
		}
	})
	for nodeID, s := range subs {
		if nodeID == third.ID {
			continue
		}
		s.shadow.assertEquals(t, mustSnapshot(t, p, nodeID), nodeID)
	}
	if len(subs[third.ID].shadow.instances) != 0 {
		t.Fatalf("evicted node still folds instances %v", subs[third.ID].shadow.instances)
	}
	if _, err := p.SnapshotForNode(third.ID); err == nil {
		t.Fatal("evicted node still has a projection")
	}
	rows := erru.Must(c.store.Queries().ListNodeNetmaps(context.Background()))
	if len(rows) != 2 {
		t.Fatalf("stamp rows = %+v, want one per remaining member", rows)
	}
}

func TestPublisherStatusWritesProduceNoUpdate(t *testing.T) {
	c := newTestCluster(t, filepath.Join(t.TempDir(), "primary.db"))
	node := c.nodes[0]
	record := statetest.MustCreateDeploymentForNode(c.store, apigen.Context{}, nodes.DefaultSpaceID, "api", node.ID, virtualSpec(""))
	inst := statetest.CreateScheduledInstance(c.store, record.Deployment.ID, record.Meta.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	p, err := New(c.store, c.prefix, acmestate.NewHolder())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	snapshot, updates, unsubscribe := mustSubscribe(t, p, node.ID)
	defer unsubscribe()
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].StatusWatermark.Present {
		t.Fatalf("instances before any status = %+v", snapshot.Instances)
	}
	at := time.Now().Truncate(time.Millisecond)
	scheduledinstances.WriteReplicatedStatus(c.store, &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, UpdatedAt: apigen.Some(at)})
	if update := drainOne(t, updates, node.ID, "status"); update != nil {
		t.Fatalf("status write produced update %+v", update)
	}
	if p.AppliedSeq() != c.seq() {
		t.Fatalf("applied seq %d after the status write, store at %d", p.AppliedSeq(), c.seq())
	}
	fresh := mustSnapshot(t, p, node.ID)
	if len(fresh.Instances) != 1 || !fresh.Instances[0].StatusWatermark.Present || !fresh.Instances[0].StatusWatermark.Value.Equal(at) {
		t.Fatalf("instances after the status = %+v", fresh.Instances)
	}
}

func TestPublisherSnapshotThenUpdatesAreContiguous(t *testing.T) {
	c := newTestCluster(t, filepath.Join(t.TempDir(), "primary.db"))
	node := c.nodes[0]
	p, err := New(c.store, c.prefix, acmestate.NewHolder())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	snapshot, updates, unsubscribe := mustSubscribe(t, p, node.ID)
	defer unsubscribe()
	if snapshot.Seq != c.seq() || snapshot.NetMap.Value.TargetNodeID != node.ID {
		t.Fatalf("snapshot = %+v, store at %d", snapshot, c.seq())
	}
	record := statetest.MustCreateDeploymentForNode(c.store, apigen.Context{}, nodes.DefaultSpaceID, "api", node.ID, virtualSpec(""))
	if update := drainOne(t, updates, node.ID, "deployment create"); update != nil {
		t.Fatalf("a deployment without placements changed the node's projection: %+v", update)
	}
	statetest.CreateScheduledInstance(c.store, record.Deployment.ID, record.Meta.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	update := drainOne(t, updates, node.ID, "instance create")
	if update == nil || update.Seq != snapshot.Seq+2 || update.Seq != c.seq() {
		t.Fatalf("update after snapshot %d = %+v (store at %d)", snapshot.Seq, update, c.seq())
	}
	if len(update.Instances) != 1 || !update.NetMap.Present || update.NetMap.Value.DerivedFromSeq != update.Seq {
		t.Fatalf("instance commit update = %+v, want the row and the re-rendered map in one update", update)
	}
	if len(update.NetMap.Value.Routes) == 0 {
		t.Fatalf("map after scheduling carries no routes: %+v", update.NetMap.Value)
	}
}

func TestPublisherQueueOverflowDropsTheSession(t *testing.T) {
	c := newTestCluster(t, filepath.Join(t.TempDir(), "primary.db"))
	node := c.nodes[0]
	p, err := New(c.store, c.prefix, acmestate.NewHolder())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, updates, unsubscribe := mustSubscribe(t, p, node.ID)
	defer unsubscribe()
	for i := 0; i <= queueSize; i++ {
		c.report(node, fmt.Sprintf("192.0.3.%d", i%250+1), node.WGPublicKey)
	}
	received := 0
	for range updates {
		received++
	}
	if received != queueSize {
		t.Fatalf("received %d updates before the close, want the full queue of %d", received, queueSize)
	}
	fresh, again, unsubscribeAgain := mustSubscribe(t, p, node.ID)
	defer unsubscribeAgain()
	if fresh.Seq != c.seq() {
		t.Fatalf("resubscribed snapshot seq %d, store at %d", fresh.Seq, c.seq())
	}
	c.report(node, "192.0.4.1", node.WGPublicKey)
	if update := <-again; update == nil || update.Seq != c.seq() {
		t.Fatalf("update after resubscribe = %+v", update)
	}
}

func TestPublisherAdoptsPersistedStampsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	c := newTestCluster(t, dbPath)
	node := c.nodes[0]
	p, err := New(c.store, c.prefix, acmestate.NewHolder())
	if err != nil {
		t.Fatal(err)
	}
	initial := mustSnapshot(t, p, node.ID).NetMap.Value
	c.report(node, "192.0.2.11", node.WGPublicKey)
	c.report(node, "192.0.2.12", node.WGPublicKey)
	latest := mustSnapshot(t, p, node.ID).NetMap.Value
	if latest.DerivedFromSeq <= initial.DerivedFromSeq || latest.DerivedFromSeq != c.seq() {
		t.Fatalf("stamp after two reports = %d (initial %d, store %d)", latest.DerivedFromSeq, initial.DerivedFromSeq, c.seq())
	}
	rows := erru.Must(c.store.Queries().ListNodeNetmaps(context.Background()))
	if len(rows) != 2 {
		t.Fatalf("stamp rows = %+v", rows)
	}
	p.Close()
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}

	store := state.Open(dbPath)
	defer store.Close()
	restarted, err := New(store, c.prefix, acmestate.NewHolder())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got := mustSnapshot(t, restarted, node.ID).NetMap.Value
	if !bytes.Equal(canonicalContent(&got), canonicalContent(&latest)) {
		t.Fatalf("restarted map %+v, want %+v", got, latest)
	}
	if got.DerivedFromSeq != latest.DerivedFromSeq {
		t.Fatalf("restarted stamp %d, want the persisted %d", got.DerivedFromSeq, latest.DerivedFromSeq)
	}
	if other := mustSnapshot(t, restarted, c.nodes[1].ID).NetMap.Value; other.DerivedFromSeq != mustSnapshot(t, p, c.nodes[1].ID).NetMap.Value.DerivedFromSeq {
		t.Fatalf("second node restarted stamp %d changed", other.DerivedFromSeq)
	}
}

func TestPublisherScopesAcmeByNode(t *testing.T) {
	c := newTestCluster(t, filepath.Join(t.TempDir(), "primary.db"))
	hosting, other := c.nodes[0], c.nodes[1]
	record := statetest.MustCreateDeploymentForNode(c.store, apigen.Context{}, nodes.DefaultSpaceID, "web", hosting.ID, virtualSpec("app.example.test"))
	inst := statetest.CreateScheduledInstance(c.store, record.Deployment.ID, record.Meta.Version, hosting.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	holder := acmestate.NewHolder()
	p, err := New(c.store, c.prefix, holder)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, hostingUpdates, unsubscribeHosting := mustSubscribe(t, p, hosting.ID)
	defer unsubscribeHosting()
	_, otherUpdates, unsubscribeOther := mustSubscribe(t, p, other.ID)
	defer unsubscribeOther()

	app := apigen.AcmeCertBinding{Hostname: "app.example.test", Secret: apigen.SecretRef{SecretID: 1, Version: 1}}
	unrelated := apigen.AcmeCertBinding{Hostname: "unrelated.example.test", Secret: apigen.SecretRef{SecretID: 2, Version: 1}}
	p.SetAcme(&apigen.AcmeState{Seq: 1, CertBindings: []apigen.AcmeCertBinding{app, unrelated}, Challenges: []apigen.AcmeHttpChallenge{
		{Token: "a", KeyAuthorization: "a.k", Hostname: "app.example.test"},
		{Token: "u", KeyAuthorization: "u.k", Hostname: "unrelated.example.test"},
		{Token: "legacy", KeyAuthorization: "legacy.k"},
	}})

	update := <-hostingUpdates
	if !update.Acme.Present || len(update.Acme.Value.CertBindings) != 1 || update.Acme.Value.CertBindings[0].Hostname != "app.example.test" {
		t.Fatalf("hosting node ACME update = %+v", update)
	}
	tokens := make([]string, 0, 2)
	for _, challenge := range update.Acme.Value.Challenges {
		tokens = append(tokens, challenge.Token)
	}
	if !slices.Equal(tokens, []string{"a", "legacy"}) {
		t.Fatalf("hosting node challenges = %v, want its hostname plus the hostname-less one", tokens)
	}
	if update.Seq != p.AppliedSeq() || len(update.Instances) != 0 || update.NetMap.Present {
		t.Fatalf("ACME update carried more than the subset: %+v", update)
	}
	select {
	case got := <-otherUpdates:
		if got.Acme.Present && len(got.Acme.Value.CertBindings) != 0 {
			t.Fatalf("other node received bindings: %+v", got.Acme.Value)
		}
		if got.Acme.Present && len(got.Acme.Value.Challenges) != 1 {
			t.Fatalf("other node challenges = %+v, want the hostname-less one only", got.Acme.Value.Challenges)
		}
	default:
	}
	snapshot := mustSnapshot(t, p, other.ID)
	if len(snapshot.Acme.Value.CertBindings) != 0 {
		t.Fatalf("other node snapshot bindings = %+v", snapshot.Acme.Value.CertBindings)
	}
	if hostingSnapshot := mustSnapshot(t, p, hosting.ID); hostingSnapshot.Acme.Value.Seq != update.Acme.Value.Seq {
		t.Fatalf("hosting node snapshot ACME seq %d, update carried %d", hostingSnapshot.Acme.Value.Seq, update.Acme.Value.Seq)
	}

	drainOne(t, hostingUpdates, hosting.ID, "before finalize")
	drainOne(t, otherUpdates, other.ID, "before finalize")
	statetest.SetScheduledInstanceState(c.store, inst.ID, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	finalized := drainOne(t, hostingUpdates, hosting.ID, "finalize")
	if finalized == nil || len(finalized.Instances) != 1 || finalized.Acme.Present {
		t.Fatalf("finalize update = %+v, want the row alone while the deployment still publishes on the node", finalized)
	}
	statetest.DeleteDeployment(c.store, apigen.Context{}, record.Deployment.ID)
	deleted := drainOne(t, hostingUpdates, hosting.ID, "delete")
	if deleted == nil || !deleted.Acme.Present || len(deleted.Acme.Value.CertBindings) != 0 || deleted.Acme.Value.Seq <= update.Acme.Value.Seq {
		t.Fatalf("delete update = %+v, want the binding withdrawn with a newer ACME seq", deleted)
	}
}
