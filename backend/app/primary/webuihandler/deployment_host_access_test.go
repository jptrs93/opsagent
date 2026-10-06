package webuihandler

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
)

const (
	hostMountPermission   = apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS
	hostNetworkPermission = apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK
)

func hostAccessSpec(mounts, network bool) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec("nginx", virtualNetworking())
	spec.Workload.Value.Container.Version = "1.29"
	if mounts {
		spec.Workload.Value.Container.Runtime.Mounts = []apigen.HostMount{{
			HostPath: "/srv/data", ContainerPath: "/data", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY,
		}}
	}
	if network {
		spec.Networking = hostNetworking()
	}
	return spec
}

func grantDeploymentAccess(t *testing.T, h *Handler, userID, spaceID, deploymentID uint64, delegated bool, verbs ...apigen.AuthzVerb) {
	t.Helper()
	refs := allEntityRefs()
	if deploymentID != 0 {
		refs = exactRefs(deploymentRef(deploymentID))
	}
	_, err := h.Authz.CreateGrant(ruleGrant(userID, &apigen.AuthzRule{
		Effect: allowEffect(delegated),
		Selector: apigen.AuthzSelector{
			Permissions: exactVerbs(verbs...),
			Spaces:      exactSpaces(spaceID),
			EntityTypes: exactEntityTypes(eDeployment),
			EntityRefs:  refs,
		},
	}), 0)
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
	node := ensureTestNode(h.Store, "primary", "primary")
	for _, caller := range []struct {
		name      string
		id        uint64
		delegated bool
	}{
		{"cluster_admin", 1, false}, {"space_admin", 2, false},
		{"cluster_agent", 1, true}, {"space_agent", 2, true},
	} {
		for features := 0; features < 4; features++ {
			t.Run(fmt.Sprintf("%s/%d", caller.name, features), func(t *testing.T) {
				created, err := h.deploymentsCreate(enforceCtx(caller.id, caller.delegated), &apigen.DeploymentCreateRequest{
					Name: fmt.Sprintf("%s_%d", caller.name, features), SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID),
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
			node := ensureTestNode(h.Store, "primary", "primary")
			if permissions&1 != 0 {
				grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostMountPermission)
			}
			if permissions&2 != 0 {
				grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostNetworkPermission)
			}
			for features := 0; features < 4; features++ {
				_, err := h.deploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
					Name: fmt.Sprintf("web_%d", features), SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID),
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
	node := ensureTestNode(h.Store, "primary", "primary")
	grantDeploymentAccess(t, h, 3, nodes.DefaultSpaceID, 0, false, vView, hostMountPermission, hostNetworkPermission)
	request := &apigen.DeploymentCreateRequest{Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: hostAccessSpec(true, true)}
	if _, err := h.deploymentsCreate(enforceCtx(3, false), request); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("host permissions without create: %v", err)
	}
	created, err := h.deploymentsCreate(enforceCtx(1, false), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.deploymentsUpdate(enforceCtx(3, false), &apigen.DeploymentUpdateRequest{
		DeploymentID: created.Deployment.ID, ExpectedSeq: created.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}},
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
				node := ensureTestNode(h.Store, "primary", "primary")
				spec := hostAccessSpec(feature.mounts, feature.network)
				running := kind == "stop"
				created, err := h.deploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{
					Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(running, node.ID), Spec: spec,
				})
				if err != nil {
					t.Fatal(err)
				}
				req := &apigen.DeploymentUpdateRequest{DeploymentID: created.Deployment.ID, ExpectedSeq: created.Meta.UpdatedSeq}
				switch kind {
				case "version":
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}
				case "start":
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: true}}
				case "stop":
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{RunningOnly: &apigen.RunningOnlyUpdate{DesiredRunning: false}}
				case "spec":
					next := hostAccessSpec(feature.mounts, feature.network)
					next.Workload.Value.Container.Runtime.OverrideCommand = []string{"/bin/sh", "-c", "cat /data/file"}
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: next}}
				case "remove_host_access":
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: hostAccessSpec(false, false)}}
				case "space":
					grantDeploymentAccess(t, h, 2, staging.ID, 0, false, vCreate, hostMountPermission, hostNetworkPermission)
					req.Update = apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}}
				}
				for _, ctx := range []apigen.Context{enforceCtx(2, false), enforceCtx(1, true), enforceCtx(2, true)} {
					_, err := h.deploymentsUpdate(ctx, req)
					requireHostAccessDenied(t, err, feature.name)
				}
				if got := h.deploymentByID(created.Deployment.ID); got.Meta.Version != created.Meta.Version {
					t.Fatal("denied updates changed the deployment")
				}
				if _, err := h.deploymentsUpdate(enforceCtx(1, false), req); err != nil {
					t.Fatalf("cluster admin update: %v", err)
				}
			})
		}
	}
}

