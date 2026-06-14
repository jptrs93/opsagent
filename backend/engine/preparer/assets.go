package preparer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type AssetProvider interface {
	FetchAsset(ctx context.Context, assetID, version int32) (*apigen.ClusterAssetBlob, error)
}

var Assets AssetProvider

func EnsureAssetsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	if cfg == nil || len(cfg.Spec.Runner.Container.AssetMounts) == 0 {
		return nil
	}
	if Assets == nil {
		return fmt.Errorf("asset provider is not configured")
	}
	if err := os.MkdirAll(AssetCacheDir(), 0o755); err != nil {
		return fmt.Errorf("creating asset cache dir: %w", err)
	}
	for _, m := range cfg.Spec.Runner.Container.AssetMounts {
		if m == nil {
			continue
		}
		if m.AssetID == 0 || m.Version == 0 {
			return fmt.Errorf("asset mount %q has unresolved asset id/version", m.Asset)
		}
		path := AssetCachePath(m.AssetID, m.Version)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking asset cache %s: %w", path, err)
		}
		asset, err := Assets.FetchAsset(ctx, m.AssetID, m.Version)
		if err != nil {
			return fmt.Errorf("fetching asset %d version %d: %w", m.AssetID, m.Version, err)
		}
		if asset.AssetID != m.AssetID || asset.Version != m.Version {
			return fmt.Errorf("primary returned wrong asset: got %d version %d", asset.AssetID, asset.Version)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, asset.Blob, 0o644); err != nil {
			return fmt.Errorf("writing asset cache %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("installing asset cache %s: %w", path, err)
		}
	}
	return nil
}

func AssetCacheDir() string {
	return ainit.StaticConfig.DataDir + "-assets"
}

func AssetCachePath(assetID, version int32) string {
	return filepath.Join(AssetCacheDir(), strconv.Itoa(int(assetID))+"_"+strconv.Itoa(int(version)))
}
