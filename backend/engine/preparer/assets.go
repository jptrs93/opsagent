package preparer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type AssetProvider interface {
	FetchAsset(ctx context.Context, assetID, version int32) (*apigen.ClusterAssetBlob, error)
}

var Assets AssetProvider

type requiredAssetRef struct {
	Label   string
	AssetID int32
	Version int32
}

func EnsureAssetsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	refs := RequiredAssetRefs(cfg)
	if len(refs) == 0 {
		return nil
	}
	if Assets == nil {
		return fmt.Errorf("asset provider is not configured")
	}
	if err := os.MkdirAll(AssetCacheDir(), 0o755); err != nil {
		return fmt.Errorf("creating asset cache dir: %w", err)
	}
	for _, ref := range refs {
		if ref.AssetID == 0 || ref.Version == 0 {
			return fmt.Errorf("%s has unresolved asset id/version", ref.Label)
		}
		path := AssetCachePath(ref.AssetID, ref.Version)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking asset cache %s: %w", path, err)
		}
		asset, err := Assets.FetchAsset(ctx, ref.AssetID, ref.Version)
		if err != nil {
			return fmt.Errorf("fetching asset %d version %d: %w", ref.AssetID, ref.Version, err)
		}
		if asset.AssetID != ref.AssetID || asset.Version != ref.Version {
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

func RequiredAssetRefs(cfg *apigen.DeploymentConfig) []requiredAssetRef {
	if cfg == nil {
		return nil
	}
	container := cfg.Spec.Runner.Container
	refs := make([]requiredAssetRef, 0, len(container.AssetMounts)+len(container.EnvVars))
	for _, m := range container.AssetMounts {
		if m == nil {
			continue
		}
		refs = append(refs, requiredAssetRef{
			Label:   fmt.Sprintf("asset mount %q", m.Asset),
			AssetID: m.AssetID,
			Version: m.Version,
		})
	}
	for key, value := range container.EnvVars {
		if value == nil || value.Asset == "" {
			continue
		}
		refs = append(refs, requiredAssetRef{
			Label:   fmt.Sprintf("asset env var %q", key),
			AssetID: value.AssetID,
			Version: value.Version,
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].AssetID < refs[j].AssetID ||
			(refs[i].AssetID == refs[j].AssetID && refs[i].Version < refs[j].Version) ||
			(refs[i].AssetID == refs[j].AssetID && refs[i].Version == refs[j].Version && refs[i].Label < refs[j].Label)
	})
	return refs
}

func AssetCacheDir() string {
	return ainit.StaticConfig.DataDir + "-assets"
}

func AssetCachePath(assetID, version int32) string {
	return filepath.Join(AssetCacheDir(), strconv.Itoa(int(assetID))+"_"+strconv.Itoa(int(version)))
}
