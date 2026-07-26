package runtimeinputs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

type AssetProvider interface {
	OpenAsset(ctx context.Context, assetID int32) (*apigen.Asset, io.ReadCloser, error)
}

type requiredAssetRef struct {
	Label      string
	AssetID    int32
	Executable bool
}

func (r *RuntimeInputs) EnsureAssetsReady(ctx context.Context, cfg *apigen.DeploymentConfig) error {
	refs := RequiredAssetRefs(cfg)
	if len(refs) == 0 {
		return nil
	}
	for _, ref := range refs {
		if ref.AssetID == 0 {
			return fmt.Errorf("%s has unresolved asset id", ref.Label)
		}
		path := AssetCachePathWithMode(ref.AssetID, ref.Executable)
		mode := AssetCacheMode(ref.Executable)
		if info, err := os.Stat(path); err == nil {
			if info.Mode().Perm() != mode {
				if chmodErr := os.Chmod(path, mode); chmodErr != nil {
					return fmt.Errorf("chmod asset cache %s: %w", path, chmodErr)
				}
			}
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking asset cache %s: %w", path, err)
		}
		asset, body, err := r.assets.OpenAsset(ctx, ref.AssetID)
		if err != nil {
			return fmt.Errorf("fetching asset %d: %w", ref.AssetID, err)
		}
		if asset.ID != ref.AssetID {
			_ = body.Close()
			return fmt.Errorf("primary returned wrong asset: got %d", asset.ID)
		}
		tmp := path + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			_ = body.Close()
			return fmt.Errorf("writing asset cache %s: %w", tmp, err)
		}
		if _, err := io.Copy(out, body); err != nil {
			_ = out.Close()
			_ = body.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("writing asset cache %s: %w", tmp, err)
		}
		if err := body.Close(); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("reading asset %d: %w", ref.AssetID, err)
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("closing asset cache %s: %w", tmp, err)
		}
		if err := os.Chmod(tmp, mode); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("chmod asset cache %s: %w", tmp, err)
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
	container := cfg.Spec.Container()
	if container == nil {
		return nil
	}
	runtime := container.Runtime
	refs := make([]requiredAssetRef, 0, len(runtime.AssetMounts)+len(runtime.EnvVars))
	for _, m := range runtime.AssetMounts {
		if m == nil {
			continue
		}
		refs = append(refs, requiredAssetRef{
			Label:      fmt.Sprintf("asset mount %d", m.AssetID),
			AssetID:    m.AssetID,
			Executable: m.Permission == apigen.FilePermission_READ_EXECUTE,
		})
	}
	for key, value := range runtime.EnvVars {
		if value == nil || value.AssetID <= 0 {
			continue
		}
		refs = append(refs, requiredAssetRef{
			Label:   fmt.Sprintf("asset env var %q", key),
			AssetID: value.AssetID,
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].AssetID < refs[j].AssetID ||
			(refs[i].AssetID == refs[j].AssetID && refs[i].Label < refs[j].Label)
	})
	return refs
}

func AssetCacheDir() string {
	return ainit.StaticConfig.AssetCacheDir
}

func AssetCachePath(assetID int32) string {
	return AssetCachePathWithMode(assetID, false)
}

func AssetCachePathWithMode(assetID int32, executable bool) string {
	name := strconv.Itoa(int(assetID))
	if executable {
		name += "_x"
	}
	return filepath.Join(AssetCacheDir(), name)
}

func AssetCacheMode(executable bool) os.FileMode {
	if executable {
		return 0o755
	}
	return 0o644
}

// RetainAssets removes cached asset files whose id is absent from keep, and
// reports how many it deleted.
//
// Only names the cache itself writes are considered, so a partial download
// (which is staged as "<name>.tmp") is never collected: its name does not parse
// as an asset id at all.
func RetainAssets(keep map[int32]struct{}) (int, error) {
	entries, err := os.ReadDir(AssetCacheDir())
	if err != nil {
		return 0, fmt.Errorf("listing asset cache: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := parseAssetCacheName(entry.Name())
		if !ok {
			continue
		}
		if _, keeping := keep[id]; keeping {
			continue
		}
		if err := os.Remove(filepath.Join(AssetCacheDir(), entry.Name())); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("removing cached asset %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

// parseAssetCacheName reverses AssetCachePathWithMode's naming.
func parseAssetCacheName(name string) (int32, bool) {
	name = strings.TrimSuffix(name, "_x")
	id, err := strconv.Atoi(name)
	if err != nil || id <= 0 {
		return 0, false
	}
	return int32(id), true
}
