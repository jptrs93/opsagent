package deployments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// In-lock locality checks use current database metadata, including space moves
// since the request's pre-lock validation. Secret values are never loaded.
func validateRefSpaces(ctx context.Context, q *pq.Queries, spec *apigen.DeploymentSpec, spaceID int32) error {
	cfg := &apigen.DeploymentEvent{Value: apigen.Deployment{Spec: *spec}}
	for _, ref := range runtimeinputs.SecretRefs(cfg) {
		if _, err := q.GetSecretValueEventByRef(ctx, ref); errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown secret %s", ref)
		} else if err != nil {
			return err
		}
		secret, err := q.GetSecretRowByID(ctx, int64(ref.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown secret %s", ref)
		}
		if err != nil {
			return err
		}
		if secret.SpaceID != int64(spaceID) && secret.SpaceID != int64(nodes.DefaultSpaceID) {
			e := SecretRefOutsideSpaceErr
			e.DisplayErr = fmt.Sprintf("Secret %q lives in space %d and cannot be referenced from a deployment in space %d", secret.Name, secret.SpaceID, spaceID)
			return e
		}
	}
	for _, ref := range runtimeinputs.ConfigRefs(cfg) {
		version, err := q.GetConfigVersionByRef(ctx, ref)
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown config %s", ref)
		}
		if err != nil {
			return err
		}
		if version.SpaceID != int64(spaceID) && version.SpaceID != int64(nodes.DefaultSpaceID) {
			e := ConfigRefOutsideSpaceErr
			e.DisplayErr = fmt.Sprintf("Config %q lives in space %d and cannot be referenced from a deployment in space %d", version.Name, version.SpaceID, spaceID)
			return e
		}
	}
	for _, ref := range runtimeinputs.RequiredAssetRefs(cfg) {
		version, err := q.GetAssetVersionJoinedByRef(ctx, ref.Ref)
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("asset %s not found", ref.Ref)
		}
		if err != nil {
			return err
		}
		if version.Asset.SpaceID != int64(spaceID) && version.Asset.SpaceID != int64(nodes.DefaultSpaceID) {
			e := AssetRefOutsideSpaceErr
			e.DisplayErr = fmt.Sprintf("Asset %q lives in space %d and cannot be referenced from a deployment in space %d", version.Asset.Key, version.Asset.SpaceID, spaceID)
			return e
		}
	}
	return nil
}
