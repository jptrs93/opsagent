package webuihandler

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const (
	hostMountPermission   = apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS
	hostNetworkPermission = apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK
)

func hostAccessSpec(mounts, network bool) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec("nginx", virtualNetworking())
	spec.Container1Spec.Version = "1.29"
	if mounts {
		spec.Container1Spec.Runtime.Mounts = []*apigen.CustomHostMount{{
			HostPath: "/srv/data", ContainerPath: "/data", Permission: apigen.FilePermission_READ_ONLY,
		}}
	}
	if network {
		spec.Networking = hostNetworking()
	}
	return spec
}

func grantDeploymentAccess(t *testing.T, h *Handler, userID int64, spaceID, deploymentID int32, delegated bool, verbs ...apigen.AuthzVerb) {
	t.Helper()
	permissions := &apigen.AuthzSelector{}
	for _, verb := range verbs {
		permissions.Include = append(permissions.Include, int64(verb))
	}
	refs := &apigen.AuthzSelector{Wildcard: true}
	if deploymentID != 0 {
		refs = &apigen.AuthzSelector{Include: []int64{int64(deploymentID)}}
	}
	_, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{UserID: userID, Grant: &apigen.AuthzGrant{Rule: &apigen.AuthzRule{
		Permissions:       permissions,
		Spaces:            &apigen.AuthzSelector{Include: []int64{int64(spaceID)}},
		EntityTypes:       &apigen.AuthzSelector{Include: []int64{int64(eDeployment)}},
		EntityRefs:        refs,
		DelegationAllowed: delegated,
	}}})
	if err != nil {
		t.Fatalf("grant deployment access: %v", err)
	}
}

func requireHostAccessDenied(t *testing.T, err error, permission string) {
	t.Helper()
	var apiErr apigen.ApiErr
	if !errors.Is(err, AccessDeniedErr) || !errors.As(err, &apiErr) || !strings.Contains(apiErr.DisplayErr, permission) {
		t.Fatalf("error = %v, want access denied naming %s", err, permission)
	}
}

func TestDeploymentCreateHostAccessDefaults(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	for _, caller := range []struct {
		name      string
		id        int32
		delegated bool
	}{
		{"cluster_admin", 1, false}, {"space_admin", 2, false},
		{"cluster_agent", 1, true}, {"space_agent", 2, true},
	} {
		for features := 0; features < 4; features++ {
			t.Run(fmt.Sprintf("%s/%d", caller.name, features), func(t *testing.T) {
				created, err := h.PostV1DeploymentsCreate(enforceCtx(caller.id, caller.delegated), &apigen.DeploymentCreateRequest{
					Name: fmt.Sprintf("%s_%d", caller.name, features), SpaceID: nodes.DefaultSpaceID, NodeID: node.ID,
					Spec: hostAccessSpec(features&1 != 0, features&2 != 0),
				})
				if features == 0 || (caller.id == 1 && !caller.delegated) {
					if err != nil || created == nil {
						t.Fatalf("create: %v", err)
					}
				} else if features&1 != 0 {
					requireHostAccessDenied(t, err, "use_host_mounts")
				} else {
					requireHostAccessDenied(t, err, "use_host_network")
				}
			})
		}
	}
}

