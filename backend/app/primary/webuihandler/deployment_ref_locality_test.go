package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func isRefOutsideSpaceErr(err error, want apigen.ApiErr) bool {
	var apiErr apigen.ApiErr
	return errors.As(err, &apiErr) && apiErr.InternalErr == want.InternalErr
}

func configEnvSpec(image string, ref apigen.ValueRef) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{
		"ENDPOINT": configEnv(ref),
	}
	return spec
}

func assetMountSpec(image string, ref apigen.ValueRef) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Workload.Value.Container.Runtime.AssetMounts = []apigen.AssetMount{{
		Asset: apigen.AssetRef{AssetID: ref.ID, Version: ref.Version}, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY,
	}}
	return spec
}

func addressEnvSpec(image string, deploymentID, spaceID uint64) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{
		"API_ADDR": addressEnv(deploymentID, spaceID),
	}
	return spec
}

func crossMountSpec(image string, sourceDeploymentID uint64) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Workload.Value.Container.Runtime.CrossDeploymentMounts = []apigen.CrossDeploymentMount{{
		DeploymentID: sourceDeploymentID, ContainerPath: "/mnt/shared", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY,
	}}
	return spec
}

func TestDeploymentRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalConfig, err := values.CreateConfig(h.Store, "global-endpoint", nodes.DefaultSpaceID, 0, 0, "https://global")
	if err != nil {
		t.Fatalf("creating global config: %v", err)
	}
	prodConfig, err := values.CreateConfig(h.Store, "prod-endpoint", prod.ID, 0, 0, "https://prod")
	if err != nil {
		t.Fatalf("creating prod config: %v", err)
	}

	create := func(name string, spaceID uint64, ref apigen.ValueRef) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec:       configEnvSpec("nginx", ref),
		})
	}

	if _, err := create("own-space", prod.ID, statetest.ValueVersions(h.Store, prodConfig)[0].Ref); err != nil {
		t.Fatalf("own-space config ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, statetest.ValueVersions(h.Store, globalConfig)[0].Ref); err != nil {
		t.Fatalf("global config ref rejected: %v", err)
	}
	if _, err := create("global-deploy", nodes.DefaultSpaceID, statetest.ValueVersions(h.Store, prodConfig)[0].Ref); !isRefOutsideSpaceErr(err, deployments.ConfigRefOutsideSpaceErr) {
		t.Fatalf("global deployment with prod config err = %v, want %v", err, deployments.ConfigRefOutsideSpaceErr)
	}
}

func TestDeploymentAssetRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	h.Assets = testAssetStore(t, h)
	globalAsset, err := createTestAsset(h, apigen.Context{}, "global.conf", nodes.DefaultSpaceID, 0, []byte("g"))
	if err != nil {
		t.Fatalf("creating global asset: %v", err)
	}
	prodAsset, err := createTestAsset(h, apigen.Context{}, "prod.conf", prod.ID, 0, []byte("p"))
	if err != nil {
		t.Fatalf("creating prod asset: %v", err)
	}

	create := func(name string, spaceID uint64, ref apigen.ValueRef) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec:       assetMountSpec("nginx", ref),
		})
	}

	if _, err := create("own-space", prod.ID, assetEventRef(prodAsset)); err != nil {
		t.Fatalf("own-space asset ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, assetEventRef(globalAsset)); err != nil {
		t.Fatalf("global asset ref rejected: %v", err)
	}
	if _, err := create("global-deploy", nodes.DefaultSpaceID, assetEventRef(prodAsset)); !isRefOutsideSpaceErr(err, deployments.AssetRefOutsideSpaceErr) {
		t.Fatalf("global deployment with prod asset err = %v, want %v", err, deployments.AssetRefOutsideSpaceErr)
	}
}

func TestDeploymentAddressRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSpec := remoteDeploymentSpec("api", virtualNetworking())
	globalTarget := createTestDeployment(h.Store, "primary", nodes.DefaultSpaceID, "global-api", &globalSpec)
	prodSpec := remoteDeploymentSpec("api", virtualNetworking())
	prodTarget := createTestDeployment(h.Store, "primary", prod.ID, "prod-api", &prodSpec)

	create := func(name string, spaceID uint64, target *apigen.DeploymentRecord) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec:       addressEnvSpec("nginx", target.Deployment.ID, target.Deployment.SpaceID),
		})
	}

	if _, err := create("own-space", prod.ID, prodTarget); err != nil {
		t.Fatalf("own-space address ref rejected: %v", err)
	}
	if _, err := create("global-ref", staging.ID, globalTarget); err != nil {
		t.Fatalf("global address ref rejected: %v", err)
	}
	if _, err := create("staging-deploy", staging.ID, prodTarget); err == nil {
		t.Fatal("staging deployment with prod address ref accepted, want error")
	}
}

func TestDeploymentCrossMountSourcesScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSpec := remoteDeploymentSpec("db", hostNetworking())
	globalSource := createTestDeployment(h.Store, "primary", nodes.DefaultSpaceID, "global-db", &globalSpec)
	prodSpec := remoteDeploymentSpec("db", hostNetworking())
	prodSource := createTestDeployment(h.Store, "primary", prod.ID, "prod-db", &prodSpec)

	create := func(name string, spaceID, sourceID uint64) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			Scheduling: apigen.DedicatedScheduling(false, node.ID),
			Spec:       crossMountSpec("nginx", sourceID),
		})
	}

	if _, err := create("own-space", prod.ID, prodSource.Deployment.ID); err != nil {
		t.Fatalf("own-space mount source rejected: %v", err)
	}
	if _, err := create("global-ref", staging.ID, globalSource.Deployment.ID); err != nil {
		t.Fatalf("global mount source rejected: %v", err)
	}
	if _, err := create("staging-deploy", staging.ID, prodSource.Deployment.ID); err == nil {
		t.Fatal("staging deployment mounting prod source accepted, want error")
	}
}

func TestDeploymentSpaceMoveRevalidatesRefLocality(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := nodes.CreateSpace(h.Store, "prod", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := nodes.CreateSpace(h.Store, "staging", 0)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	prodConfig, err := values.CreateConfig(h.Store, "prod-endpoint", prod.ID, 0, 0, "https://prod")
	if err != nil {
		t.Fatalf("creating prod config: %v", err)
	}
	referrer, err := h.deploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: prod.ID, Name: "web",
		Scheduling: apigen.DedicatedScheduling(false, node.ID),
		Spec:       configEnvSpec("nginx", statetest.ValueVersions(h.Store, prodConfig)[0].Ref),
	})
	if err != nil {
		t.Fatalf("creating referencing deployment: %v", err)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: referrer.Deployment.ID, ExpectedSeq: referrer.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}},
	}); !isRefOutsideSpaceErr(err, deployments.ConfigRefOutsideSpaceErr) {
		t.Fatalf("move with prod config ref err = %v, want %v", err, deployments.ConfigRefOutsideSpaceErr)
	}

	sourceSpec := remoteDeploymentSpec("db", hostNetworking())
	source := createTestDeployment(h.Store, "primary", prod.ID, "db", &sourceSpec)
	mounterSpec := crossMountSpec("nginx", source.Deployment.ID)
	createTestDeployment(h.Store, "primary", prod.ID, "mounter", &mounterSpec)
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: source.Deployment.ID, ExpectedSeq: source.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID}},
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("mounted source move to staging err = %v, want %v", err, deployments.MoveReferencesOutsideSpaceErr)
	}
	if _, err := h.deploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequest{
		DeploymentID: source.Deployment.ID, ExpectedSeq: source.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{AssignedSpace: &apigen.AssignedSpaceUpdate{SpaceID: nodes.DefaultSpaceID}},
	}); err != nil {
		t.Fatalf("mounted source move to global: %v", err)
	}
}

func assetEventRef(e *pq.AssetEvent) apigen.ValueRef {
	return apigen.ValueRef{ID: e.AssetID, Version: e.ValueVersion}
}