func TestDeploymentHostAccessChecksProposedSpecAndScope(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	created, err := h.deploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: hostAccessSpec(false, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequest{DeploymentID: created.Deployment.ID, ExpectedSeq: created.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: hostAccessSpec(true, true)}}}
	_, err = h.deploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	// A grant for another space or deployment cannot authorize this update.
	grantDeploymentAccess(t, h, 2, staging.ID, 0, false, hostMountPermission, hostNetworkPermission)
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.Deployment.ID+1, false, hostMountPermission, hostNetworkPermission)
	_, err = h.deploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.Deployment.ID, false, hostMountPermission)
	_, err = h.deploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_network")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.Deployment.ID, false, hostNetworkPermission)
	updated, err := h.deploymentsUpdate(enforceCtx(2, false), req)
	if err != nil {
		t.Fatalf("explicit deployment grants: %v", err)
	}
	req = &apigen.DeploymentUpdateRequest{DeploymentID: updated.Deployment.ID, ExpectedSeq: updated.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}}
	_, err = h.deploymentsUpdate(enforceCtx(2, true), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, created.Deployment.ID, true, hostMountPermission, hostNetworkPermission)
	if _, err := h.deploymentsUpdate(enforceCtx(2, true), req); err != nil {
		t.Fatalf("explicitly delegated host access: %v", err)
	}
}

func TestDeploymentHostAccessRequiredInDestinationSpace(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	grantDeploymentAccess(t, h, 2, nodes.DefaultSpaceID, 0, false, hostMountPermission, hostNetworkPermission)
	grantDeploymentAccess(t, h, 2, staging.ID, 0, false, vCreate)
	created, err := h.deploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: hostAccessSpec(true, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequest{DeploymentID: created.Deployment.ID, ExpectedSeq: created.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}}}
	_, err = h.deploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
	grantDeploymentAccess(t, h, 2, staging.ID, created.Deployment.ID, false, hostMountPermission)
	_, err = h.deploymentsUpdate(enforceCtx(2, false), req)
	requireHostAccessDenied(t, err, "use_host_network")
	grantDeploymentAccess(t, h, 2, staging.ID, created.Deployment.ID, false, hostNetworkPermission)
	if _, err := h.deploymentsUpdate(enforceCtx(2, false), req); err != nil {
		t.Fatalf("move with both destination permissions: %v", err)
	}
}

func TestDeploymentHostAccessGlobalDenyAndVisibility(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	created, err := h.deploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{
		Name: "web", SpaceID: staging.ID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: hostAccessSpec(true, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &apigen.DeploymentUpdateRequest{DeploymentID: created.Deployment.ID, ExpectedSeq: created.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}}
	if _, err := h.deploymentsUpdate(enforceCtx(2, false), req); !errors.Is(err, deployments.NotFoundErr) {
		t.Fatalf("hidden deployment: %v", err)
	}
	_, err = h.Authz.CreateGlobalRule("deny_host_mounts", &apigen.AuthzRule{
		Effect: denyEffect(false),
		Selector: apigen.AuthzSelector{
			Permissions: exactVerbs(hostMountPermission),
			Spaces:      allSpacesExcluding(),
			EntityTypes: exactEntityTypes(eDeployment),
			EntityRefs:  allEntityRefs(),
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.deploymentsUpdate(enforceCtx(1, false), req)
	requireHostAccessDenied(t, err, "use_host_mounts")
}

func TestDeploymentHostAccessDefaultsAndManagedVolumes(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	spec := hostAccessSpec(false, false)
	spec.Networking = apigen.NetworkingConfig{}
	if _, err := h.deploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "source", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec,
	}); err == nil || !strings.Contains(err.Error(), "networking.mode") {
		t.Fatalf("create with unspecified networking mode: err = %v, want a mode rejection", err)
	}
	spec.Networking = virtualNetworking()
	source, err := h.deploymentsCreate(enforceCtx(2, false), &apigen.DeploymentCreateRequest{
		Name: "source", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec,
	})
	if err != nil {
		t.Fatalf("create with virtual networking: %v", err)
	}
	spec.Workload.Value.Container.Runtime.CrossDeploymentMounts = []apigen.CrossDeploymentMount{{
		DeploymentID: source.Deployment.ID, ContainerPath: "/data", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE,
	}}
	consumer, err := h.deploymentsCreate(enforceCtx(2, true), &apigen.DeploymentCreateRequest{
		Name: "consumer", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, node.ID), Spec: spec,
	})
	if err != nil {
		t.Fatalf("managed volumes without host permissions: %v", err)
	}
	if _, err := h.deploymentsUpdate(enforceCtx(2, true), &apigen.DeploymentUpdateRequest{
		DeploymentID: consumer.Deployment.ID, ExpectedSeq: consumer.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}},
	}); err != nil {
		t.Fatalf("ordinary update without host permissions: %v", err)
	}
}
