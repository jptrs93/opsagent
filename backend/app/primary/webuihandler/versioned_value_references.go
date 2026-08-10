package webuihandler

import (
	"errors"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb"
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

func requestedDeploymentVersions(update bool, refs []*apigen.DeploymentConfigVersionRef) ([]storage.DeploymentConfigVersion, error) {
	if !update && len(refs) != 0 {
		return nil, InvalidReferencingDeploymentsErr
	}
	seen := make(map[int32]struct{}, len(refs))
	out := make([]storage.DeploymentConfigVersion, 0, len(refs))
	for _, ref := range refs {
		if ref == nil || ref.ID <= 0 || ref.Version <= 0 {
			return nil, InvalidReferencingDeploymentsErr
		}
		if _, duplicate := seen[ref.ID]; duplicate {
			return nil, InvalidReferencingDeploymentsErr
		}
		seen[ref.ID] = struct{}{}
		out = append(out, storage.DeploymentConfigVersion{ID: ref.ID, Version: ref.Version})
	}
	return out, nil
}

func versionedValueSetError(err error) error {
	switch {
	case errors.Is(err, primarydb.ErrInvalidReferencingDeployments):
		return InvalidReferencingDeploymentsErr
	case errors.Is(err, primarydb.ErrReferencingDeploymentsChanged):
		return ReferencingDeploymentsChangedErr
	default:
		return err
	}
}