func TestDeploymentHostPermissionsAreIndependentAndAdditional(t *testing.T) {
	for permissions := 0; permissions < 4; permissions++ {
		t.Run(fmt.Sprintf("permissions_%d", permissions), func(t *testing.T) {
			h, _ := newEnforcementTestHandler(t)
			node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
			if permissions&1 != 0 {
				grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostMountPermission)
			}
			if permissions&2 != 0 {
				grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostNetworkPermission)
			}
			for features := 0; features < 4; features++ {
				_, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
					Name: fmt.Sprintf("web_%d", features), SpaceID: nodes.DefaultSpaceID, NodeID: node.ID,
					Spec: hostAccessSpec(features&1 != 0, features&2 != 0),
				})
				if features & ^permissions == 0 {
					if err != nil {
						t.Fatalf("features %d: %v", features, err)
					}
				} else if features&1 != 0 && permissions&1 == 0 {
					requireHostAccessDenied(t, err, "use_host_mounts")
				} else {
					requireHostAccessDenied(t, err, "use_host_network")
				}
			}
		})
	}

	h, _ := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	grantDeploymentAccess(t, h, 3, nodes.DefaultSpaceID, 0, false, vView, hostMountPermission, hostNetworkPermission)
	request := &apigen.DeploymentCreateRequest{Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: hostAccessSpec(true, true)}
	if _, err := h.PostV1DeploymentsCreate(enforceCtx(3, false), request); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("host permissions without create: %v", err)
	}
	created, err := h.PostV1DeploymentsCreate(enforceCtx(1, false), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostV2DeploymentsUpdate(enforceCtx(3, false), &apigen.DeploymentUpdateRequestV2{
		DeploymentID: created.DeploymentID, ExpectedVersion: created.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"},
	}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("host permissions without update: %v", err)
	}
}

func TestDeploymentHostAccessChecksEveryUpdateKind(t *testing.T) {
	for _, feature := range []struct {
		name            string
		mounts, network bool
	}{
		{"use_host_mounts", true, false}, {"use_host_network", false, true},
	} {
		for _, kind := range []string{"version", "start", "stop", "spec", "remove_host_access", "space"} {
			t.Run(feature.name+"/"+kind, func(t *testing.T) {
				h, staging := newEnforcementTestHandler(t)
				node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
				spec := hostAccessSpec(feature.mounts, feature.network)
				spec.Container1Spec.Running = kind == "stop"
				created, err := h.PostV1DeploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{
					Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: spec,
				})
				if err != nil {
					t.Fatal(err)
				}
				req := &apigen.DeploymentUpdateRequestV2{DeploymentID: created.DeploymentID, ExpectedVersion: created.Version + 1}
				switch kind {
				case "version":
					req.VersionOnlyUpdate = &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}
				case "start":
					req.RunningOnlyUpdate = &apigen.RunningOnlyUpdate{DesiredRunning: true}
				case "stop":
					req.RunningOnlyUpdate = &apigen.RunningOnlyUpdate{DesiredRunning: false}
				case "spec":
					next := hostAccessSpec(feature.mounts, feature.network)
					next.Container1Spec.Runtime.OverrideCommand = []string{"/bin/sh", "-c", "cat /data/file"}
					req.SpecUpdate = &apigen.SpecUpdate{Spec: next}
				case "remove_host_access":
					req.SpecUpdate = &apigen.SpecUpdate{Spec: hostAccessSpec(false, false)}
				case "space":
					grantDeploymentAccess(t, h, 2, staging.ID, 0, false, vCreate, hostMountPermission, hostNetworkPermission)
					req.AssignedSpaceUpdate = &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}
				}
				for _, ctx := range []apigen.Context{enforceCtx(2, false), enforceCtx(1, true), enforceCtx(2, true)} {
					_, err := h.PostV2DeploymentsUpdate(ctx, req)
					requireHostAccessDenied(t, err, feature.name)
				}
				if got := h.deploymentByID(created.DeploymentID); got.Version != created.Version {
					t.Fatal("denied updates changed the deployment")
				}
				if _, err := h.PostV2DeploymentsUpdate(enforceCtx(1, false), req); err != nil {
					t.Fatalf("cluster admin update: %v", err)
				}
			})
		}
	}
}

func TestDeploymentHostAccessChecksProposedSpecAndScope(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	created, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: hostAccessSpec(false, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequestV2{DeploymentID: created.DeploymentID, ExpectedVersion: created.Version + 1,
		SpecUpdate: &apigen.SpecUpdate{Spec: hostAccessSpec(true, true)}}
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	// A grant for another space or deployment cannot authorize this update.
	grantDeploymentAccess(t, h, 2, staging.ID, 0, false, hostMountPermission, hostNetworkPermission)
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.DeploymentID+1, false, hostMountPermission, hostNetworkPermission)
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.DeploymentID, false, hostMountPermission)
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_network")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.DeploymentID, false, hostNetworkPermission)
	updated, err := h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	if err != nil {
		t.Fatalf("explicit deployment grants: %v", err)
	}
	req = &apigen.DeploymentUpdateRequestV2{DeploymentID: updated.DeploymentID, ExpectedVersion: updated.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, true), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.DeploymentID, true, hostMountPermission, hostNetworkPermission)
	if _, err := h.PostV2DeploymentsUpdate(enforceCtx(2, true), req); err != nil {
		t.Fatalf("explicitly delegated host access: %v", err)
	}
}

