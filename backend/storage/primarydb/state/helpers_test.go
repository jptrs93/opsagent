package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// The literal keeps the assertion independent of internaldeploy.NetproxySpec,
// so an accidental change to the shipped limit fails here.
const netproxyFileDescriptorLimit = 65_536

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

// mustCreateDeploymentForNode is a test seeding convenience: it locks, panics
// on a duplicate active identity, and creates the deployment.
func mustCreateDeploymentForNode(s *Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for _, cfg := range s.deploymentCache {
		if storage.DeploymentKeyMatches(cfg.Def, nodeID, spaceID, name) && !cfg.Deleted() {
			panic(fmt.Sprintf("deployment node=%d space=%d name=%q already exists", nodeID, spaceID, name))
		}
	}
	return s.CreateDeploymentLocked(ctx, &apigen.DeploymentDef{NodeID: nodeID, SpaceID: spaceID, Name: name, Spec: *spec})
}

func updateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) *apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	def := deploymentFromRow(s.mustLatestEventLocked(deploymentID)).Def
	def.Spec = *spec
	return s.UpdateDeploymentLocked(ctx, deploymentID, &def)
}

func moveDeploymentSpace(s *Service, ctx apigen.Context, deploymentID, spaceID int32) *apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	def := deploymentFromRow(s.mustLatestEventLocked(deploymentID)).Def
	def.SpaceID = spaceID
	return s.UpdateDeploymentLocked(ctx, deploymentID, &def)
}

func deleteDeployment(s *Service, ctx apigen.Context, deploymentID int32) *apigen.Deployment {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.DeleteDeploymentLocked(ctx, deploymentID)
}

// mustSetDeploymentWorkloadState appends a spec version with only the workload
// state changed. Test-only counterpart of the deploy-time spec update paths.
func mustSetDeploymentWorkloadState(s *Service, ctx apigen.Context, deploymentID int32, version string, running bool) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	existing := deploymentFromRow(s.mustLatestEventLocked(deploymentID))
	spec := mustDecodeDeploymentSpec(existing.Def.Spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(fmt.Sprintf("update deployment workload state: %v", err))
	}
	def := existing.Def
	def.Spec = *spec
	s.UpdateDeploymentLocked(ctx, deploymentID, &def)
}

// mustUpdateDeploymentSpec appends the given spec as a new version, preserving
// the stored workload state.
func mustUpdateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	existing := deploymentFromRow(s.mustLatestEventLocked(deploymentID))
	storedSpec := mustDecodeDeploymentSpec(spec.Encode(), int64(deploymentID), int64(existing.SpecVersion))
	if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
		panic(fmt.Sprintf("preserve deployment workload state: %v", err))
	}
	def := existing.Def
	def.Spec = *storedSpec
	s.UpdateDeploymentLocked(ctx, deploymentID, &def)
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
	s.Mu.Lock()
	defer s.Mu.Unlock()
	c, _, appendErr := s.AppendConfigVersionWithDeploymentUpdatesLocked(int32(row.ID), value, author, false, nil)
	if appendErr != nil {
		panic(fmt.Sprintf("setConfigByName append: %v", appendErr))
	}
	return c
}
