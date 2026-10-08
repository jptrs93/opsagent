package clusterhandler

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestBuildAllowedRefs(t *testing.T) {
	secret := apigen.ValueRef{ID: 7, Version: 2}
	config := apigen.ValueRef{ID: 9, Version: 1}
	refs := buildAllowedRefs([]apigen.ScheduledInstanceState{{
		Instance: apigen.ScheduledInstance{ID: 99},
		Config: apigen.DeploymentRecord{
			Deployment: apigen.Deployment{ID: 42, Spec: apigen.DeploymentSpec{Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
				Source: apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{Repo: "github.com/acme/app", Flake: "flake.nix"}}},
				Runtime: apigen.ContainerRuntime{EnvVars: map[string]apigen.EnvVar{
					"SECRET": {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: secret.Secret()}}},
					"CONFIG": {Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: config.Config()}}},
					"ASSET":  {Value: apigen.EnvVarValueOneof{Asset: &apigen.AssetEnv{Key: "app.env", Asset: apigen.AssetRef{AssetID: 3, Version: 1}}}},
				}, AssetMounts: []apigen.AssetMount{{Asset: apigen.AssetRef{AssetID: 4, Version: 5}, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY}}},
			}}}}},
		},
	}})

	if !refs.scheduledInstanceAllowed(99) {
		t.Fatal("scheduled instance id should be allowed")
	}
	if !refs.deploymentAllowed(42) {
		t.Fatal("deployment id should be allowed")
	}
	if !refs.allSecretsAllowed([]apigen.ValueRef{secret}) || refs.allSecretsAllowed([]apigen.ValueRef{{ID: 7, Version: 1}}) || refs.allSecretsAllowed([]apigen.ValueRef{{ID: 8, Version: 2}}) {
		t.Fatal("secret refs not scoped correctly")
	}
	if !refs.allConfigsAllowed([]apigen.ValueRef{config}) || refs.allConfigsAllowed([]apigen.ValueRef{{ID: 10, Version: 1}}) {
		t.Fatal("config refs not scoped correctly")
	}
	if !refs.assetAllowed(apigen.ValueRef{ID: 3, Version: 1}) || !refs.assetAllowed(apigen.ValueRef{ID: 4, Version: 5}) || refs.assetAllowed(apigen.ValueRef{ID: 4, Version: 4}) {
		t.Fatal("asset refs not scoped correctly")
	}
	if !refs.usesGithub {
		t.Fatal("GitHub credentials should be allowed for GitHub-backed deployments")
	}
}

func TestSessionRejectsCrossMachineStatusWrite(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	m1Node := nodes.EnsurePrimaryNode(store, "m1", "m1", netip.MustParseAddr("10.0.0.1"), "")
	m2Node := nodes.EnsurePrimaryNode(store, "m2", "m2", netip.MustParseAddr("10.0.0.2"), "")
	spec := statetest.SpecWithVersion("1")
	spec.Container().Source = apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "docker.io/library/nginx"}}}
	m1 := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", m1Node.ID, spec)
	m2 := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", m2Node.ID, spec)
	m1Inst := statetest.CreateScheduledInstance(store, m1.Deployment.ID, m1.Meta.Version, m1Node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	m2Inst := statetest.CreateScheduledInstance(store, m2.Deployment.ID, m2.Meta.Version, m2Node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)

	sess := newSession(context.Background(), func() {}, m1Node.ID, "m1", scheduledInstancePredicateForNode(m1Node.ID), store, &sessionNetMapProvider{current: &apigen.ClusterNetMap{}})
	crossMachine := &apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: m2Inst.ID,
		Runner:              apigen.Some(apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING}),
	}
	crossMachine.BumpUpdatedAt()
	sess.handleStatusWrite(crossMachine)
	if got, err := store.Queries().GetLatestScheduledInstanceStatus(context.Background(), m2Inst.ID); err == nil && got.Runner.Present && got.Runner.Value.Status == apigen.RunningStatus_RUNNING_STATUS_RUNNING {
		t.Fatal("cross-machine status write was accepted")
	}

	sameMachine := &apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: m1Inst.ID,
		Runner:              apigen.Some(apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING}),
	}
	sameMachine.BumpUpdatedAt()
	sess.handleStatusWrite(sameMachine)
	if got, err := store.Queries().GetLatestScheduledInstanceStatus(context.Background(), m1Inst.ID); err != nil || !got.Runner.Present || got.Runner.Value.Status != apigen.RunningStatus_RUNNING_STATUS_RUNNING {
		t.Fatalf("same-machine status write was rejected; status = %v", got)
	}
}

func TestSessionRoutingUsesNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "secondary", "secondary-cn", netip.MustParseAddr("10.0.0.2"), "")
	handler := New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil, nil)
	sess := newSession(context.Background(), func() {}, node.ID, "secondary-cn", scheduledInstancePredicateForNode(node.ID), store, &sessionNetMapProvider{current: &apigen.ClusterNetMap{}})
	handler.registerSession(node.ID, "secondary-cn", sess)
	t.Cleanup(func() { handler.unregisterSession(node.ID, "secondary-cn", sess) })

	if sess.NodeID != node.ID {
		t.Fatalf("session node ID = %d, want %d", sess.NodeID, node.ID)
	}
	if _, ok := handler.ConnectedNodes()[node.ID]; !ok {
		t.Fatalf("node %d was not recorded as connected", node.ID)
	}

	req := &apigen.MsgToSecondary{DeploymentLogRequest: apigen.Some(apigen.DeploymentLogRequest{})}
	reader, err := handler.RequestLogs(node.ID, req)
	if err != nil {
		t.Fatalf("request logs: %v", err)
	}
	defer reader.Close()
	if !strings.HasPrefix(req.DeploymentLogRequest.Value.RequestID, "secondary-cn-") {
		t.Fatalf("request ID = %q, want secondary CN prefix", req.DeploymentLogRequest.Value.RequestID)
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
	for frame = <-sess.outbox; !frame.LogQueryRequest.Present; frame = <-sess.outbox {
	}
	if !strings.HasPrefix(frame.LogQueryRequest.Value.RequestID, "secondary-cn-") {
		t.Fatalf("log query frame = %+v, want secondary CN request ID", frame)
	}
	sess.handleIncoming(&apigen.MsgToPrimary{
		LogQueryResponse: apigen.Some(apigen.LogQueryResponse{Stats: apigen.Some(apigen.LogQueryStats{MatchedRows: 3})}),
		LogRequestID:     apigen.Some(frame.LogQueryRequest.Value.RequestID),
	})
	res := <-resCh
	if res.err != nil || res.resp == nil || res.resp.Stats.Value.MatchedRows != 3 {
		t.Fatalf("log query result = %+v, err = %v", res.resp, res.err)
	}

	_, err = handler.RequestLogs(node.ID+1, &apigen.MsgToSecondary{DeploymentLogRequest: apigen.Some(apigen.DeploymentLogRequest{})})
	var notConnected *NodeNotConnectedError
	if !errors.As(err, &notConnected) || notConnected.NodeID != node.ID+1 {
		t.Fatalf("missing node error = %v, want node ID %d", err, node.ID+1)
	}
}

func TestSessionClusterHelloUpdatesAuthenticatedNodeUnderlay(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("192.0.2.1"), "")
	nodes.ReportNode(store, primary.Identifier, apigen.NodeReported{Identifier: primary.Identifier, UnderlayAddress: apigen.AddrOf(netip.MustParseAddr("192.0.2.1")), WgPublicKey: primary.WGPublicKey, HostAddresses: primary.HostAddresses})
	secondary := nodes.EnsurePrimaryNode(store, "secondary", "secondary-cn", netip.MustParseAddr("192.0.2.2"), "")
	nodes.ReportNode(store, secondary.Identifier, apigen.NodeReported{Identifier: secondary.Identifier, UnderlayAddress: apigen.AddrOf(netip.MustParseAddr("192.0.2.2")), WgPublicKey: secondary.WGPublicKey, HostAddresses: secondary.HostAddresses})
	sess := newSession(context.Background(), func() {}, secondary.ID, secondary.Identifier, scheduledInstancePredicateForNode(secondary.ID), store, &sessionNetMapProvider{current: &apigen.ClusterNetMap{}})

	const helloWGKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="
	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: apigen.Some(apigen.ClusterHello{Reported: apigen.NodeReported{UnderlayAddress: apigen.AddrOf(netip.MustParseAddr("192.0.2.3")), WgPublicKey: helloWGKey}, ClusterProtocolVersion: apigen.ClusterProtocolVersion})})
	if got := nodeAddresses(t, store, secondary.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("secondary addresses = %v, want [192.0.2.3]", got)
	}
	if got := nodeAddresses(t, store, primary.ID); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("primary addresses = %v, want [192.0.2.1]", got)
	}

	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: apigen.Some(apigen.ClusterHello{Reported: apigen.NodeReported{UnderlayAddress: apigen.AddrOf(netip.MustParseAddr("2001:db8::3")), WgPublicKey: helloWGKey}, ClusterProtocolVersion: apigen.ClusterProtocolVersion})})
	if got := nodeAddresses(t, store, secondary.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("invalid hello changed secondary addresses to %v", got)
	}
}

func TestSessionRejectsClusterProtocolMismatch(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	secondary := nodes.EnsurePrimaryNode(store, "secondary", "secondary-cn", netip.MustParseAddr("10.0.0.2"), "")
	cancelled := false
	sess := newSession(context.Background(), func() { cancelled = true }, secondary.ID, secondary.Identifier, scheduledInstancePredicateForNode(secondary.ID), store, &sessionNetMapProvider{current: &apigen.ClusterNetMap{}})

	sess.handleIncoming(&apigen.MsgToPrimary{ClusterHello: apigen.Some(apigen.ClusterHello{ClusterProtocolVersion: apigen.ClusterProtocolVersion - 1})})
	if !cancelled {
		t.Fatal("cluster protocol mismatch did not cancel session")
	}
}

func nodeAddresses(t *testing.T, store *state.Service, nodeID uint64) []string {
	t.Helper()
	for _, node := range nodes.ListNodes(store.Queries()) {
		if node.ID == nodeID {
			if addr := node.UnderlayAddress.Addr(); addr.IsValid() {
				return []string{addr.String()}
			}
			return nil
		}
	}
	t.Fatalf("node %d not found", nodeID)
	return nil
}