func TestDeploymentHostAccessRequiredInDestinationSpace(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostMountPermission, hostNetworkPermission)
	grantDeploymentAccess(t, h, 2, staging.ID, 0, false, vCreate)
	created, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: hostAccessSpec(true, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequestV2{DeploymentID: created.DeploymentID, ExpectedVersion: created.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}}
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, staging.ID, created.DeploymentID, false, hostMountPermission)
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_network")
	grantDeploymentAccess(t, h, 2, staging.ID, created.DeploymentID, false, hostNetworkPermission)
	if _, err := h.PostV2DeploymentsUpdate(enforceCtx(2, false), req); err != nil {
		t.Fatalf("move with both destination permissions: %v", err)
	}
}

func TestDeploymentHostAccessGlobalDenyAndVisibility(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	created, err := h.PostV1DeploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: staging.ID, NodeID: node.ID, Spec: hostAccessSpec(true, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequestV2{DeploymentID: created.DeploymentID, ExpectedVersion: created.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}
	if _, err := h.PostV2DeploymentsUpdate(enforceCtx(2, false), req); !errors.Is(err, deployments.NotFoundErr) {
		t.Fatalf("hidden deployment: %v", err)
	}
	_, err = h.Authz.CreateGlobalRule("deny_host_mounts", &apigen.AuthzGlobalRule{
		Deny: true, Permissions: &apigen.AuthzSelector{Include: []int64{int64(hostMountPermission)}},
		Spaces:      &apigen.AuthzSelector{Wildcard: true},
		EntityTypes: &apigen.AuthzSelector{Include: []int64{int64(eDeployment)}},
		EntityRefs:  &apigen.AuthzSelector{Wildcard: true},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(1, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
}

func TestDeploymentHostAccessDefaultsAndManagedVolumes(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	spec := hostAccessSpec(false, false)
	spec.Networking = apigen.NetworkingConfig{}
	source, err := h.PostV1DeploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "source", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: spec,
	})
	if err != nil {
		t.Fatalf("create with default networking: %v", err)
	}
	if source.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
		t.Fatal("new unspecified networking did not normalize to virtual")
	}
	spec.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{
		DeploymentID: source.DeploymentID, ContainerPath: "/data", Permission: apigen.FilePermission_READ_WRITE,
	}}
	consumer, err := h.PostV1DeploymentsCreate(enforceCtx(2, true), &apigen.DeploymentCreateRequest{
		Name: "consumer", SpaceID: nodes.DefaultSpaceID, NodeID: node.ID, Spec: spec,
	})
	if err != nil {
		t.Fatalf("managed volumes without host permissions: %v", err)
	}
	if _, err := h.PostV2DeploymentsUpdate(enforceCtx(2, true), &apigen.DeploymentUpdateRequestV2{
		DeploymentID: consumer.DeploymentID, ExpectedVersion: consumer.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"},
	}); err != nil {
		t.Fatalf("ordinary update without host permissions: %v", err)
	}
	// Seed an old stored spec without normalizing it through today's create
	// validator. The runner uses the host network when a saved mode is unset.
	legacy := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{})
	old := statetest.MustCreateDeploymentForNode(h.Store, enforceCtx(1, false), nodes.DefaultSpaceID, "legacy", node.ID, &legacy)
	_, err = h.PostV2DeploymentsUpdate(enforceCtx(2, false), &apigen.DeploymentUpdateRequestV2{
		DeploymentID: old.DeploymentID, ExpectedVersion: old.Version + 1,
		VersionOnlyUpdate: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"},
	})
	requireHostAccessDenied(t, err, "use_host_network")
}
