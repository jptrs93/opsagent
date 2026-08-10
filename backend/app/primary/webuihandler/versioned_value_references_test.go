package webuihandler

import (
	"errors"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestRequestedDeploymentVersionsValidatesRequestShape(t *testing.T) {
	refs := []*apigen.DeploymentConfigVersionRef{{ID: 10, Version: 3}}
	if _, err := requestedDeploymentVersions(false, refs); !errors.Is(err, InvalidReferencingDeploymentsErr) {
		t.Fatalf("list without flag error = %v", err)
	}
	if _, err := requestedDeploymentVersions(true, []*apigen.DeploymentConfigVersionRef{
		{ID: 10, Version: 3},
		{ID: 10, Version: 3},
	}); !errors.Is(err, InvalidReferencingDeploymentsErr) {
		t.Fatalf("duplicate list error = %v", err)
	}
	versions, err := requestedDeploymentVersions(true, refs)
	if err != nil || len(versions) != 1 || versions[0].ID != 10 || versions[0].Version != 3 {
		t.Fatalf("versions = %+v, err = %v", versions, err)
	}
}

func TestVersionedValueSetErrorMapsChangedReferences(t *testing.T) {
	err := versionedValueSetError(state.ErrReferencingDeploymentsChanged)
	if !errors.Is(err, ReferencingDeploymentsChangedErr) {
		t.Fatalf("mapped error = %v", err)
	}
}
