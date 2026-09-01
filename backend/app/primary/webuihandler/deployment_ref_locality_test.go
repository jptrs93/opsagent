package webuihandler

import (
	"errors"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/assetstore"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func isRefOutsideSpaceErr(err error, want apigen.ApiErr) bool {
	var apiErr apigen.ApiErr
	return errors.As(err, &apiErr) && apiErr.InternalErr == want.InternalErr
}

func configEnvSpec(image string, configVersionID int32) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	id := configVersionID
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"ENDPOINT": {ConfigVersionID: &id},
	}
	return spec
}

func assetMountSpec(image string, assetVersionID int32) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Container1Spec.Runtime.AssetMounts = []*apigen.AssetMount{{
		AssetVersionID: assetVersionID, ContainerPath: "/etc/app.conf", Permission: apigen.FilePermission_READ_ONLY,
	}}
	return spec
}

func addressEnvSpec(image string, deploymentID, spaceID int32) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	id, space := deploymentID, spaceID
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"API_ADDR": {AddressDeploymentID: &id, AddressSpaceID: &space},
	}
	return spec
}

func crossMountSpec(image string, sourceDeploymentID int32) apigen.DeploymentSpec {
	spec := remoteDeploymentSpec(image, hostNetworking())
	spec.Container1Spec.Runtime.CrossDeploymentMounts = []*apigen.CrossDeploymentMount{{
		DeploymentID: sourceDeploymentID, ContainerPath: "/mnt/shared", Permission: apigen.FilePermission_READ_ONLY,
	}}
	return spec
}

func TestDeploymentRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalConfig, err := h.Store.CreateConfigWithVersion("global-endpoint", state.DefaultSpaceID, 0, 0, "https://global")
	if err != nil {
		t.Fatalf("creating global config: %v", err)
	}
	prodConfig, err := h.Store.CreateConfigWithVersion("prod-endpoint", prod.ID, 0, 0, "https://prod")
	if err != nil {
		t.Fatalf("creating prod config: %v", err)
	}

	create := func(name string, spaceID, configVersionID int32) (*apigen.Deployment, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			NodeID: node.ID,
			Spec:   configEnvSpec("nginx", configVersionID),
		})
	}

	if _, err := create("own-space", prod.ID, prodConfig.ValueVersions[0].ID); err != nil {
		t.Fatalf("own-space config ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, globalConfig.ValueVersions[0].ID); err != nil {
		t.Fatalf("global config ref rejected: %v", err)
	}
	if _, err := create("global-deploy", state.DefaultSpaceID, prodConfig.ValueVersions[0].ID); !isRefOutsideSpaceErr(err, ConfigRefOutsideSpaceErr) {
		t.Fatalf("global deployment with prod config err = %v, want %v", err, ConfigRefOutsideSpaceErr)
	}
}

func TestDeploymentAssetRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	h.Assets = &assetstore.Store{DB: h.Store}
	globalAsset, err := createTestAsset(h, apigen.Context{}, "global.conf", state.DefaultSpaceID, 0, []byte("g"))
	if err != nil {
		t.Fatalf("creating global asset: %v", err)
	}
	prodAsset, err := createTestAsset(h, apigen.Context{}, "prod.conf", prod.ID, 0, []byte("p"))
	if err != nil {
		t.Fatalf("creating prod asset: %v", err)
	}

	create := func(name string, spaceID, assetVersionID int32) (*apigen.Deployment, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			NodeID: node.ID,
			Spec:   assetMountSpec("nginx", assetVersionID),
		})
	}

	if _, err := create("own-space", prod.ID, prodAsset.ID); err != nil {
		t.Fatalf("own-space asset ref rejected: %v", err)
	}
	if _, err := create("global-ref", prod.ID, globalAsset.ID); err != nil {
		t.Fatalf("global asset ref rejected: %v", err)
	}
	if _, err := create("global-deploy", state.DefaultSpaceID, prodAsset.ID); !isRefOutsideSpaceErr(err, AssetRefOutsideSpaceErr) {
		t.Fatalf("global deployment with prod asset err = %v, want %v", err, AssetRefOutsideSpaceErr)
	}
}

