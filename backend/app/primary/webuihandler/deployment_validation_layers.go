package webuihandler

import (
	"net/http"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var NodeSpaceNotAllowedErr = apigen.NewApiErr(
	"This node does not allow deployments from that space",
	"node_space_not_allowed", http.StatusConflict)

func preLockDeploymentValidate(updated *apigen.Deployment) error {
	if updated.Name == "" {
		return invalidConfigErrf("name is required")
	}
	if updated.NodeID <= 0 {
		return invalidConfigErrf("nodeId is required")
	}
	if updated.SpaceID < 0 || updated.SpaceID > network.MaxSpaceID {
		return invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	return validateNixWorkloadVersion(&updated.Spec)
}

func validateNodeAllowsSpace(live state.LiveState, nodeID, spaceID int32) error {
	node := live.Nodes[nodeID]
	if node == nil {
		return invalidConfigErrf("node is not registered")
	}
	if !slices.Contains(node.AllowedSpaces, spaceID) {
		return NodeSpaceNotAllowedErr
	}
	return nil
}

func validateNoDuplicateIdentity(live state.LiveState, updated *apigen.Deployment) error {
	for _, other := range live.Deployments {
		if other.Deleted || other.ID == updated.ID {
			continue
		}
		if storage.DeploymentKeyMatches(*other, updated.NodeID, updated.SpaceID, updated.Name) {
			return DuplicateDeploymentErr
		}
	}
	return nil
}

func (h *Handler) inLockValidateDeploymentCreate(updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalIdentity(updated.SpaceID, updated.Name) {
		return invalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.NodeID, updated.SpaceID); err != nil {
		return err
	}
	if err := h.validateNodeNetworkingClaims(live, updated.NodeID, updated.ID, &updated.Spec); err != nil {
		return err
	}
	if err := h.validateAddressEnvRefs(live, updated.NodeID, updated.ID, updated.SpaceID, &updated.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Spec, updated.NodeID, updated.ID, updated.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Spec, updated.SpaceID)
}

func (h *Handler) inLockValidateDeploymentUpdate(existing, updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalConfig(existing) {
		base, err := cloneDeploymentSpec(&existing.Spec)
		if err != nil {
			return err
		}
		if err := base.SetWorkloadState(updated.Spec.WorkloadVersion(), updated.Spec.WorkloadRunning()); err != nil {
			return invalidConfigErrf("spec: %v", err)
		}
		if !state.DeploymentSpecsEqual(base, &updated.Spec) {
			return invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
		}
	}
	if internaldeploy.IsSelfConfig(existing) && !updated.Spec.WorkloadRunning() {
		return invalidConfigErrf("the opendeploy system deployment cannot be stopped")
	}
	if updated.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL &&
		deploymentUsesAddressID(live, int32Set([]int32{existing.ID})) {
		return invalidConfigErrf("deployment networking cannot leave virtual mode while address references exist")
	}
	if err := h.validateNodeNetworkingClaims(live, updated.NodeID, updated.ID, &updated.Spec); err != nil {
		return err
	}
	if err := h.validateAddressEnvRefs(live, updated.NodeID, updated.ID, updated.SpaceID, &updated.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Spec, updated.NodeID, updated.ID, updated.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Spec, updated.SpaceID)
}

func (h *Handler) inLockValidateDeploymentSpaceMove(existing, updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalConfig(existing) {
		return invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}
	if existing.SpaceID == 0 {
		return invalidConfigErrf("deployments in space 0 cannot be moved")
	}
	if updated.SpaceID < 1 || updated.SpaceID > network.MaxSpaceID {
		return invalidConfigErrf("spaceId must be between 1 and %d", network.MaxSpaceID)
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.NodeID, updated.SpaceID); err != nil {
		return err
	}
	ids := int32Set([]int32{existing.ID})
	if deploymentUsesAddressID(live, ids) {
		return DeploymentAddressReferencedErr
	}
	if updated.SpaceID != state.DefaultSpaceID && referencesOutsideSpace(live, ids, crossDeploymentMountSourceIDs, updated.SpaceID) {
		return MoveReferencesOutsideSpaceErr
	}
	if err := h.validateAddressEnvRefs(live, updated.NodeID, updated.ID, updated.SpaceID, &updated.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Spec, updated.NodeID, updated.ID, updated.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Spec, updated.SpaceID)
}

func (h *Handler) inLockValidateDeploymentDelete(existing *apigen.Deployment, live state.LiveState) error {
	statuses := h.Store.InstanceStatusesForDeploymentLocked(existing.ID)
	if internaldeploy.IsInternalConfig(existing) {
		if !h.canDeleteStaleDisconnectedSystemDeployment(existing) {
			return invalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !h.canDeleteDeployment(existing, statuses) {
		return invalidConfigErrf("deployment must be stopped before deletion")
	}
	if details := h.deploymentRefDetails(live, int32Set([]int32{existing.ID}), addressRefIDs); len(details) > 0 {
		return referenceInUseDetailErr("Deployment address", details)
	}
	return nil
}
