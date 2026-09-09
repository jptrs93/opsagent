package webuihandler

import (
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

var InvalidReferencingDeploymentsErr = apigen.NewApiErr(
	"Referencing deployments must contain unique positive IDs and current versions",
	"invalid_referencing_deployments",
	http.StatusBadRequest,
)

var ReferencingDeploymentsChangedErr = apigen.NewApiErr(
	"Referencing deployments changed; refresh and try again",
	"referencing_deployments_changed",
	http.StatusConflict,
)

func requestedDeploymentVersions(update bool, refs []*apigen.DeploymentSpecVersionRef) ([]storage.DeploymentSpecVersion, error) {
	if !update && len(refs) != 0 {
		return nil, InvalidReferencingDeploymentsErr
	}
	seen := make(map[int32]struct{}, len(refs))
	out := make([]storage.DeploymentSpecVersion, 0, len(refs))
	for _, ref := range refs {
		if ref == nil || ref.ID <= 0 || ref.SpecVersion <= 0 {
			return nil, InvalidReferencingDeploymentsErr
		}
		if _, duplicate := seen[ref.ID]; duplicate {
			return nil, InvalidReferencingDeploymentsErr
		}
		seen[ref.ID] = struct{}{}
		out = append(out, storage.DeploymentSpecVersion{ID: ref.ID, SpecVersion: ref.SpecVersion})
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
