package webuihandler

import (
	"net/http"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var NodeSpaceNotAllowedErr = apigen.NewApiErr(
	"This node does not allow deployments from that space",
	"node_space_not_allowed", http.StatusConflict)

func validateDeploymentDef(def *apigen.DeploymentDef) error {
	if def.Name == "" {
		return invalidConfigErrf("name is required")
	}
	if def.NodeID <= 0 {
		return invalidConfigErrf("nodeId is required")
	}
	if def.SpaceID < 0 || def.SpaceID > network.MaxSpaceID {
		return invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	return validateNixWorkloadVersion(&def.Spec)
}

func preLockValidateDeploymentCreate(store *state.Service, secretStore *secrets.Manager, gitVersions GitSourceProvider, ctx apigen.Context, updated *apigen.Deployment) error {
	if err := validateDeploymentDef(&updated.Def); err != nil {
		return err
	}
	spec, err := validateDeploymentSpec(store, secretStore, &updated.Def.Spec)
	if err != nil {
		return err
	}
	updated.Def.Spec = *spec
	if spec.WorkloadRunning() {
		return verifyRunningNixSource(gitVersions, ctx, spec)
	}
	return nil
}

func preLockValidateDeploymentUpdate(store *state.Service, secretStore *secrets.Manager, gitVersions GitSourceProvider, ctx apigen.Context, existing *apigen.Deployment, req *apigen.DeploymentUpdateRequestV2, updated *apigen.Deployment) error {
	if err := validateDeploymentVersion(existing, req.ExpectedVersion); err != nil {
		return err
	}
	if req.SpecUpdate != nil {
		spec, err := validateDeploymentSpec(store, secretStore, &updated.Def.Spec)
		if err != nil {
			return err
		}
		updated.Def.Spec = *spec
	}
	if !updated.WorkloadRunning() && !sameDesiredVersionSource(&existing.Def.Spec, &updated.Def.Spec) {
		if err := updated.SetWorkloadState("", false); err != nil {
			return invalidConfigErrf("spec: %v", err)
		}
	}
	if updated.Def.NodeID == existing.Def.NodeID && updated.Def.SpaceID == existing.Def.SpaceID &&
		updated.Def.Name == existing.Def.Name && state.DeploymentSpecsEqual(&updated.Def.Spec, &existing.Def.Spec) {
		return invalidConfigErrf("nothing changed")
	}
	if updated.WorkloadRunning() && updated.WorkloadVersion() == "" {
		return invalidConfigErrf("deployment has no version to start; set a target version")
	}
	if err := validateDeploymentDef(&updated.Def); err != nil {
		return err
	}
	nixChanged := !sameNixBuildConfig(nixSource(&existing.Def.Spec), nixSource(&updated.Def.Spec))
	if updated.WorkloadRunning() && nixSource(&updated.Def.Spec) != nil &&
		(!existing.WorkloadRunning() || updated.WorkloadVersion() != existing.WorkloadVersion() || nixChanged) {
		return verifyRunningNixSource(gitVersions, ctx, &updated.Def.Spec)
	}
	return nil
}

func preLockValidateDeploymentDelete(existing *apigen.Deployment, requestVersion int32) error {
	return validateDeploymentVersion(existing, requestVersion)
}

func validateDeploymentVersion(existing *apigen.Deployment, requestVersion int32) error {
	if requestVersion != existing.Version+1 {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", requestVersion, existing.Version+1)
	}
	return nil
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
		if other.ID == updated.ID {
			continue
		}
		if storage.DeploymentKeyMatches(other.Def, updated.Def.NodeID, updated.Def.SpaceID, updated.Def.Name) {
			return DuplicateDeploymentErr
		}
	}
	return nil
}

func inLockValidateDeploymentCreate(store *state.Service, secretStore *secrets.Manager, primaryNodeID int32, updated *apigen.Deployment, live state.LiveState) error {
	if internaldeploy.IsInternalIdentity(updated.Def.SpaceID, updated.Def.Name) {
		return invalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.Def.NodeID, updated.Def.SpaceID); err != nil {
		return err
	}
	if err := validateNodeNetworkingClaims(primaryNodeID, live, updated.Def.NodeID, updated.ID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := validateAddressEnvRefs(live, updated.Def.NodeID, updated.ID, updated.Def.SpaceID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Def.Spec, updated.Def.NodeID, updated.ID, updated.Def.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(store, secretStore, &updated.Def.Spec, updated.Def.SpaceID)
}

func inLockValidateDeploymentUpdate(store *state.Service, secretStore *secrets.Manager, primaryNodeID int32, existing, updated *apigen.Deployment, expectedVersion int32, live state.LiveState) error {
	if existing == nil || existing.Deleted() {
		return DeploymentNotFoundErr
	}
	if err := validateDeploymentVersion(existing, expectedVersion); err != nil {
		return err
	}
	if existing.Def.SpaceID != updated.Def.SpaceID {
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
	} else {
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
		if err := validateNodeNetworkingClaims(primaryNodeID, live, updated.Def.NodeID, updated.ID, &updated.Def.Spec); err != nil {
			return err
		}
	}
	if err := validateAddressEnvRefs(live, updated.Def.NodeID, updated.ID, updated.Def.SpaceID, &updated.Def.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Def.Spec, updated.Def.NodeID, updated.ID, updated.Def.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(store, secretStore, &updated.Def.Spec, updated.Def.SpaceID)
}

func inLockValidateDeploymentDelete(store *state.Service, cluster *clusterhandler.Handler, primaryNodeID int32, existing *apigen.Deployment, requestVersion int32, live state.LiveState) error {
	if existing == nil || existing.Deleted() {
		return DeploymentNotFoundErr
	}
	if err := validateDeploymentVersion(existing, requestVersion); err != nil {
		return err
	}
	statuses := store.InstanceStatusesForDeploymentLocked(existing.ID)
	if internaldeploy.IsInternalConfig(existing) {
		if !canDeleteStaleDisconnectedSystemDeployment(cluster, primaryNodeID, existing) {
			return invalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !canDeleteDeployment(cluster, primaryNodeID, existing, statuses) {
		return invalidConfigErrf("deployment must be stopped before deletion")
	}
	if details := deploymentRefDetails(store, live, int32Set([]int32{existing.ID}), addressRefIDs); len(details) > 0 {
		return referenceInUseDetailErr("Deployment address", details)
	}
	return nil
}
