package webuihandler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/enrollment"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func acceptSecondaryNode(t *testing.T, store *state.Service, identifier string) *nodes.Node {
	t.Helper()
	req, version, err := nodes.UpsertEnrollmentRequest(store, "127.0.0.1", "v0.0.1", apigen.NodeReported{Identifier: identifier, UnderlayAddress: "10.0.0.2", WgPublicKey: "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.AcceptEnrollmentRequest(store, req.ID, identifier, req.RequestingMachineID, version); err != nil {
		t.Fatal(err)
	}
	for _, member := range nodes.ListNodes(store.Queries()) {
		if member.Identifier == identifier {
			return member
		}
	}
	t.Fatalf("node %q not listed", identifier)
	return nil
}

func TestDrainingNodeRefusesNewDeployments(t *testing.T) {
	h, _ := newNodeSpacesHandler(t)
	node := acceptSecondaryNode(t, h.Store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	if _, err := h.PostV1NodesDrain(ctx, &apigen.NodeDrainRequest{Identifier: node.Identifier, Draining: true}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_, err := h.PostV1DeploymentsCreate(ctx, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "web", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec})
	if err == nil || !strings.Contains(err.Error(), "node_draining") {
		t.Fatalf("create on draining node: got %v, want node_draining", err)
	}
	if _, err := h.PostV1NodesDrain(ctx, &apigen.NodeDrainRequest{Identifier: node.Identifier, Draining: false}); err != nil {
		t.Fatalf("undrain: %v", err)
	}
	if _, err := h.PostV1DeploymentsCreate(ctx, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "web", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec}); err != nil {
		t.Fatalf("create after undrain: %v", err)
	}
}

func TestEvictEndpointRefusesPinnedDeploymentsThenForces(t *testing.T) {
	h, _ := newNodeSpacesHandler(t)
	node := acceptSecondaryNode(t, h.Store, "secondary-id")
	ctx := apigen.Context{Ctx: context.Background()}
	cfg, err := h.PostV1DeploymentsCreate(ctx, &apigen.DeploymentCreateRequest{SpaceID: nodes.DefaultSpaceID, Name: "web", Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: remoteDeploymentSpec("nginx", hostNetworking())})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = h.PostV1NodesEvict(ctx, &apigen.NodeEvictRequest{Identifier: node.Identifier, ExpectedVersion: node.Version})
	if err == nil || !strings.Contains(err.Error(), "node_has_deployments") {
		t.Fatalf("evict without force: got %v, want node_has_deployments", err)
	}
	if _, err := h.PostV1NodesEvict(ctx, &apigen.NodeEvictRequest{Identifier: node.Identifier, ExpectedVersion: node.Version + 1, Force: true}); !errors.Is(err, NodeVersionChangedErr) {
		t.Fatalf("evict with stale version: got %v, want NodeVersionChangedErr", err)
	}
	event, err := h.PostV1NodesEvict(ctx, &apigen.NodeEvictRequest{Identifier: node.Identifier, ExpectedVersion: node.Version, Force: true})
	if err != nil || event.Value.Status != apigen.NodeLifecycleStatus_NODE_MEMBER_EVICTED {
		t.Fatalf("forced evict: event=%+v err=%v", event, err)
	}
	if _, err := h.PostV1NodesDrain(ctx, &apigen.NodeDrainRequest{Identifier: node.Identifier, Draining: true}); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("drain evicted node: got %v, want NodeNotFoundErr", err)
	}
	exposure, err := h.PostV1NodesExposure(ctx, &apigen.NodeExposureRequest{Identifier: node.Identifier})
	if err != nil {
		t.Fatalf("exposure: %v", err)
	}
	if exposure.NodeID != node.ID || len(exposure.Deployments) != 1 || exposure.Deployments[0].ID != cfg.DeploymentID || exposure.Deployments[0].Name != "web" {
		t.Fatalf("exposure = %+v, want deployment %d web", exposure, cfg.DeploymentID)
	}
	if !enrollment.Evicted(enrollment.NodeEvictedErr) || enrollment.Evicted(enrollment.IdentifierEvictedErr) {
		t.Fatal("eviction classification must key on the 410 status alone")
	}
}

func TestEvictionRequiresNodeDelete(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)
	node := acceptSecondaryNode(t, h.Store, "secondary-id")
	if _, err := h.PostV1NodesEvict(spaceop, &apigen.NodeEvictRequest{Identifier: node.Identifier, ExpectedVersion: node.Version}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("space operator evict: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1NodesExposure(spaceop, &apigen.NodeExposureRequest{Identifier: node.Identifier}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("space operator exposure: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1NodesDrain(spaceop, &apigen.NodeDrainRequest{Identifier: node.Identifier, Draining: true}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("space operator drain: got %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1NodesEvict(admin, &apigen.NodeEvictRequest{Identifier: node.Identifier, ExpectedVersion: node.Version}); err != nil {
		t.Fatalf("admin evict: %v", err)
	}
}
