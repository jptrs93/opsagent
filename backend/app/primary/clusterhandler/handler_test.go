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
		Config: apigen.DeploymentConfig{
			ID: 42,
			Spec: apigen.DeploymentSpec{
				Prepare: apigen.PrepareConfig{NixDockerBuild: &apigen.NixDockerBuildConfig{Repo: "github.com/acme/app", Flake: "flake.nix"}},
				Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
					EnvVars: map[string]*apigen.EnvVarValue{
						"SECRET": &apigen.EnvVarValue{SecretID: &secretID},
						"CONFIG": &apigen.EnvVarValue{ConfigID: &configID},
						"ASSET":  &apigen.EnvVarValue{Asset: "app.env", AssetID: 3},
					},
					AssetMounts: []*apigen.ContainerAssetMount{&apigen.ContainerAssetMount{Asset: "nginx.conf", AssetID: 4, Path: "/etc/nginx/nginx.conf"}},
				}},
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
	spec := &apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "docker.io/library/nginx"}},
		Runner:  apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{}},
	}
	m1 := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, m1Node.ID, spec, apigen.DesiredState{Version: "1", Running: true})
	m2 := store.MustCreateDeploymentForNode(apigen.Context{}, &apigen.DeploymentIdentity{SpaceID: 1, Name: "web"}, m2Node.ID, spec, apigen.DesiredState{Version: "1", Running: true})

	sess := newSession(context.Background(), func() {}, m1Node.ID, "m1", deploymentPredicateForNode(m1Node.ID), store)
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
	handler := New(store, nil, nil, nil, network.Prefix{})
	sess := newSession(context.Background(), func() {}, node.ID, "worker-cn", deploymentPredicateForNode(node.ID), store)
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
