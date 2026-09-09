package deployments

import (
	"context"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"net/http"
	"slices"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var NodeSpaceNotAllowedErr = apigen.NewApiErr(
	"This node does not allow deployments from that space",
	"node_space_not_allowed", http.StatusConflict)

func validateDeployment(def *apigen.Deployment) error {
	if def.Name == "" {
		return InvalidConfigErrf("name is required")
	}
	if def.NodeID <= 0 {
		return InvalidConfigErrf("nodeId is required")
	}
	if def.SpaceID < 0 || def.SpaceID > network.MaxSpaceID {
		return InvalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	return validateNixWorkloadVersion(&def.Spec)
}

func preLockValidateDeploymentCreate(store *state.Service, secretStore *secrets.Manager, gitVersions NixSourceVerifier, ctx apigen.Context, updated *apigen.DeploymentEvent) error {
	if err := validateDeployment(&updated.Value); err != nil {
		return err
	}
	spec, err := ValidateSpec(store, secretStore, &updated.Value.Spec)
	if err != nil {
		return err
	}
	updated.Value.Spec = *spec
	if spec.WorkloadRunning() {
		return verifyRunningNixSource(gitVersions, ctx, spec)
	}
	return nil
}

func preLockValidateDeploymentUpdate(store *state.Service, secretStore *secrets.Manager, gitVersions NixSourceVerifier, ctx apigen.Context, existing *apigen.DeploymentEvent, req *apigen.DeploymentUpdateRequestV2, updated *apigen.DeploymentEvent) error {
	if req.SpecUpdate != nil {
		spec, err := ValidateSpec(store, secretStore, &updated.Value.Spec)
		if err != nil {
			return err
		}
		updated.Value.Spec = *spec
	}
	if !updated.WorkloadRunning() && !sameDesiredVersionSource(&existing.Value.Spec, &updated.Value.Spec) {
		if err := updated.SetWorkloadState("", false); err != nil {
			return InvalidConfigErrf("spec: %v", err)
		}
	}
	if updated.Value.NodeID == existing.Value.NodeID && updated.Value.SpaceID == existing.Value.SpaceID &&
		updated.Value.Name == existing.Value.Name && pq.DeploymentSpecsEqual(&updated.Value.Spec, &existing.Value.Spec) {
		return InvalidConfigErrf("nothing changed")
	}
	if updated.WorkloadRunning() && updated.WorkloadVersion() == "" {
		return InvalidConfigErrf("deployment has no version to start; set a target version")
	}
	if err := validateDeployment(&updated.Value); err != nil {
		return err
	}
	nixChanged := !sameNixBuildConfig(nixSource(&existing.Value.Spec), nixSource(&updated.Value.Spec))
	if updated.WorkloadRunning() && nixSource(&updated.Value.Spec) != nil &&
		(!existing.WorkloadRunning() || updated.WorkloadVersion() != existing.WorkloadVersion() || nixChanged) {
		return verifyRunningNixSource(gitVersions, ctx, &updated.Value.Spec)
	}
	return nil
}

func validateNodeAllowsSpace(live nodes.LiveState, nodeID, spaceID int32) error {
	node := live.Nodes[nodeID]
	if node == nil {
		return InvalidConfigErrf("node is not registered")
	}
	if !slices.Contains(node.AllowedSpaces, spaceID) {
		return NodeSpaceNotAllowedErr
	}
	return nil
}

func validateNoDuplicateIdentity(live nodes.LiveState, updated *apigen.DeploymentEvent) error {
	for _, other := range live.Deployments {
		if other.DeploymentID == updated.DeploymentID {
			continue
		}
		if storage.DeploymentKeyMatches(other.Value, updated.Value.NodeID, updated.Value.SpaceID, updated.Value.Name) {
			return DuplicateErr
		}
	}
	return nil
}

func inLockValidateDeploymentCreate(ctx context.Context, q *pq.Queries, reservations []ingressplan.Reservation, updated *apigen.DeploymentEvent) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	if internaldeploy.IsInternalIdentity(updated.Value.SpaceID, updated.Value.Name) {
		return InvalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.Value.NodeID, updated.Value.SpaceID); err != nil {
		return err
	}
	if err := ValidateNodeNetworkingClaims(live, reservations, updated.Value.NodeID, updated.DeploymentID, &updated.Value.Spec); err != nil {
		return err
	}
	if err := validateAddressEnvRefs(live, updated.Value.NodeID, updated.DeploymentID, updated.Value.SpaceID, &updated.Value.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Value.Spec, updated.Value.NodeID, updated.DeploymentID, updated.Value.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(ctx, q, &updated.Value.Spec, updated.Value.SpaceID)
}

func inLockValidateDeploymentUpdate(ctx context.Context, q *pq.Queries, reservations []ingressplan.Reservation, updated *apigen.DeploymentEvent, expectedVersion int32) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	existing := live.Deployments[updated.DeploymentID]
	if existing == nil || existing.Deleted() {
		return NotFoundErr
	}
	if existing.Version != expectedVersion {
		return InvalidConfigErrf("deployment version mismatch: deployment %d has version %d, expected %d", existing.DeploymentID, existing.Version, expectedVersion)
	}
	if existing.Value.SpaceID != updated.Value.SpaceID {
		if internaldeploy.IsInternalConfig(existing) {
			return InvalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
		}
		if existing.Value.SpaceID == 0 {
			return InvalidConfigErrf("deployments in space 0 cannot be moved")
		}
		if updated.Value.SpaceID < 1 || updated.Value.SpaceID > network.MaxSpaceID {
			return InvalidConfigErrf("spaceId must be between 1 and %d", network.MaxSpaceID)
		}
		if err := validateNoDuplicateIdentity(live, updated); err != nil {
			return err
		}
		if err := validateNodeAllowsSpace(live, updated.Value.NodeID, updated.Value.SpaceID); err != nil {
			return err
		}
		ids := Int32Set([]int32{existing.DeploymentID})
		if UsesAddressID(live, ids) {
			return AddressReferencedErr
		}
		if updated.Value.SpaceID != nodes.DefaultSpaceID && ReferencesOutsideSpace(live, ids, CrossDeploymentMountSourceIDs, updated.Value.SpaceID) {
			return MoveReferencesOutsideSpaceErr
		}
	} else {
		if internaldeploy.IsInternalConfig(existing) {
			base, err := cloneDeploymentSpec(&existing.Value.Spec)
			if err != nil {
				return err
			}
			if err := base.SetWorkloadState(updated.Value.Spec.WorkloadVersion(), updated.Value.Spec.WorkloadRunning()); err != nil {
				return InvalidConfigErrf("spec: %v", err)
			}
			if !pq.DeploymentSpecsEqual(base, &updated.Value.Spec) {
				return InvalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
			}
		}
		if internaldeploy.IsSelfConfig(existing) && !updated.Value.Spec.WorkloadRunning() {
			return InvalidConfigErrf("the opendeploy system deployment cannot be stopped")
		}
		if updated.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL &&
			UsesAddressID(live, Int32Set([]int32{existing.DeploymentID})) {
			return InvalidConfigErrf("deployment networking cannot leave virtual mode while address references exist")
		}
		if err := ValidateNodeNetworkingClaims(live, reservations, updated.Value.NodeID, updated.DeploymentID, &updated.Value.Spec); err != nil {
			return err
		}
	}
	if err := validateAddressEnvRefs(live, updated.Value.NodeID, updated.DeploymentID, updated.Value.SpaceID, &updated.Value.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Value.Spec, updated.Value.NodeID, updated.DeploymentID, updated.Value.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(ctx, q, &updated.Value.Spec, updated.Value.SpaceID)
}

func inLockValidateDeploymentDelete(ctx context.Context, q *pq.Queries, cluster NodeConnectivity, primaryNodeID, deploymentID, expectedVersion int32) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	existing := live.Deployments[deploymentID]
	if existing == nil || existing.Deleted() {
		return NotFoundErr
	}
	if existing.Version != expectedVersion {
		return InvalidConfigErrf("deployment version mismatch: deployment %d has version %d, expected %d", existing.DeploymentID, existing.Version, expectedVersion)
	}
	statuses := []apigen.ScheduledInstanceStatus{}
	for _, entry := range live.Scheduled {
		if entry.Instance.DeploymentID == existing.DeploymentID {
			statuses = append(statuses, entry.Status)
		}
	}
	if internaldeploy.IsInternalConfig(existing) {
		if !canDeleteStaleDisconnectedSystemDeployment(cluster, primaryNodeID, existing) {
			return InvalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !canDeleteDeployment(cluster, primaryNodeID, existing, statuses) {
		return InvalidConfigErrf("deployment must be stopped before deletion")
	}
	if details := RefDetails(ctx, q, live, Int32Set([]int32{existing.DeploymentID}), AddressRefIDs); len(details) > 0 {
		return ReferenceInUseDetailErr("Deployment address", details)
	}
	return nil
}
