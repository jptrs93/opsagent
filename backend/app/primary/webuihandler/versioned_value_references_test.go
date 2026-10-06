package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestRequestedDeploymentVersionsValidatesRequestShape(t *testing.T) {
	refs := []apigen.DeploymentExpectedSeq{{DeploymentID: 10, ExpectedSeq: 3}}
	if _, err := requestedDeploymentVersions(false, refs); !errors.Is(err, InvalidReferencingDeploymentsErr) {
		t.Fatalf("list without flag error = %v", err)
	}
	if _, err := requestedDeploymentVersions(true, []apigen.DeploymentExpectedSeq{
		{DeploymentID: 10, ExpectedSeq: 3},
		{DeploymentID: 10, ExpectedSeq: 3},
	}); !errors.Is(err, InvalidReferencingDeploymentsErr) {
		t.Fatalf("duplicate list error = %v", err)
	}
	versions, err := requestedDeploymentVersions(true, refs)
	if err != nil || len(versions) != 1 || versions[0].DeploymentID != 10 || versions[0].ExpectedSeq != 3 {
		t.Fatalf("versions = %+v, err = %v", versions, err)
	}
}

func TestVersionedValueSetErrorMapsChangedReferences(t *testing.T) {
	err := versionedValueSetError(values.ErrReferencingDeploymentsChanged)
	if !errors.Is(err, ReferencingDeploymentsChangedErr) {
		t.Fatalf("mapped error = %v", err)
	}
}