func TestDeploymentAddressRefsScopedToOwnOrGlobalSpace(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSpec := remoteDeploymentSpec("api", virtualNetworking())
	globalTarget := createTestDeployment(h.Store, "primary", state.DefaultSpaceID, "global-api", &globalSpec)
	prodSpec := remoteDeploymentSpec("api", virtualNetworking())
	prodTarget := createTestDeployment(h.Store, "primary", prod.ID, "prod-api", &prodSpec)

	create := func(name string, spaceID int32, target *apigen.Deployment) (*apigen.Deployment, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			NodeID: node.ID,
			Spec:   addressEnvSpec("nginx", target.ID, target.SpaceID),
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
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	globalSpec := remoteDeploymentSpec("db", hostNetworking())
	globalSource := createTestDeployment(h.Store, "primary", state.DefaultSpaceID, "global-db", &globalSpec)
	prodSpec := remoteDeploymentSpec("db", hostNetworking())
	prodSource := createTestDeployment(h.Store, "primary", prod.ID, "prod-db", &prodSpec)

	create := func(name string, spaceID, sourceID int32) (*apigen.Deployment, error) {
		return h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
			SpaceID: spaceID, Name: name,
			NodeID: node.ID,
			Spec:   crossMountSpec("nginx", sourceID),
		})
	}

	if _, err := create("own-space", prod.ID, prodSource.ID); err != nil {
		t.Fatalf("own-space mount source rejected: %v", err)
	}
	if _, err := create("global-ref", staging.ID, globalSource.ID); err != nil {
		t.Fatalf("global mount source rejected: %v", err)
	}
	if _, err := create("staging-deploy", staging.ID, prodSource.ID); err == nil {
		t.Fatal("staging deployment mounting prod source accepted, want error")
	}
}

func TestDeploymentSpaceMoveRevalidatesRefLocality(t *testing.T) {
	h, node := newSecretLocalityHandler(t)
	prod, err := h.Store.CreateSpace("prod")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	staging, err := h.Store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	prodConfig, err := h.Store.CreateConfigWithVersion("prod-endpoint", prod.ID, 0, 0, "https://prod")
	if err != nil {
		t.Fatalf("creating prod config: %v", err)
	}
	referrer, err := h.PostV1DeploymentsCreate(apigen.Context{}, &apigen.DeploymentCreateRequest{
		SpaceID: prod.ID, Name: "web",
		NodeID: node.ID,
		Spec:   configEnvSpec("nginx", prodConfig.ValueVersions[0].ID),
	})
	if err != nil {
		t.Fatalf("creating referencing deployment: %v", err)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: referrer.ID, ExpectedVersion: referrer.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID},
	}); !isRefOutsideSpaceErr(err, ConfigRefOutsideSpaceErr) {
		t.Fatalf("move with prod config ref err = %v, want %v", err, ConfigRefOutsideSpaceErr)
	}

	sourceSpec := remoteDeploymentSpec("db", hostNetworking())
	source := createTestDeployment(h.Store, "primary", prod.ID, "db", &sourceSpec)
	mounterSpec := crossMountSpec("nginx", source.ID)
	createTestDeployment(h.Store, "primary", prod.ID, "mounter", &mounterSpec)
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: source.ID, ExpectedVersion: source.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: staging.ID},
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("mounted source move to staging err = %v, want %v", err, MoveReferencesOutsideSpaceErr)
	}
	if _, err := h.PostV2DeploymentsUpdate(apigen.Context{}, &apigen.DeploymentUpdateRequestV2{
		DeploymentID: source.ID, ExpectedVersion: source.Version + 1,
		AssignedSpaceUpdate: &apigen.AssignedSpaceUpdate{SpaceID: state.DefaultSpaceID},
	}); err != nil {
		t.Fatalf("mounted source move to global: %v", err)
	}
}
