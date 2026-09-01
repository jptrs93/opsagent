package clusterhandler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestBuildAllowedRefs(t *testing.T) {
	secretID := int32(7)
	configID := int32(9)
	refs := buildAllowedRefs([]apigen.ScheduledInstanceState{{
		Instance: apigen.ScheduledInstance{ID: 99},
		Config: apigen.Deployment{
			ID: 42,
			Spec: apigen.DeploymentSpec{
				Container1Spec: &apigen.ContainerSpec{
					Source: apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild{Repo: "github.com/acme/app", Flake: "flake.nix"}},
					Runtime: apigen.ContainerRuntime{
						EnvVars: map[string]*apigen.EnvVarValue{
							"SECRET": {SecretVersionID: &secretID},
							"CONFIG": {ConfigVersionID: &configID},
							"ASSET":  {Asset: "app.env", AssetVersionID: 3},
						},
						AssetMounts: []*apigen.AssetMount{{AssetVersionID: 4, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_READ_ONLY}},
					},
				},
			},
		},
	}})

	if !refs.scheduledInstanceAllowed(99) {
		t.Fatal("scheduled instance id should be allowed")
	}
	if !refs.deploymentAllowed(42) {
		t.Fatal("deployment id should be allowed")
	}
	if !refs.allSecretsAllowed([]int32{7}) || refs.allSecretsAllowed([]int32{8}) {
		t.Fatal("secret refs not scoped correctly")
	}
	if !refs.allConfigsAllowed([]int32{9}) || refs.allConfigsAllowed([]int32{10}) {
		t.Fatal("config refs not scoped correctly")
	}
	if !refs.assetAllowed(3) || !refs.assetAllowed(4) || refs.assetAllowed(5) {
		t.Fatal("asset refs not scoped correctly")
	}
	if !refs.usesGithub {
		t.Fatal("GitHub credentials should be allowed for GitHub-backed deployments")
	}
}

func TestSessionRejectsCrossMachineStatusWrite(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	m1Node := store.EnsurePrimaryNode("m1", "m1")
	m2Node := store.EnsurePrimaryNode("m2", "m2")
	spec := &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "docker.io/library/nginx"}},
			Version: "1",
			Running: true,
		},
	}
	m1 := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", m1Node.ID, spec)
	m2 := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", m2Node.ID, spec)
	m1Inst := store.CreateScheduledInstanceForTest(m1.ID, m1.SpecVersion, m1Node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	m2Inst := store.CreateScheduledInstanceForTest(m2.ID, m2.SpecVersion, m2Node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	sess := newSession(context.Background(), func() {}, m1Node.ID, "m1", scheduledInstancePredicateForNode(m1Node.ID), store, nil)
	crossMachine := &apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: m2Inst.ID,
		DeploymentID:        m2.ID,
		Runner:              apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING},
	}
	crossMachine.BumpUpdatedAt()
	sess.handleStatusWrite(crossMachine)
	if got := store.FetchScheduledInstanceStatus(m2Inst.ID); got != nil && got.Runner.Status == apigen.RunningStatus_RUNNING {
		t.Fatal("cross-machine status write was accepted")
	}

	sameMachine := &apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: m1Inst.ID,
		DeploymentID:        m1.ID,
		Runner:              apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING},
	}
	sameMachine.BumpUpdatedAt()
	sess.handleStatusWrite(sameMachine)
	if got := store.FetchScheduledInstanceStatus(m1Inst.ID); got == nil || got.Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("same-machine status write was rejected; status = %v", got)
	}
}

func TestSessionRoutingUsesNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("secondary", "secondary-cn")
	handler := New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil)
	sess := newSession(context.Background(), func() {}, node.ID, "secondary-cn", scheduledInstancePredicateForNode(node.ID), store, nil)
	handler.registerSession(node.ID, "secondary-cn", sess)
	t.Cleanup(func() { handler.unregisterSession(node.ID, "secondary-cn", sess) })

	if sess.NodeID != node.ID {
		t.Fatalf("session node ID = %d, want %d", sess.NodeID, node.ID)
	}
	if _, ok := handler.ConnectedNodes()[node.ID]; !ok {
		t.Fatalf("node %d was not recorded as connected", node.ID)
	}

	req := &apigen.MsgToSecondary{DeploymentLogRequest: &apigen.DeploymentLogRequest{}}
	reader, err := handler.RequestLogs(node.ID, req)
	if err != nil {
		t.Fatalf("request logs: %v", err)
	}
	defer reader.Close()
	if !strings.HasPrefix(req.DeploymentLogRequest.RequestID, "secondary-cn-") {
		t.Fatalf("request ID = %q, want secondary CN prefix", req.DeploymentLogRequest.RequestID)
	}

	queryReq := &apigen.LogQueryRequest{DeploymentID: 7}
	type queryResult struct {
		resp *apigen.LogQueryResponse
		err  error
	}
	resCh := make(chan queryResult, 1)
	go func() {
		resp, err := handler.RequestLogQuery(context.Background(), node.ID, queryReq)
		resCh <- queryResult{resp, err}
	}()
	// Drain frames the way the session send-loop would until the query frame
	// appears, then reply as the secondary.
	var frame *apigen.MsgToSecondary
	for frame = <-sess.outbox; frame.LogQueryRequest == nil; frame = <-sess.outbox {
	}
	if !strings.HasPrefix(frame.LogQueryRequest.RequestID, "secondary-cn-") {
		t.Fatalf("log query frame = %+v, want secondary CN request ID", frame)
	}
	sess.handleIncoming(&apigen.MsgToPrimary{
		LogQueryResponse: &apigen.LogQueryResponse{Stats: &apigen.LogQueryStats{MatchedRows: 3}},
		LogRequestID:     frame.LogQueryRequest.RequestID,
	})
	res := <-resCh
	if res.err != nil || res.resp == nil || res.resp.Stats.MatchedRows != 3 {
		t.Fatalf("log query result = %+v, err = %v", res.resp, res.err)
	}

	_, err = handler.RequestLogs(node.ID+1, &apigen.MsgToSecondary{DeploymentLogRequest: &apigen.DeploymentLogRequest{}})
	var notConnected *NodeNotConnectedError
	if !errors.As(err, &notConnected) || notConnected.NodeID != node.ID+1 {
		t.Fatalf("missing node error = %v, want node ID %d", err, node.ID+1)
	}
}

func TestSessionClusterHelloUpdatesAuthenticatedNodeUnderlay(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.MustSetNodeAddresses(primary.ID, []string{"192.0.2.1"})
	secondary := store.EnsurePrimaryNode("secondary", "secondary-cn")
	store.MustSetNodeAddresses(secondary.ID, []string{"192.0.2.2"})
	sess := newSession(context.Background(), func() {}, secondary.ID, secondary.Identifier, scheduledInstancePredicateForNode(secondary.ID), store, nil)

	const helloWGKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: &apigen.ClusterHello{UnderlayAddress: " 192.0.2.3 ", WgPublicKey: helloWGKey, ClusterProtocolVersion: apigen.ClusterProtocolVersion}})
	if got := nodeAddresses(t, store, secondary.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("secondary addresses = %v, want [192.0.2.3]", got)
	}
	if got := nodeAddresses(t, store, primary.ID); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("primary addresses = %v, want [192.0.2.1]", got)
	}

	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: &apigen.ClusterHello{UnderlayAddress: "2001:db8::3", WgPublicKey: helloWGKey, ClusterProtocolVersion: apigen.ClusterProtocolVersion}})
	if got := nodeAddresses(t, store, secondary.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("invalid hello changed secondary addresses to %v", got)
	}
}

func TestSessionRejectsClusterProtocolMismatch(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	secondary := store.EnsurePrimaryNode("secondary", "secondary-cn")
	cancelled := false
	sess := newSession(context.Background(), func() { cancelled = true }, secondary.ID, secondary.Identifier, scheduledInstancePredicateForNode(secondary.ID), store, nil)

	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: &apigen.ClusterHello{ClusterProtocolVersion: apigen.ClusterProtocolVersion - 1}})
	if !cancelled {
		t.Fatal("cluster protocol mismatch did not cancel session")
	}
}

func nodeAddresses(t *testing.T, store *state.Service, nodeID int32) []string {
	t.Helper()
	for _, node := range store.ListNodes() {
		if node.ID == nodeID {
			return node.Addresses
		}
	}
	t.Fatalf("node %d not found", nodeID)
	return nil
}
