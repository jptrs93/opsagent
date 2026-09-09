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
	for _, id := range runtimeinputs.SecretRefs(cfg) {
		version, err := q.GetSecretValueEventByID(ctx, int64(id))
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown secret id %d", id)
		}
		if err != nil {
			return err
		}
		secret, err := q.GetSecretRowByID(ctx, int64(version.SecretID))
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown secret id %d", id)
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
	for _, id := range runtimeinputs.ConfigRefs(cfg) {
		ref, err := q.GetConfigVersionByID(ctx, int64(id))
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("unknown config id %d", id)
		}
		if err != nil {
			return err
		}
		if ref.SpaceID != int64(spaceID) && ref.SpaceID != int64(nodes.DefaultSpaceID) {
			e := ConfigRefOutsideSpaceErr
			e.DisplayErr = fmt.Sprintf("Config %q lives in space %d and cannot be referenced from a deployment in space %d", ref.Name, ref.SpaceID, spaceID)
			return e
		}
	}
	for _, ref := range runtimeinputs.RequiredAssetRefs(cfg) {
		version, err := q.GetAssetVersionJoinedByID(ctx, int64(ref.AssetVersionID))
		if errors.Is(err, sql.ErrNoRows) {
			return InvalidConfigErrf("asset version id %d not found", ref.AssetVersionID)
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
