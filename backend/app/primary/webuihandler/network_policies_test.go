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

	if _, err := h.PostV1NetworkPoliciesCreate(admin, &apigen.NetworkPolicyCreateRequest{
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY,
		Source:      spacePeer(staging.ID),
		Destination: spacePeer(nodes.DefaultSpaceID),
	}); !errors.Is(err, networkpolicies.DenyUnsupportedErr) {
		t.Fatalf("deny create error = %v, want networkpolicies.DenyUnsupportedErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.RedundantErr) {
		t.Fatalf("same-space create error = %v, want networkpolicies.RedundantErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(999), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("missing space create error = %v, want networkpolicies.PeerNotFoundErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(42), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("missing deployment create error = %v, want networkpolicies.PeerNotFoundErr", err)
	}

	bad := allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))
	bad.Ports = []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 700000}}
	if _, err := h.PostV1NetworkPoliciesCreate(admin, bad); !errors.Is(err, networkpolicies.InvalidErr) {
		t.Fatalf("bad port create error = %v, want networkpolicies.InvalidErr", err)
	}

	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.NetworkPolicyID <= 0 || created.Version != 1 || created.Value.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
		t.Fatalf("created = %+v", created)
	}
}

func TestNetworkPolicyDestinationConsentAuthz(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	spaceAdmin := enforceCtx(2, false)
	viewer := enforceCtx(3, false)

	if _, err := h.PostV1NetworkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("space admin naming invisible source error = %v, want networkpolicies.PeerNotFoundErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), spacePeer(staging.ID))); !errors.Is(err, networkpolicies.PeerNotFoundErr) {
		t.Fatalf("space admin writing to invisible destination error = %v, want networkpolicies.PeerNotFoundErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesCreate(viewer, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID))); err == nil {
		t.Fatal("viewer created a policy without update access on the destination space")
	}

	admin := enforceCtx(1, false)
	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}

	list, err := h.PostV1NetworkPoliciesList(spaceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].NetworkPolicyID != created.NetworkPolicyID {
		t.Fatalf("space admin list = %+v, want the rule targeting their space", list.Items)
	}

	if err := h.PostV1NetworkPoliciesDelete(viewer, &apigen.NetworkPolicyDeleteRequest{ID: created.NetworkPolicyID}); err == nil {
		t.Fatal("viewer deleted a policy without update access on the destination space")
	}
	if err := h.PostV1NetworkPoliciesDelete(spaceAdmin, &apigen.NetworkPolicyDeleteRequest{ID: created.NetworkPolicyID}); err != nil {
		t.Fatalf("destination space admin delete failed: %v", err)
	}
	if list, err := h.PostV1NetworkPoliciesList(enforceCtx(1, false)); err != nil || len(list.Items) != 0 {
		t.Fatalf("list after delete = %+v err=%v, want empty", list, err)
	}
}

func TestNetworkPolicyUpdateVersionConflict(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(nodes.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	update := &apigen.NetworkPolicyUpdateRequest{
		ID:          created.NetworkPolicyID,
		Version:     created.Version,
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      spacePeer(staging.ID),
		Destination: spacePeer(nodes.DefaultSpaceID),
		Ports:       []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 443}},
	}
	updated, err := h.PostV1NetworkPoliciesUpdate(admin, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || len(updated.Value.Ports) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := h.PostV1NetworkPoliciesUpdate(admin, update); !errors.Is(err, networkpolicies.VersionConflictErr) {
		t.Fatalf("stale update error = %v, want networkpolicies.VersionConflictErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesUpdate(admin, &apigen.NetworkPolicyUpdateRequest{ID: 99, Version: 1, Action: update.Action, Source: update.Source, Destination: update.Destination}); !errors.Is(err, networkpolicies.NotFoundErr) {
		t.Fatalf("missing update error = %v, want networkpolicies.NotFoundErr", err)
	}
}

func TestNetworkPolicyDeploymentPeerResolution(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	spec := &apigen.DeploymentSpec{}
	spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	cfg := createTestDeployment(h.Store, "primary-id", staging.ID, "api", spec)

	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(nodes.DefaultSpaceID), deploymentPeer(cfg.DeploymentID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.Value.Destination.Kind != apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT || created.Value.Destination.ID != cfg.DeploymentID {
		t.Fatalf("created destination = %+v", created.Value.Destination)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(cfg.DeploymentID), spacePeer(staging.ID))); !errors.Is(err, networkpolicies.RedundantErr) {
		t.Fatalf("deployment-to-own-space create error = %v, want networkpolicies.RedundantErr", err)
	}
}
