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
)

var NodeSpaceNotAllowedErr = apigen.NewApiErr(
	"This node does not allow deployments from that space",
	"node_space_not_allowed", http.StatusConflict)

var NodeDrainingErr = apigen.NewApiErr(
	"This node is draining and does not accept running deployments",
	"node_draining", http.StatusConflict)

func validateDeployment(def *apigen.Deployment) error {
	if def.Name == "" {
		return InvalidConfigErrf("name is required")
	}
	if err := validateScheduling(&def.Scheduling); err != nil {
		return err
	}
	if def.SpaceID > uint64(network.MaxSpaceID) {
		return InvalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	return validateNixWorkloadVersion(def)
}

// SystemSpaceErr refuses a user deployment in space 0, which the primary
// reserves for its own deployments.
func SystemSpaceErr() error {
	return InvalidConfigErrf("spaceId must be between 1 and %d", network.MaxSpaceID)
}

func preLockValidateDeploymentCreate(q *pq.Queries, secretStore *secrets.Manager, gitVersions NixSourceVerifier, ctx apigen.Context, updated *apigen.DeploymentRecord) error {
	if updated.Deployment.SpaceID == internaldeploy.SpaceID {
		return SystemSpaceErr()
	}
	if err := validateDeployment(&updated.Deployment); err != nil {
		return err
	}
	spec, err := ValidateSpec(q, secretStore, &updated.Deployment.Spec)
	if err != nil {
		return err
	}
	updated.Deployment.Spec = *spec
	if updated.WorkloadRunning() {
		return verifyRunningNixSource(gitVersions, ctx, spec)
	}
	return nil
}

// validateScheduling accepts exactly one dedicated node until multi-node
// placement lands: the node is part of a deployment's identity, so an empty
// list is not yet a legal draft.
func validateScheduling(scheduling *apigen.Scheduling) error {
	if scheduling.Placement.Value.DedicatedNodes == nil {
		return InvalidConfigErrf("scheduling.dedicatedNodes is required")
	}
	nodes := scheduling.Placement.Value.DedicatedNodes.Nodes
	if len(nodes) != 1 {
		return InvalidConfigErrf("scheduling.dedicatedNodes.nodes must name exactly one node")
	}
	if nodes[0] == 0 {
		return InvalidConfigErrf("scheduling.dedicatedNodes.nodes: node id must be positive")
	}
	return nil
}

func preLockValidateDeploymentUpdate(q *pq.Queries, secretStore *secrets.Manager, gitVersions NixSourceVerifier, ctx apigen.Context, existing *apigen.DeploymentRecord, req *apigen.DeploymentUpdateRequest, updated *apigen.DeploymentRecord) error {
	if req.Update.Spec != nil {
		if err := validateInternalSpecUnchanged(existing, updated); err != nil {
			return err
		}
		spec, err := ValidateSpec(q, secretStore, &updated.Deployment.Spec)
		if err != nil {
			return err
		}
		updated.Deployment.Spec = *spec
	}
	if !updated.WorkloadRunning() && !sameDesiredVersionSource(&existing.Deployment.Spec, &updated.Deployment.Spec) {
		if err := updated.SetWorkloadState("", false); err != nil {
			return InvalidConfigErrf("spec: %v", err)
		}
	}
	if updated.Deployment.PlacementNodeID() != existing.Deployment.PlacementNodeID() {
		return InvalidConfigErrf("scheduling.dedicatedNodes.nodes: the node cannot be changed after creation")
	}
	if updated.WorkloadRunning() && updated.WorkloadVersion() == "" {
		return InvalidConfigErrf("deployment has no version to start; set a target version")
	}
	if err := validateDeployment(&updated.Deployment); err != nil {
		return err
	}
	nixChanged := !sameNixBuildConfig(nixSource(&existing.Deployment.Spec), nixSource(&updated.Deployment.Spec))
	if updated.WorkloadRunning() && nixSource(&updated.Deployment.Spec) != nil &&
		(!existing.WorkloadRunning() || updated.WorkloadVersion() != existing.WorkloadVersion() || nixChanged) {
		return verifyRunningNixSource(gitVersions, ctx, &updated.Deployment.Spec)
	}
	return nil
}

// validateInternalSpecUnchanged lets a system deployment's spec change only in
// its workload version.
func validateInternalSpecUnchanged(existing, updated *apigen.DeploymentRecord) error {
	if !internaldeploy.IsInternalConfig(existing) {
		return nil
	}
	base, err := cloneDeploymentSpec(&existing.Deployment.Spec)
	if err != nil {
		return err
	}
	if err := base.SetWorkloadVersion(updated.Deployment.Spec.WorkloadVersion()); err != nil {
		return InvalidConfigErrf("spec: %v", err)
	}
	if !pq.DeploymentSpecsEqual(base, &updated.Deployment.Spec) {
		return InvalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}
	return nil
}

func validateNodeAllowsSpace(live nodes.LiveState, nodeID, spaceID uint64) error {
	node := live.Nodes[nodeID]
	if node == nil {
		return InvalidConfigErrf("node is not registered")
	}
	if !slices.Contains(node.AllowedSpaces, spaceID) {
		return NodeSpaceNotAllowedErr
	}
	return nil
}

func validateNodeNotDraining(live nodes.LiveState, updated, existing *apigen.DeploymentRecord) error {
	node := live.Nodes[updated.Deployment.PlacementNodeID()]
	if node == nil || node.Status != apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_DRAINING {
		return nil
	}
	if existing != nil && existing.Deployment.PlacementNodeID() == updated.Deployment.PlacementNodeID() && !updated.WorkloadRunning() {
		return nil
	}
	return NodeDrainingErr
}

func validateNoDuplicateIdentity(live nodes.LiveState, updated *apigen.DeploymentRecord) error {
	for _, other := range live.Deployments {
		if other.Deployment.ID == updated.Deployment.ID {
			continue
		}
		if storage.DeploymentKeyMatches(other.Deployment, updated.Deployment.PlacementNodeID(), updated.Deployment.SpaceID, updated.Deployment.Name) {
			return DuplicateErr
		}
	}
	return nil
}

func inLockValidateDeploymentCreate(ctx context.Context, q *pq.Queries, reservations []ingressplan.Reservation, updated *apigen.DeploymentRecord) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	if internaldeploy.IsInternalIdentity(updated.Deployment.SpaceID, updated.Deployment.Name) {
		return InvalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if err := validateNoDuplicateIdentity(live, updated); err != nil {
		return err
	}
	if err := validateNodeAllowsSpace(live, updated.Deployment.PlacementNodeID(), updated.Deployment.SpaceID); err != nil {
		return err
	}
	if err := validateNodeNotDraining(live, updated, nil); err != nil {
		return err
	}
	if err := ValidateNodeNetworkingClaims(live, reservations, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, &updated.Deployment.Spec); err != nil {
		return err
	}
	if err := validateAddressEnvRefs(live, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, updated.Deployment.SpaceID, &updated.Deployment.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Deployment.Spec, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, updated.Deployment.SpaceID); err != nil {
		return err
	}
	if err := validateIssuedTLSNames(&updated.Deployment.Spec, updated.Deployment.ID, updated.Deployment.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(ctx, q, &updated.Deployment.Spec, updated.Deployment.SpaceID)
}

func inLockValidateDeploymentUpdate(ctx context.Context, q *pq.Queries, reservations []ingressplan.Reservation, updated *apigen.DeploymentRecord, expectedSeq int64) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	existing := live.Deployments[updated.Deployment.ID]
	if existing == nil || existing.Deleted() {
		return NotFoundErr
	}
	if expectedSeq != 0 && existing.Meta.UpdatedSeq > expectedSeq {
		return InvalidConfigErrf("deployment %d changed since it was loaded", existing.Deployment.ID)
	}
	if err := validateNodeNotDraining(live, updated, existing); err != nil {
		return err
	}
	if existing.Deployment.SpaceID != updated.Deployment.SpaceID {
		if internaldeploy.IsInternalConfig(existing) {
			return InvalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
		}
		if existing.Deployment.SpaceID == 0 {
			return InvalidConfigErrf("deployments in space 0 cannot be moved")
		}
		if updated.Deployment.SpaceID < 1 || updated.Deployment.SpaceID > uint64(network.MaxSpaceID) {
			return SystemSpaceErr()
		}
		if err := validateNoDuplicateIdentity(live, updated); err != nil {
			return err
		}
		if err := validateNodeAllowsSpace(live, updated.Deployment.PlacementNodeID(), updated.Deployment.SpaceID); err != nil {
			return err
		}
		ids := IDSet([]uint64{existing.Deployment.ID})
		if UsesAddressID(live, ids) {
			return AddressReferencedErr
		}
		if updated.Deployment.SpaceID != nodes.DefaultSpaceID && ReferencesOutsideSpace(live, ids, CrossDeploymentMountSourceIDs, updated.Deployment.SpaceID) {
			return MoveReferencesOutsideSpaceErr
		}
	} else {
		if err := validateInternalSpecUnchanged(existing, updated); err != nil {
			return err
		}
		if internaldeploy.IsSelfConfig(existing) && !updated.WorkloadRunning() {
			return InvalidConfigErrf("the opendeploy system deployment cannot be stopped")
		}
		if updated.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL &&
			UsesAddressID(live, IDSet([]uint64{existing.Deployment.ID})) {
			return InvalidConfigErrf("deployment networking cannot leave virtual mode while address references exist")
		}
		if err := ValidateNodeNetworkingClaims(live, reservations, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, &updated.Deployment.Spec); err != nil {
			return err
		}
	}
	if err := validateAddressEnvRefs(live, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, updated.Deployment.SpaceID, &updated.Deployment.Spec); err != nil {
		return err
	}
	if err := validateCrossDeploymentMountSources(live, &updated.Deployment.Spec, updated.Deployment.PlacementNodeID(), updated.Deployment.ID, updated.Deployment.SpaceID); err != nil {
		return err
	}
	if err := validateIssuedTLSNames(&updated.Deployment.Spec, updated.Deployment.ID, updated.Deployment.SpaceID); err != nil {
		return err
	}
	return validateRefSpaces(ctx, q, &updated.Deployment.Spec, updated.Deployment.SpaceID)
}

func inLockValidateDeploymentDelete(ctx context.Context, q *pq.Queries, cluster NodeConnectivity, primaryNodeID, deploymentID uint64, expectedSeq int64) error {
	live, err := nodes.ReadLiveState(ctx, q)
	if err != nil {
		return err
	}
	existing := live.Deployments[deploymentID]
	if existing == nil || existing.Deleted() {
		return NotFoundErr
	}
	if expectedSeq != 0 && existing.Meta.UpdatedSeq > expectedSeq {
		return InvalidConfigErrf("deployment %d changed since it was loaded", existing.Deployment.ID)
	}
	statuses := []apigen.ScheduledInstanceStatus{}
	for _, entry := range live.Scheduled {
		if entry.Instance.Deployment.DeploymentID == existing.Deployment.ID {
			statuses = append(statuses, entry.Status.Value)
		}
	}
	if internaldeploy.IsInternalConfig(existing) {
		if !canDeleteStaleDisconnectedSystemDeployment(cluster, primaryNodeID, existing) {
			return InvalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !canDeleteDeployment(cluster, primaryNodeID, existing, statuses) {
		return InvalidConfigErrf("deployment must be stopped before deletion")
	}
	if details := RefDetails(ctx, q, live, IDSet([]uint64{existing.Deployment.ID}), AddressRefIDs); len(details) > 0 {
		return ReferenceInUseDetailErr("Deployment address", details)
	}
	return nil
}
