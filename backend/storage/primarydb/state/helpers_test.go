package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// The literal keeps the assertion independent of internaldeploy.NetproxySpec,
// so an accidental change to the shipped limit fails here.
const netproxyFileDescriptorLimit = 65_536

func i32ptr(v int32) *int32 { return &v }

// nonEmptySpec returns a valid spec that encodes to non-empty bytes.
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}

// mustSetDeploymentWorkloadState appends a spec version with only the workload
// state changed. Test-only counterpart of the deploy-time spec update paths.
func mustSetDeploymentWorkloadState(s *Service, ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	userID := int64(ctx.AttributionUserID())

	existing := s.mustLatestDeploymentLocked(deploymentID)
	spec := mustDecodeDeploymentSpec(existing.Spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(fmt.Sprintf("update deployment workload state: %v", err))
	}
	s.mustAppendSpecVersionLocked(existing, spec, userID)
}

// mustUpdateDeploymentSpec appends the given spec as a new version, preserving
// the stored workload state.
func mustUpdateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	userID := int64(ctx.AttributionUserID())

	if spec == nil {
		panic("deployment spec must not be nil")
	}

	existing := s.mustLatestDeploymentLocked(deploymentID)
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment workload state: %v", err))
	}
	s.mustAppendSpecVersionLocked(existing, storedSpec, userID)
}

// getAssetInRootByKey resolves an asset by key in a space's implicit root
// directory.
func getAssetInRootByKey(s *Service, spaceID int32, key string) (Asset, bool) {
	return s.GetAssetInDirectory(spaceID, 0, key)
}

// setConfigByName is a create-or-append seeding convenience: it targets the
// root directory of the default space by name.
func setConfigByName(s *Service, name, value string, author int32) *apigen.Config {
	row, err := s.q.GetConfigInDirectoryByName(context.Background(), pq.GetConfigInDirectoryByNameParams{
		SpaceID:          int64(DefaultSpaceID),
		ValueDirectoryID: 0,
		Name:             name,
	})
	if err == sql.ErrNoRows {
		c, createErr := s.CreateConfigWithVersion(name, DefaultSpaceID, 0, author, value)
		if createErr != nil {
			panic(fmt.Sprintf("setConfigByName create: %v", createErr))
		}
		return c
	}
	if err != nil {
		panic(fmt.Sprintf("GetConfigInDirectoryByName: %v", err))
	}
	c, _, appendErr := s.AppendConfigVersionWithDeploymentUpdates(int32(row.ID), value, author, false, nil)
	if appendErr != nil {
		panic(fmt.Sprintf("setConfigByName append: %v", appendErr))
	}
	return c
}
