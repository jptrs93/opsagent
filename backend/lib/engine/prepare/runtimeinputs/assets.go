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
	OpenAsset(ctx context.Context, ref apigen.ValueRef) (io.ReadCloser, error)
}

type requiredAssetRef struct {
	Label      string
	Ref        apigen.ValueRef
	Executable bool
}

func (r *RuntimeInputs) EnsureAssetsReady(ctx context.Context, cfg *apigen.DeploymentRecord) error {
	refs := RequiredAssetRefs(cfg)
	if len(refs) == 0 {
		return nil
	}
	for _, ref := range refs {
		if !ref.Ref.Valid() {
			return fmt.Errorf("%s has an unresolved asset reference", ref.Label)
		}
		path := AssetCachePathWithMode(ref.Ref, ref.Executable)
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
		body, err := r.assets.OpenAsset(ctx, ref.Ref)
		if err != nil {
			return fmt.Errorf("fetching asset %s: %w", ref.Ref, err)
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
			return fmt.Errorf("reading asset %s: %w", ref.Ref, err)
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

func RequiredAssetRefs(cfg *apigen.DeploymentRecord) []requiredAssetRef {
	if cfg == nil {
		return nil
	}
	container := cfg.Deployment.Spec.Container()
	if container == nil {
		return nil
	}
	runtime := container.Runtime
	refs := make([]requiredAssetRef, 0, len(runtime.AssetMounts)+len(runtime.EnvVars))
	for _, m := range runtime.AssetMounts {
		refs = append(refs, requiredAssetRef{
			Label:      fmt.Sprintf("asset mount %s", m.Asset),
			Ref:        m.Asset.Ref(),
			Executable: m.Permission == apigen.FilePermission_FILE_PERMISSION_READ_EXECUTE,
		})
	}
	for key, value := range runtime.EnvVars {
		if value.Value.Asset == nil || !value.Value.Asset.Asset.Valid() {
			continue
		}
		refs = append(refs, requiredAssetRef{
			Label: fmt.Sprintf("asset env var %q", key),
			Ref:   value.Value.Asset.Asset.Ref(),
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Ref.Less(refs[j].Ref) ||
			(refs[i].Ref == refs[j].Ref && refs[i].Label < refs[j].Label)
	})
	return refs
}

func AssetCacheDir() string {
	return ainit.StaticConfig.AssetCacheDir
}

func AssetCachePath(ref apigen.ValueRef) string {
	return AssetCachePathWithMode(ref, false)
}

// AssetCacheName is the cache file name for one asset value:
// "<asset id>@<value version>". Earlier layouts wrote "<row id>" and, before
// 2026-07, "<old asset id>_<version>"; the "@" keeps a leftover file from
// ever matching a current ref, since a cache hit is trusted by name alone.
func AssetCacheName(ref apigen.ValueRef) string {
	return strconv.FormatUint(ref.ID, 10) + "@" + strconv.FormatUint(uint64(ref.Version), 10)
}

func AssetCachePathWithMode(ref apigen.ValueRef, executable bool) string {
	name := AssetCacheName(ref)
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

// RetainAssets removes cached asset files whose ref is absent from keep, and
// reports how many it deleted.
//
// Only names the cache itself writes are considered, so a partial download
// (which is staged as "<name>.tmp") is never collected: its name does not parse
// as an asset ref at all. Files named by an earlier layout are never kept.
func RetainAssets(keep map[apigen.ValueRef]struct{}) (int, error) {
	entries, err := os.ReadDir(AssetCacheDir())
	if err != nil {
		return 0, fmt.Errorf("listing asset cache: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ref, ok := parseAssetCacheName(entry.Name())
		if !ok {
			continue
		}
		if _, keeping := keep[ref]; keeping && ref.Valid() {
			continue
		}
		if err := os.Remove(filepath.Join(AssetCacheDir(), entry.Name())); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("removing cached asset %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

// parseAssetCacheName reverses AssetCachePathWithMode's naming. Names from an
// earlier layout ("<row id>" or "<id>_<version>") parse to a zero ref, which
// no keep set holds.
func parseAssetCacheName(name string) (apigen.ValueRef, bool) {
	name = strings.TrimSuffix(name, "_x")
	if idText, versionText, ok := strings.Cut(name, "@"); ok {
		id, idOK := positiveUint(idText, 64)
		version, versionOK := positiveUint(versionText, 32)
		if !idOK || !versionOK {
			return apigen.ValueRef{}, false
		}
		return apigen.ValueRef{ID: id, Version: uint32(version)}, true
	}
	idText, versionText, paired := strings.Cut(name, "_")
	if _, ok := positiveUint(idText, 64); !ok {
		return apigen.ValueRef{}, false
	}
	if paired {
		if _, ok := positiveUint(versionText, 32); !ok {
			return apigen.ValueRef{}, false
		}
	}
	return apigen.ValueRef{}, true
}

func positiveUint(text string, bits int) (uint64, bool) {
	if text == "" || text[0] == '+' || text[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseUint(text, 10, bits)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}
