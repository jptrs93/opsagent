package webuihandler

import (
	"errors"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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
		Destination: spacePeer(state.DefaultSpaceID),
	}); !errors.Is(err, NetworkPolicyDenyUnsupportedErr) {
		t.Fatalf("deny create error = %v, want NetworkPolicyDenyUnsupportedErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(state.DefaultSpaceID), spacePeer(state.DefaultSpaceID))); !errors.Is(err, NetworkPolicyRedundantErr) {
		t.Fatalf("same-space create error = %v, want NetworkPolicyRedundantErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(999), spacePeer(state.DefaultSpaceID))); !errors.Is(err, NetworkPolicyPeerNotFoundErr) {
		t.Fatalf("missing space create error = %v, want NetworkPolicyPeerNotFoundErr", err)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(42), spacePeer(state.DefaultSpaceID))); !errors.Is(err, NetworkPolicyPeerNotFoundErr) {
		t.Fatalf("missing deployment create error = %v, want NetworkPolicyPeerNotFoundErr", err)
	}

	bad := allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID))
	bad.Ports = []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 700000}}
	if _, err := h.PostV1NetworkPoliciesCreate(admin, bad); !errors.Is(err, InvalidNetworkPolicyErr) {
		t.Fatalf("bad port create error = %v, want InvalidNetworkPolicyErr", err)
	}

	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID <= 0 || created.Version != 1 || created.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
		t.Fatalf("created = %+v", created)
	}
}

func TestNetworkPolicyDestinationConsentAuthz(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	spaceAdmin := enforceCtx(2, false)
	viewer := enforceCtx(3, false)

	if _, err := h.PostV1NetworkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID))); !errors.Is(err, NetworkPolicyPeerNotFoundErr) {
		t.Fatalf("space admin naming invisible source error = %v, want NetworkPolicyPeerNotFoundErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesCreate(spaceAdmin, allowCreateRequest(spacePeer(state.DefaultSpaceID), spacePeer(staging.ID))); !errors.Is(err, NetworkPolicyPeerNotFoundErr) {
		t.Fatalf("space admin writing to invisible destination error = %v, want NetworkPolicyPeerNotFoundErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesCreate(viewer, allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID))); err == nil {
		t.Fatal("viewer created a policy without update access on the destination space")
	}

	admin := enforceCtx(1, false)
	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}

	list, err := h.PostV1NetworkPoliciesList(spaceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("space admin list = %+v, want the rule targeting their space", list.Items)
	}

	if err := h.PostV1NetworkPoliciesDelete(viewer, &apigen.NetworkPolicyDeleteRequest{ID: created.ID}); err == nil {
		t.Fatal("viewer deleted a policy without update access on the destination space")
	}
	if err := h.PostV1NetworkPoliciesDelete(spaceAdmin, &apigen.NetworkPolicyDeleteRequest{ID: created.ID}); err != nil {
		t.Fatalf("destination space admin delete failed: %v", err)
	}
	if list, err := h.PostV1NetworkPoliciesList(enforceCtx(1, false)); err != nil || len(list.Items) != 0 {
		t.Fatalf("list after delete = %+v err=%v, want empty", list, err)
	}
}

func TestNetworkPolicyUpdateVersionConflict(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(staging.ID), spacePeer(state.DefaultSpaceID)))
	if err != nil {
		t.Fatal(err)
	}
	update := &apigen.NetworkPolicyUpdateRequest{
		ID:          created.ID,
		Version:     created.Version,
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      spacePeer(staging.ID),
		Destination: spacePeer(state.DefaultSpaceID),
		Ports:       []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 443}},
	}
	updated, err := h.PostV1NetworkPoliciesUpdate(admin, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || len(updated.Ports) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := h.PostV1NetworkPoliciesUpdate(admin, update); !errors.Is(err, NetworkPolicyVersionConflictErr) {
		t.Fatalf("stale update error = %v, want NetworkPolicyVersionConflictErr", err)
	}
	if _, err := h.PostV1NetworkPoliciesUpdate(admin, &apigen.NetworkPolicyUpdateRequest{ID: 99, Version: 1, Action: update.Action, Source: update.Source, Destination: update.Destination}); !errors.Is(err, NetworkPolicyNotFoundErr) {
		t.Fatalf("missing update error = %v, want NetworkPolicyNotFoundErr", err)
	}
}

func TestNetworkPolicyDeploymentPeerResolution(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	admin := enforceCtx(1, false)
	spec := &apigen.DeploymentSpec{}
	spec.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	cfg := createTestDeployment(h.Store, "primary-id", staging.ID, "api", spec)

	created, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(spacePeer(state.DefaultSpaceID), deploymentPeer(cfg.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if created.Destination.Kind != apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT || created.Destination.ID != cfg.ID {
		t.Fatalf("created destination = %+v", created.Destination)
	}

	if _, err := h.PostV1NetworkPoliciesCreate(admin, allowCreateRequest(deploymentPeer(cfg.ID), spacePeer(staging.ID))); !errors.Is(err, NetworkPolicyRedundantErr) {
		t.Fatalf("deployment-to-own-space create error = %v, want NetworkPolicyRedundantErr", err)
	}
}
