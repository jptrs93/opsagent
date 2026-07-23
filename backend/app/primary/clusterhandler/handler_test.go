package clusterhandler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestBuildAllowedRefs(t *testing.T) {
	secretID := int32(7)
	configID := int32(9)
	refs := buildAllowedRefs([]apigen.DeploymentWithStatus{{
		Config: apigen.DeploymentConfig2{
			ID: 42,
			Spec: apigen.DeploymentSpec2{
				Container1Spec: &apigen.ContainerSpec{
					Source: apigen.ContainerBundleSource{NixDockerBuild: &apigen.NixDockerBuild2{Repo: "github.com/acme/app", Flake: "flake.nix"}},
					Runtime: apigen.ContainerRuntime{
						EnvVars: map[string]*apigen.EnvVarValue2{
							"SECRET": {SecretID: &secretID},
							"CONFIG": {ConfigID: &configID},
							"ASSET":  {Asset: "app.env", AssetID: 3},
						},
						AssetMounts: []*apigen.AssetMount2{{AssetID: 4, ContainerPath: "/etc/nginx/nginx.conf", Permission: apigen.FilePermission_READ_ONLY}},
					},
				},
			},
		},
	}})

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
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	m1Node := store.EnsurePrimaryNode("m1", "m1")
	m2Node := store.EnsurePrimaryNode("m2", "m2")
	spec := &apigen.DeploymentSpec2{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "docker.io/library/nginx"}},
			Version: "1",
			Running: true,
		},
	}
	m1 := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, m1Node.ID, spec)
	m2 := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, m2Node.ID, spec)

	sess := newSession(context.Background(), func() {}, m1Node.ID, "m1", deploymentPredicateForNode(m1Node.ID), store, nil)
	crossMachine := &apigen.DeploymentStatus{DeploymentID: m2.ID, Runner: apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING}}
	crossMachine.BumpUpdatedAt()
	sess.handleStatusWrite(crossMachine)
	if got := store.FetchDeploymentStatus(m2.ID); got.Runner.Status == apigen.RunningStatus_RUNNING {
		t.Fatal("cross-machine status write was accepted")
	}

	sameMachine := &apigen.DeploymentStatus{DeploymentID: m1.ID, Runner: apigen.RunnerStatus{Status: apigen.RunningStatus_RUNNING}}
	sameMachine.BumpUpdatedAt()
	sess.handleStatusWrite(sameMachine)
	if got := store.FetchDeploymentStatus(m1.ID); got.Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("same-machine status write was rejected; status = %v", got.Runner.Status)
	}
}

func TestSessionRoutingUsesNodeID(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("worker", "worker-cn")
	handler := New(store, nil, nil, nil, network.Prefix{}, nil)
	sess := newSession(context.Background(), func() {}, node.ID, "worker-cn", deploymentPredicateForNode(node.ID), store, nil)
	handler.registerSession(node.ID, "worker-cn", sess)
	t.Cleanup(func() { handler.unregisterSession(node.ID, "worker-cn", sess) })

	if sess.NodeID != node.ID {
		t.Fatalf("session node ID = %d, want %d", sess.NodeID, node.ID)
	}
	if _, ok := handler.ConnectedNodes()[node.ID]; !ok {
		t.Fatalf("node %d was not recorded as connected", node.ID)
	}

	req := &apigen.MsgToWorker{DeploymentLogRequest: &apigen.DeploymentLogRequest{}}
	reader, err := handler.RequestLogs(node.ID, req)
	if err != nil {
		t.Fatalf("request logs: %v", err)
	}
	defer reader.Close()
	if !strings.HasPrefix(req.DeploymentLogRequest.RequestID, "worker-cn-") {
		t.Fatalf("request ID = %q, want worker CN prefix", req.DeploymentLogRequest.RequestID)
	}

	search, err := handler.RequestLogSearch(node.ID, &apigen.MsgToWorker{LogSearchRequest: &apigen.LogSearchRequest{}})
	if err != nil {
		t.Fatalf("request log search: %v", err)
	}
	defer search.Close()

	_, err = handler.RequestLogs(node.ID+1, &apigen.MsgToWorker{DeploymentLogRequest: &apigen.DeploymentLogRequest{}})
	var notConnected *NodeNotConnectedError
	if !errors.As(err, &notConnected) || notConnected.NodeID != node.ID+1 {
		t.Fatalf("missing node error = %v, want node ID %d", err, node.ID+1)
	}
}

func TestSessionClusterHelloUpdatesAuthenticatedNodeUnderlay(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary")
	store.MustSetNodeAddresses(primary.ID, []string{"192.0.2.1"})
	worker := store.EnsurePrimaryNode("worker", "worker-cn")
	store.MustSetNodeAddresses(worker.ID, []string{"192.0.2.2"})
	sess := newSession(context.Background(), func() {}, worker.ID, worker.Identifier, deploymentPredicateForNode(worker.ID), store, nil)

	sess.handleIncoming(&apigen.MsgToMaster{ClusterHello: &apigen.ClusterHello{UnderlayAddress: " 192.0.2.3 ", ClusterProtocolVersion: apigen.ClusterProtocolVersion}})
	if got := nodeAddresses(t, store, worker.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("worker addresses = %v, want [192.0.2.3]", got)
	}
	if got := nodeAddresses(t, store, primary.ID); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("primary addresses = %v, want [192.0.2.1]", got)
	}

	sess.handleIncoming(&apigen.MsgToMaster{ClusterHello: &apigen.ClusterHello{UnderlayAddress: "2001:db8::3", ClusterProtocolVersion: apigen.ClusterProtocolVersion}})
	if got := nodeAddresses(t, store, worker.ID); len(got) != 1 || got[0] != "192.0.2.3" {
		t.Fatalf("invalid hello changed worker addresses to %v", got)
	}
}

func TestSessionRejectsClusterProtocolMismatch(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	worker := store.EnsurePrimaryNode("worker", "worker-cn")
	cancelled := false
	sess := newSession(context.Background(), func() { cancelled = true }, worker.ID, worker.Identifier, deploymentPredicateForNode(worker.ID), store, nil)

	sess.handleIncoming(&apigen.MsgToMaster{ClusterHello: &apigen.ClusterHello{ClusterProtocolVersion: apigen.ClusterProtocolVersion - 1}})
	if !cancelled {
		t.Fatal("cluster protocol mismatch did not cancel session")
	}
}

func nodeAddresses(t *testing.T, store *sqlite.PrimaryStorage, nodeID int32) []string {
	t.Helper()
	for _, node := range store.ListNodes() {
		if node.ID == nodeID {
			return node.Addresses
		}
	}
	t.Fatalf("node %d not found", nodeID)
	return nil
}
