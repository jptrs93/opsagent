package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/networkpolicies"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func spacePeer(id int32) *apigen.NetworkPolicyPeerRef {
	return &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: id}
}

func deploymentPeer(id int32) *apigen.NetworkPolicyPeerRef {
	return &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT, ID: id}
}

func allowCreateRequest(source, destination *apigen.NetworkPolicyPeerRef) *apigen.NetworkPolicyCreateRequest {
	return &apigen.NetworkPolicyCreateRequest{
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      source,
		Destination: destination,
	}
}

func TestNetworkPolicyCreateValidation(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)

	if _, err := h.networkPoliciesCreate(admin, &apigen.NetworkPolicyCreateRequest{
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY,
		Source:      spacePeer(staging.ID),
		Destination: spacePeer(nodes.DefaultSpaceID),
	}); !errors.Is(err, networkpolicies.DenyUnsupportedErr) {
		t.Fatalf("deny create error = %v, want networkpolicies.DenyUnsupportedErr", err)
	}

	if _, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.RedundantErr) {
		t.Fatalf("same-space create error = %v, want networkpolicies.RedundantErr", err)
	}

	if _, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(999), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("missing space create error = %v, want networkpolicies.PeerNotFoundErr", err)
	}

	if _, err := h.networkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(42), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("missing deployment create error = %v, want networkpolicies.PeerNotFoundErr", err)
	}

	bad := allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))
	bad.Ports = []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 700000}}
	if _, err := h.networkPoliciesCreate(admin, bad); !errors.Is(err, networkpolicies.InvalidErr) {
		t.Fatalf("bad port create error = %v, want networkpolicies.InvalidErr", err)
	}

	created, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.NetworkPolicyID <= 0 || created.Seq <= 0 || created.Value.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
		t.Fatalf("created = %+v", created)
	}
}

func TestNetworkPolicyDestinationConsentAuthz(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	spaceAdmin := enforceCtx(2, false)
	viewer := enforceCtx(3, false)

	if _, err := h.networkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("space admin naming invisible source error = %v, want networkpolicies.PeerNotFoundErr", err)
	}
	if _, err := h.networkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), spacePeer(staging.ID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("space admin writing to invisible destination error = %v, want networkpolicies.PeerNotFoundErr", err)
	}
	if _, err := h.networkPoliciesCreate(viewer, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))); err == nil {
		t.Fatal("viewer created a policy without update access on the destination space")
	}

	admin := enforceCtx(1, false)
	created, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}

	list := visibleOpening(t, h, spaceAdmin)[apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY]
	if len(list) != 1 || list[int64(created.NetworkPolicyID)] == nil {
		t.Fatalf("space admin list = %+v, want the rule targeting their space", list)
	}

	if err := h.PostV1NetworkPoliciesDelete(viewer, &apigen.NetworkPolicyDeleteRequest{ID: created.NetworkPolicyID}); err == nil {
		t.Fatal("viewer deleted a policy without update access on the destination space")
	}
	if err := h.PostV1NetworkPoliciesDelete(spaceAdmin, &apigen.NetworkPolicyDeleteRequest{ID: created.NetworkPolicyID}); err != nil {
		t.Fatalf("destination space admin delete failed: %v", err)
	}
	if list := visibleOpening(t, h, enforceCtx(1, false))[apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY]; len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
}

func TestNetworkPolicyUpdateVersionConflict(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	created, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	update := &apigen.NetworkPolicyUpdateRequest{
		ID:          created.NetworkPolicyID,
		ExpectedSeq: created.Seq,
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      spacePeer(staging.ID),
		Destination: spacePeer(nodes.DefaultSpaceID),
		Ports:       []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 443}},
	}
	updated, err := h.networkPoliciesUpdate(admin, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Seq <= created.Seq || len(updated.Value.Ports) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := h.networkPoliciesUpdate(admin, update); !errors.Is(err, networkpolicies.VersionConflictErr) {
		t.Fatalf("stale update error = %v, want networkpolicies.VersionConflictErr", err)
	}
	if _, err := h.networkPoliciesUpdate(admin, &apigen.NetworkPolicyUpdateRequest{ID: 99, ExpectedSeq: 1, Action: update.Action, Source: update.Source, Destination: update.Destination}); !errors.Is(err, networkpolicies.NotFoundErr) {
		t.Fatalf("missing update error = %v, want networkpolicies.NotFoundErr", err)
	}
}

func TestNetworkPolicyDeploymentPeerResolution(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	spec := &apigen.DeploymentSpec{}
	spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	cfg := createTestDeployment(h.Store, "primary-id", staging.ID, "api", spec)

	created, err := h.networkPoliciesCreate(admin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), deploymentPeer(cfg.DeploymentID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.Value.Destination.Kind != apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT || created.Value.Destination.ID != cfg.DeploymentID {
		t.Fatalf("created destination = %+v", created.Value.Destination)
	}

	if _, err := h.networkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(cfg.DeploymentID), spacePeer(staging.ID))); !errors.Is(err, networkpolicies.RedundantErr) {
		t.Fatalf("deployment-to-own-space create error = %v, want networkpolicies.RedundantErr", err)
	}
}
