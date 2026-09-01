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
	if updated.Def.Name == "" {
		return invalidConfigErrf("name is required")
	}
	if updated.Def.NodeID <= 0 {
		return invalidConfigErrf("nodeId is required")
	}
	if updated.Def.SpaceID < 0 || updated.Def.SpaceID > network.MaxSpaceID {
		return invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	return validateNixWorkloadVersion(&updated.Def.Spec)
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
		if other.Deleted() || other.ID == updated.ID {
			continue
		}
		if storage.DeploymentKeyMatches(other.Def, updated.Def.NodeID, updated.Def.SpaceID, updated.Def.Name) {
			return DuplicateDeploymentErr
		}
	}
	return nil
}

func (h *Handler) inLockValidateDeploymentCreate(updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalIdentity(updated.Def.SpaceID, updated.Def.Name) {
		return invalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.Def.NodeID, updated.Def.SpaceID); err != nil {
		return err
	}
	if err := h.validateNodeNetworkingClaims(live, updated.Def.NodeID, updated.ID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := h.validateAddressEnvRefs(live, updated.Def.NodeID, updated.ID, updated.Def.SpaceID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Def.Spec, updated.Def.NodeID, updated.ID, updated.Def.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Def.Spec, updated.Def.SpaceID)
}

func (h *Handler) inLockValidateDeploymentUpdate(existing, updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalConfig(existing) {
		base, err := cloneDeploymentSpec(&existing.Def.Spec)
		if err != nil {
			return err
		}
		if err := base.SetWorkloadState(updated.Def.Spec.WorkloadVersion(), updated.Def.Spec.WorkloadRunning()); err != nil {
			return invalidConfigErrf("spec: %v", err)
		}
		if !state.DeploymentSpecsEqual(base, &updated.Def.Spec) {
			return invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
		}
	}
	if internaldeploy.IsSelfConfig(existing) && !updated.Def.Spec.WorkloadRunning() {
		return invalidConfigErrf("the opendeploy system deployment cannot be stopped")
	}
	if updated.Def.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL &&
		deploymentUsesAddressID(live, int32Set([]int32{existing.ID})) {
		return invalidConfigErrf("deployment networking cannot leave virtual mode while address references exist")
	}
	if err := h.validateNodeNetworkingClaims(live, updated.Def.NodeID, updated.ID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := h.validateAddressEnvRefs(live, updated.Def.NodeID, updated.ID, updated.Def.SpaceID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Def.Spec, updated.Def.NodeID, updated.ID, updated.Def.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Def.Spec, updated.Def.SpaceID)
}

func (h *Handler) inLockValidateDeploymentSpaceMove(existing, updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalConfig(existing) {
		return invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}
	if existing.Def.SpaceID == 0 {
		return invalidConfigErrf("deployments in space 0 cannot be moved")
	}
	if updated.Def.SpaceID < 1 || updated.Def.SpaceID > network.MaxSpaceID {
		return invalidConfigErrf("spaceId must be between 1 and %d", network.MaxSpaceID)
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.Def.NodeID, updated.Def.SpaceID); err != nil {
		return err
	}
	ids := int32Set([]int32{existing.ID})
	if deploymentUsesAddressID(live, ids) {
		return DeploymentAddressReferencedErr
	}
	if updated.Def.SpaceID != state.DefaultSpaceID && referencesOutsideSpace(live, ids, crossDeploymentMountSourceIDs, updated.Def.SpaceID) {
		return MoveReferencesOutsideSpaceErr
	}
	if err := h.validateAddressEnvRefs(live, updated.Def.NodeID, updated.ID, updated.Def.SpaceID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := h.validateCrossDeploymentMountSources(live, &updated.Def.Spec, updated.Def.NodeID, updated.ID, updated.Def.SpaceID); err != nil {
		return err
	}
	return h.validateRefSpaces(&updated.Def.Spec, updated.Def.SpaceID)
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
