package webuihandler

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
)

var InvalidReferencingDeploymentsErr = apigen.NewApiErr(
	"Referencing deployments must contain unique positive IDs",
	"invalid_referencing_deployments",
	http.StatusBadRequest,
)

var ReferencingDeploymentsChangedErr = apigen.NewApiErr(
	"Referencing deployments changed; refresh and try again",
	"referencing_deployments_changed",
	http.StatusConflict,
)

func requestedDeploymentVersions(update bool, refs []apigen.DeploymentExpectedSeq) ([]apigen.DeploymentExpectedSeq, error) {
	if !update && len(refs) != 0 {
		return nil, InvalidReferencingDeploymentsErr
	}
	seen := make(map[uint64]struct{}, len(refs))
	out := make([]apigen.DeploymentExpectedSeq, 0, len(refs))
	for _, ref := range refs {
		if ref.DeploymentID == 0 || ref.ExpectedSeq < 0 {
			return nil, InvalidReferencingDeploymentsErr
		}
		if _, duplicate := seen[ref.DeploymentID]; duplicate {
			return nil, InvalidReferencingDeploymentsErr
		}
		seen[ref.DeploymentID] = struct{}{}
		out = append(out, ref)
	}
	return out, nil
}

func versionedValueSetError(err error) error {
	switch {
	case errors.Is(err, values.ErrInvalidReferencingDeployments):
		return InvalidReferencingDeploymentsErr
	case errors.Is(err, values.ErrReferencingDeploymentsChanged):
		return ReferencingDeploymentsChangedErr
	default:
		return err
	}
}
